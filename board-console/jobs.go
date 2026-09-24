package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Job is one thing the operator (or a schedule) asked for — a crawl, a
// cleanup, a reindex — with the runs it took as its steps. Activity lists
// jobs, not runs: "Add + Crawl greenhouse" is one row whose steps are
// add-boards, ingest and reindex, instead of three unrelated-looking rows
// interleaved with everything else.
type Job struct {
	Key       string // "j<id>", or "r<run id>" for a run recorded before jobs existed
	Kind      string // "Crawl", "Full re-crawl", "Cleanup", "Cleanup preview", "Reindex", "Recount companies", "Remove provider", "Clear build cache"
	Provider  string // empty for catalogue-wide jobs
	Steps     []*JobStep
	StartedAt time.Time
	Status    string // success / partial / failed / running
	Summary   string
	Duration  time.Duration
	Actions   []string // the steps' actions in order, for the Job column's pills
}

// JobStep is one run within a job.
type JobStep struct {
	*Run
	Outcome RunOutcome
	// SharedWith names the other jobs this run also served — a reindex
	// that ran once for two crawls shows under both, "shared with keka".
	SharedWith string
}

// Running reports whether any step is still going.
func (j *Job) Running() bool { return j.Status == "running" }

// isCleanup reports whether the job belongs on the Cleanup tab.
func (j *Job) isCleanup() bool { return strings.HasPrefix(j.Kind, "Cleanup") }

// hasAction reports whether any step ran action.
func (j *Job) hasAction(action string) bool {
	for _, s := range j.Steps {
		if s.Action == action {
			return true
		}
	}
	return false
}

// buildJobs groups runs (in any order) into jobs, newest job first by when
// its first step started. A run with several jobs — the shared reindex —
// becomes a step of each. A run with none, recorded before jobs existed,
// is a job of its own: nothing is guessed about what it belonged to.
func buildJobs(runs []*Run) []*Job {
	byKey := map[string]*Job{}
	var order []*Job
	for _, run := range runs {
		keys := []string{"r" + strconv.Itoa(run.ID)}
		if len(run.Jobs) > 0 {
			keys = keys[:0]
			for _, id := range run.Jobs {
				keys = append(keys, "j"+strconv.Itoa(id))
			}
		}
		for _, k := range keys {
			j, ok := byKey[k]
			if !ok {
				j = &Job{Key: k}
				byKey[k] = j
				order = append(order, j)
			}
			j.Steps = append(j.Steps, &JobStep{Run: run, Outcome: run.Outcome()})
		}
	}

	for _, j := range order {
		sort.Slice(j.Steps, func(a, b int) bool {
			if !j.Steps[a].StartedAt.Equal(j.Steps[b].StartedAt) {
				return j.Steps[a].StartedAt.Before(j.Steps[b].StartedAt)
			}
			return j.Steps[a].ID < j.Steps[b].ID
		})
		j.finish()
	}

	// "shared with …": each step that also belongs to other jobs names them.
	for _, j := range order {
		for _, s := range j.Steps {
			if len(s.Jobs) < 2 {
				continue
			}
			var others []string
			for _, id := range s.Jobs {
				k := "j" + strconv.Itoa(id)
				if other, ok := byKey[k]; ok && k != j.Key {
					others = append(others, other.label())
				}
			}
			s.SharedWith = strings.Join(others, ", ")
		}
	}

	sort.SliceStable(order, func(a, b int) bool { return order[a].StartedAt.After(order[b].StartedAt) })
	return order
}

// label is how another job refers to this one: its provider, or its kind
// when it has none ("shared with Cleanup").
func (j *Job) label() string {
	if j.Provider != "" {
		return j.Provider
	}
	return j.Kind
}

