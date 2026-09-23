package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type activityPageData struct {
	Active string
	Runs   []*Run

	StatusFilter   string
	ActionFilter   string
	ProviderFilter string
}

// activityFilters is read from the request's query params by both the page
// handler and the polling endpoint, so a poll while filters are active
// returns the SAME filtered set the page is showing — without that, the
// page's "did the set of visible runs change?" check would see the
// unfiltered server list as different from what's on screen and reload on
// every single poll tick.
type activityFilters struct {
	status   string
	action   string
	provider string
}

func filtersFromRequest(r *http.Request) activityFilters {
	q := r.URL.Query()
	return activityFilters{
		status:   q.Get("status"),
		action:   q.Get("action"),
		provider: strings.ToLower(strings.TrimSpace(q.Get("provider"))),
	}
}

func filterRuns(all []*Run, f activityFilters) []*Run {
	if f.status == "" && f.action == "" && f.provider == "" {
		return all
	}
	var out []*Run
	for _, run := range all {
		if f.status != "" && string(run.Status) != f.status {
			continue
		}
		if f.action != "" && run.Action != f.action {
			continue
		}
		if f.provider != "" && !strings.Contains(strings.ToLower(run.Provider), f.provider) {
			continue
		}
		out = append(out, run)
	}
	return out
}

func handleActivity(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := filtersFromRequest(r)
		app.tmpl.Render(w, "activity", activityPageData{
			Active:         "activity",
			Runs:           filterRuns(app.activity.List(), f),
			StatusFilter:   f.status,
			ActionFilter:   f.action,
			ProviderFilter: r.URL.Query().Get("provider"),
		})
	}
}

// runStatusJSON is what /activity/status returns per run — enough for the
// page's JS to patch an already-rendered row and its expanded detail (if
// open) in place, without a full reload, while a run is still going.
type runStatusJSON struct {
	ID       int    `json:"id"`
	Status   string `json:"status"`
	Duration string `json:"duration"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Err      string `json:"err"`
}

// handleActivityStatus is polled client-side while anything is running: it
// carries the live (possibly still-growing) stdout/stderr for every VISIBLE
// (i.e. filter-matching) run, so an expanded row's log updates in place
// instead of only appearing once the run finishes.
func handleActivityStatus(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := filtersFromRequest(r)
		runs := filterRuns(app.activity.List(), f)
		out := make([]runStatusJSON, len(runs))
		for i, run := range runs {
			out[i] = runStatusJSON{
				ID:       run.ID,
				Status:   string(run.Status),
				Duration: run.Duration().String(),
				Stdout:   run.Stdout,
				Stderr:   run.Stderr,
				Err:      run.Err,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"running": app.activity.AnyRunning(),
			"runs":    out,
		})
	}
}
