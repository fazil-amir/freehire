package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const activityPageSize = 25

// cleanupCard is the Cleanup tab's last/next run, shown in its toolbar.
type cleanupCard struct {
	LastRun    time.Time // zero before the first real run
	LastStatus RunStatus // empty before the first real run
	NextRun    time.Time
	DueNow     bool
	Paused     bool // switched off on the Schedules page
}

func buildCleanupCard(app *App) cleanupCard {
	j := app.system.Get(sysCleanup)
	now := time.Now().Round(0)
	return cleanupCard{LastRun: j.LastRun, LastStatus: j.LastStatus, NextRun: j.NextRun(now), DueNow: j.Due(now), Paused: !j.Enabled}
}

// activityFilters is read from the request's query params, so a caller
// polling with the same query gets the same page of jobs. Every filter
// applies to JOBS: status is the job's status, action means "has a step of
// that action", provider is the job's.
type activityFilters struct {
	status      string
	action      string
	provider    string
	cleanupView bool // ?view=cleanup: Cleanup jobs; otherwise everything else
	page        int
}

func filtersFromRequest(r *http.Request) activityFilters {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	return activityFilters{
		status:      q.Get("status"),
		action:      q.Get("action"),
		provider:    strings.ToLower(strings.TrimSpace(q.Get("provider"))),
		cleanupView: q.Get("view") == "cleanup",
		page:        page,
	}
}

// paginate returns the requested page of jobs (clamped into range) and the
// page count.
func paginate(jobs []*Job, page int) ([]*Job, int, int) {
	totalPages := (len(jobs) + activityPageSize - 1) / activityPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	page = min(max(page, 1), totalPages)
	start := (page - 1) * activityPageSize
	end := min(start+activityPageSize, len(jobs))
	return jobs[start:end], page, totalPages
}

// filterJobs keeps the jobs the filters (and tab) select.
func filterJobs(all []*Job, f activityFilters) []*Job {
	var out []*Job
	for _, j := range all {
		if j.isCleanup() != f.cleanupView {
			continue
		}
		if f.status != "" && j.Status != f.status {
			continue
		}
		if f.action != "" && !j.hasAction(f.action) {
			continue
		}
		if f.provider != "" && !strings.Contains(strings.ToLower(j.Provider), f.provider) {
			continue
		}
		out = append(out, j)
	}
	return out
}

// activityJobs is the page of jobs the request asks for, plus how many
// matched in total.
func activityJobs(app *App, f activityFilters) ([]*Job, int, int, int) {
	matching := filterJobs(buildJobs(app.activity.List()), f)
	jobs, page, totalPages := paginate(matching, f.page)
	return jobs, page, totalPages, len(matching)
}

// explainHistory is how many earlier runs of the same kind go with an
// explanation request — enough to spot "this board has failed every time".
const explainHistory = 5

// explainRun asks the model about one run (see explain.go), with the
// earlier runs of the same action for the same provider as history. A
// refusal returns its HTTP status and message.
func explainRun(app *App, ctx context.Context, id int) (string, int, string) {
	if !app.explainer.Enabled() {
		return "", http.StatusServiceUnavailable, "Set OPENAI_API_KEY in .env to enable explanations."
	}
	run, ok := app.activity.Get(id)
	if !ok {
		return "", http.StatusNotFound, "That run is no longer in the activity log."
	}
	// Earlier runs of the same action for the same provider, newest first.
	var history []*Run
	for _, h := range app.activity.List() {
		if h.ID < run.ID && h.Action == run.Action && h.Provider == run.Provider {
			history = append(history, h)
			if len(history) == explainHistory {
				break
			}
		}
	}
	answer, err := app.explainer.Explain(ctx, run, history)
	if err != nil {
		return "", http.StatusBadGateway, err.Error()
	}
	return answer, 0, ""
}
