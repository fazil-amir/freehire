package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveProvider_RetiresEveryBoardInOneRunAndDropsSchedules(t *testing.T) {
	sched, store, activity := newTestScheduler(t) // has one schedule for "acme"
	r := sched.runner
	calls := filepath.Join(t.TempDir(), "calls")
	// Fake add-board: records its arguments; "gone" is already retired.
	r.bin.AddBoard = writeScript(t, `echo "$*" >> `+calls+`
case "$*" in *--board=gone*) echo "no live board" >&2; exit 1;; esac
echo "add-board: retired $*" >&2`)

	boards := []liveBoard{{"acme", "one", ""}, {"acme", "two", "eu"}, {"acme", "gone", ""}}
	var deleted int
	if !r.StartRemoveProvider("acme", boards, func() int { deleted, _ = store.DeleteByProvider("acme"); return deleted }, nil) {
		t.Fatal("removal did not start")
	}
	waitFor(t, "the removal to finish", func() bool { return !r.Crawling("acme") })

	got, _ := os.ReadFile(calls)
	for _, want := range []string{
		"--retire --provider=acme --board=one --apply",
		"--retire --provider=acme --board=two --apply --region=eu",
		"--retire --provider=acme --board=gone --apply",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("add-board was not called with %q; calls:\n%s", want, got)
		}
	}
	if deleted != 1 || len(store.List()) != 0 {
		t.Errorf("want the provider's schedule deleted, deleted=%d left=%d", deleted, len(store.List()))
	}

	var runs []*Run
	for _, run := range activity.List() {
		if run.Action == "remove-boards" {
			runs = append(runs, run)
		}
	}
	if len(runs) != 1 {
		t.Fatalf("want ONE remove-boards run for all boards, got %d", len(runs))
	}
	o := runs[0].Outcome()
	if o.Status != OutcomePartial || o.Summary != "2 boards retired · 1 schedule deleted · 1 failed" {
		t.Errorf("outcome = %+v", o)
	}
}

func TestRemoveProvider_RefusedWhileTheProviderIsCrawling(t *testing.T) {
	sched, store, _ := newTestScheduler(t)
	r := sched.runner
	done := make(chan struct{})
	if !r.StartCrawl("acme", false, false, func(error) { close(done) }) {
		t.Fatal("crawl did not start")
	}
	if r.StartRemoveProvider("acme", []liveBoard{{"acme", "one", ""}}, func() int { n, _ := store.DeleteByProvider("acme"); return n }, nil) {
		t.Fatal("a removal must be refused while the provider is crawling")
	}
	if len(store.List()) != 1 {
		t.Fatal("a refused removal must not delete schedules")
	}
	<-done
}

// A removal that retires every board purges the provider's activity — in
// memory and in the file, so it stays gone after a restart — and leaves
// other providers' runs alone.
func TestRemoveProvider_CleanRemovalPurgesTheProvidersActivity(t *testing.T) {
	sched, store, activity := newTestSchedulerFor(t, "acme", "keka")
	r := sched.runner
	r.bin.AddBoard = writeScript(t, "true")
	for _, p := range []string{"acme", "keka"} {
		activity.Finish(activity.Start("ingest", p, activity.NewJob()), nil)
	}

	var purged []int
	purge := func() { purged = activity.PurgeProvider("acme") }
	if !r.StartRemoveProvider("acme", []liveBoard{{"acme", "one", ""}}, func() int { n, _ := store.DeleteByProvider("acme"); return n }, purge) {
		t.Fatal("removal did not start")
	}
	waitFor(t, "the removal to finish", func() bool { return !r.Crawling("acme") })

	if len(purged) != 2 { // its crawl and the removal run itself
		t.Errorf("want 2 acme runs purged, got %v", purged)
	}
	check := func(where string, runs []*Run) {
		for _, run := range runs {
			if run.Provider == "acme" {
				t.Errorf("%s: acme run %d (%s) survived the purge", where, run.ID, run.Action)
			}
		}
		if len(runs) != 1 || runs[0].Provider != "keka" {
			t.Errorf("%s: want only keka's run left, got %d runs", where, len(runs))
		}
	}
	check("memory", activity.List())
	reloaded, _, _ := loadActivityRuns(activity.path, 100)
	check("file", reloaded)

	// The log keeps working after the rewrite.
	activity.Finish(activity.Start("ingest", "keka", activity.NewJob()), nil)
	reloaded, _, _ = loadActivityRuns(activity.path, 100)
	if len(reloaded) != 2 {
		t.Errorf("want the new run appended after the purge, got %d runs", len(reloaded))
	}
}
