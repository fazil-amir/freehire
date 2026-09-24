package main

import (
	"strings"
	"testing"
	"time"
)

func at(min int) time.Time { return time.Date(2026, 9, 24, 10, min, 0, 0, time.UTC) }

func step(id int, action, provider string, jobs []int, status RunStatus, start, end int, stderr string) *Run {
	r := &Run{ID: id, Action: action, Provider: provider, Jobs: jobs, Status: status, StartedAt: at(start), Stderr: stderr}
	if status == StatusDone || status == StatusFailed {
		r.FinishedAt = at(end)
	}
	return r
}

func jobByKey(jobs []*Job, key string) *Job {
	for _, j := range jobs {
		if j.Key == key {
			return j
		}
	}
	return nil
}

func TestBuildJobs(t *testing.T) {
	runs := []*Run{
		// greenhouse crawl (job 1): add-boards, ingest, then a reindex shared with keka (job 2)
		step(1, "add-boards", "greenhouse", []int{1}, StatusDone, 0, 0, "bulk-add-boards: done. added=3 duplicate=0 failed=0 elapsed=0s"),
		step(2, "ingest", "greenhouse", []int{1}, StatusDone, 0, 25, "ingest done: provider=greenhouse providers=1 ingested=225573 failed=0 skipped=0 rejected=0 unreadable=0"),
		step(3, "ingest", "keka", []int{2}, StatusFailed, 5, 24, "ingest done: provider=keka providers=1 ingested=4278 failed=2 skipped=0 rejected=0 unreadable=0"),
		step(4, "reindex", "", []int{1, 2}, StatusFailed, 26, 26, "reindex: refusing rebuild — free 19GiB is below the 20GiB floor"),
		// a run from before jobs existed
		step(5, "ingest", "bayt", nil, StatusFailed, 1, 1, "ingest done: provider=bayt providers=1 ingested=0 failed=8 skipped=0 rejected=0 unreadable=0"),
		// a crawl still running (job 3)
		step(6, "ingest", "adp", []int{3}, StatusRunning, 30, 0, ""),
		// a cleanup (job 4) with its own reindex and recount
		step(7, "close-chronic-boards", "", []int{4}, StatusDone, 40, 40, "close-chronic-boards: 0 chronic board(s) (0 skipped as region-ambiguous), closed 3 job(s) total"),
		step(8, "recount-companies", "", []int{4}, StatusDone, 41, 41, "recount-companies done: companies updated=7"),
	}
	jobs := buildJobs(runs)
	if len(jobs) != 5 {
		t.Fatalf("want 5 jobs, got %d", len(jobs))
	}
	if jobs[0].Key != "j4" || jobs[len(jobs)-1].Key != "j1" {
		t.Errorf("jobs must be newest first by start, got %s … %s", jobs[0].Key, jobs[len(jobs)-1].Key)
	}

	gh := jobByKey(jobs, "j1")
	if gh.Kind != "Crawl" || gh.Provider != "greenhouse" || len(gh.Steps) != 3 {
		t.Fatalf("greenhouse job: %+v", gh)
	}
	if strings.Join(gh.Actions, ",") != "add-boards,ingest,reindex" {
		t.Errorf("steps in order: %v", gh.Actions)
	}
	// crawl worked, the reindex after it failed → partial, and it says why
	if gh.Status != OutcomePartial || gh.Summary != "225,573 jobs · reindex failed" {
		t.Errorf("greenhouse status: %s / %q", gh.Status, gh.Summary)
	}
	if gh.Steps[2].SharedWith != "keka" {
		t.Errorf("the shared reindex must say who else it served, got %q", gh.Steps[2].SharedWith)
	}

	keka := jobByKey(jobs, "j2")
	if len(keka.Steps) != 2 || keka.Steps[1].Action != "reindex" || keka.Steps[1].SharedWith != "greenhouse" {
		t.Errorf("keka must include the shared reindex too: %+v", keka.Steps)
	}
	if keka.Status != OutcomePartial {
		t.Errorf("keka: ingest partial → job partial, got %s", keka.Status)
	}

	old := jobByKey(jobs, "r5")
	if old == nil || len(old.Steps) != 1 || old.Status != OutcomeFailed || old.Summary != "0 jobs · 8 boards failed" {
		t.Errorf("an old run is a single-step job: %+v", old)
	}

	if adp := jobByKey(jobs, "j3"); adp.Status != "running" || !adp.Running() {
		t.Errorf("a running step makes the job running: %s", adp.Status)
	}

	cl := jobByKey(jobs, "j4")
	if cl.Kind != "Cleanup" || !cl.isCleanup() || cl.Status != OutcomeSuccess || cl.Summary != "closed 3 jobs" {
		t.Errorf("cleanup job: %s %s %q", cl.Kind, cl.Status, cl.Summary)
	}

	// Tabs and filters work on jobs.
	pipeline := filterJobs(jobs, activityFilters{})
	cleanup := filterJobs(jobs, activityFilters{cleanupView: true})
	if len(pipeline) != 4 || len(cleanup) != 1 {
		t.Errorf("pipeline %d / cleanup %d", len(pipeline), len(cleanup))
	}
	if got := filterJobs(jobs, activityFilters{action: "reindex"}); len(got) != 2 {
		t.Errorf("action=reindex matches the jobs that HAVE a reindex step: %d", len(got))
	}
	if got := filterJobs(jobs, activityFilters{status: OutcomePartial}); len(got) != 2 {
		t.Errorf("status filter uses the job status: %d", len(got))
	}
}