// finish fills a job's derived fields from its (sorted) steps.
func (j *Job) finish() {
	j.StartedAt = j.Steps[0].StartedAt
	var end time.Time
	running := false
	seen := map[string]bool{}
	for _, s := range j.Steps {
		if j.Provider == "" && s.Provider != "" {
			j.Provider = s.Provider
		}
		if !seen[s.Action] {
			seen[s.Action] = true
			j.Actions = append(j.Actions, s.Action)
		}
		if s.Status == StatusRunning || s.Status == StatusQueued {
			running = true
		} else if s.FinishedAt.After(end) {
			end = s.FinishedAt
		}
	}
	j.Kind = jobKind(j.Steps)
	if running || end.IsZero() {
		end = time.Now()
	}
	j.Duration = end.Sub(j.StartedAt).Round(time.Second)

	main := j.mainStep()
	switch {
	case running:
		j.Status = "running"
		if main.Outcome.Status != "running" && main.Outcome.Status != "queued" {
			j.Summary = main.Outcome.Summary // the crawl is done; a later step still runs
		}
	case main.Outcome.Status == OutcomeFailed:
		j.Status, j.Summary = OutcomeFailed, main.Outcome.Summary
	default:
		// The main step decides; a later step that failed downgrades the job
		// to partial — jobs came in, but e.g. search was not updated.
		j.Status, j.Summary = main.Outcome.Status, main.Outcome.Summary
		var failed []string
		for _, s := range j.Steps {
			if s != main && s.Outcome.Status == OutcomeFailed {
				failed = append(failed, s.Action+" failed")
			}
		}
		if len(failed) > 0 {
			j.Status = OutcomePartial
			j.Summary = strings.TrimPrefix(j.Summary+" · "+strings.Join(failed, " · "), " · ")
		}
	}
}

// jobKind names a job by the steps it holds.
func jobKind(steps []*JobStep) string {
	has := map[string]*JobStep{}
	for _, s := range steps {
		if _, ok := has[s.Action]; !ok {
			has[s.Action] = s
		}
	}
	switch {
	case has["close-chronic-boards"] != nil:
		if has["close-chronic-boards"].Label == "dry run" {
			return "Cleanup preview"
		}
		return "Cleanup"
	case has["remove-boards"] != nil:
		return "Remove provider"
	case has["ingest"] != nil:
		for _, s := range steps {
			if s.Action == "ingest" && s.Label == "full re-crawl" {
				return "Full re-crawl"
			}
		}
		return "Crawl"
	case has["add-boards"] != nil:
		return "Crawl" // its add step failed before the ingest could start
	case has["recount-companies"] != nil || has["reindex-companies"] != nil:
		return "Recount companies"
	case has["reindex"] != nil:
		return "Reindex"
	case has["prune-build-cache"] != nil:
		return "Clear build cache"
	}
	return steps[0].Action
}

// mainStep is the step whose outcome is the job's: the ingest of a crawl,
// the cleanup of a cleanup, and so on — never a reindex that followed.
func (j *Job) mainStep() *JobStep {
	var want []string
	switch j.Kind {
	case "Crawl", "Full re-crawl":
		want = []string{"ingest", "add-boards"}
	case "Cleanup", "Cleanup preview":
		want = []string{"close-chronic-boards"}
	case "Remove provider":
		want = []string{"remove-boards"}
	case "Recount companies":
		want = []string{"recount-companies", "reindex-companies"}
	}
	for _, a := range want {
		for _, s := range j.Steps {
			if s.Action == a {
				return s
			}
		}
	}
	return j.Steps[0]
}

// jobsFingerprint changes whenever the page would render differently: a
// job appears, a step joins it, or a status changes. The Activity page
// polls it and re-renders only when it moves.
func jobsFingerprint(jobs []*Job) string {
	var b strings.Builder
	for _, j := range jobs {
		b.WriteString(j.Key + ":" + j.Status)
		for _, s := range j.Steps {
			b.WriteString("," + strconv.Itoa(s.ID) + "=" + s.Outcome.Status)
		}
		b.WriteString(";")
	}
	return b.String()
}