func jobsOf(activity *ActivityLog, action string) [][]int {
	var out [][]int
	for _, r := range activity.List() {
		if r.Action == action {
			out = append(out, r.Jobs)
		}
	}
	return out
}

func TestRunner_StepsCarryTheirJob(t *testing.T) {
	sched, _, activity := newTestScheduler(t)
	r := sched.runner
	r.bin.Reindex = writeScript(t, "sleep 1")

	// A reindex already running…
	r.queueReindex()
	waitFor(t, "the first reindex", func() bool { return len(jobsOf(activity, "reindex")) == 1 })
	// …while two crawls finish: both are served by the NEXT reindex.
	r.bin.Ingest = writeScript(t, "true")
	r.OnCrawlFinished = nil
	done := make(chan struct{}, 2)
	r.StartCrawl("acme", true, false, func(error) { done <- struct{}{} })
	<-done
	// A second crawl finishing meanwhile asks for a reindex the same way.
	other := activity.NewJob()
	r.queueReindex(other)
	waitFor(t, "both reindexes", func() bool { return len(jobsOf(activity, "reindex")) == 2 && !reindexBusy(r) })

	ingest := jobsOf(activity, "ingest")[0]
	reindexes := jobsOf(activity, "reindex") // newest first
	if len(ingest) != 1 {
		t.Fatalf("an ingest belongs to exactly its crawl's job: %v", ingest)
	}
	shared := reindexes[0]
	if len(shared) != 2 || shared[0] != ingest[0] || shared[1] != other {
		t.Fatalf("the reindex after the running one must serve BOTH waiting jobs %v, got %v", []int{ingest[0], other}, shared)
	}
	if first := reindexes[1]; len(first) != 1 || first[0] == ingest[0] {
		t.Fatalf("the reindex already running must not claim the later crawl: %v", first)
	}
}

func TestRunner_CleanupIsOneJob(t *testing.T) {
	sched, _, activity := newTestScheduler(t)
	r := sched.runner
	for _, bin := range []*string{&r.bin.CloseChronicBoards, &r.bin.Reindex, &r.bin.RecountCompanies, &r.bin.ReindexCompanies} {
		*bin = writeScript(t, "true")
	}
	if !r.StartCleanup(true) {
		t.Fatal("cleanup did not start")
	}
	waitFor(t, "the cleanup's steps", func() bool {
		return len(jobsOf(activity, "reindex-companies")) == 1 && len(jobsOf(activity, "reindex")) == 1
	})
	want := jobsOf(activity, "close-chronic-boards")[0][0]
	for _, a := range []string{"reindex", "recount-companies", "reindex-companies"} {
		if got := jobsOf(activity, a)[0]; len(got) != 1 || got[0] != want {
			t.Errorf("%s belongs to %v, want the cleanup's job %d", a, got, want)
		}
	}
	if jobs := buildJobs(activity.List()); len(jobs) != 1 || jobs[0].Kind != "Cleanup" || len(jobs[0].Steps) != 4 {
		t.Fatalf("want ONE Cleanup job with 4 steps, got %d jobs: %+v", len(jobs), jobs[0])
	}
}

func reindexBusy(r *Runner) bool {
	r.reindexMu.Lock()
	defer r.reindexMu.Unlock()
	return r.reindexRunning
}
