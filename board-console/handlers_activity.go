package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const activityPageSize = 25

type activityPageData struct {
	Active string
	Runs   []*Run

	StatusFilter   string
	ActionFilter   string
	ProviderFilter string
	CleanupView    bool // ?view=cleanup — the "Dead board cleanup" tab

	Page       int
	TotalPages int
	Total      int
	PrevURL    string // empty on the first page
	NextURL    string // empty on the last page

	Cleanup cleanupCard
}

// cleanupCard is the Cleanup tab's last/next run, shown in its toolbar.
type cleanupCard struct {
	LastRun    time.Time // zero before the first real run
	LastStatus RunStatus // empty before the first real run
	NextRun    time.Time
	DueNow     bool
}

func buildCleanupCard(app *App) cleanupCard {
	st := app.cleanup.State()
	now := time.Now()
	card := cleanupCard{NextRun: cleanupNext(st.LastRun, now), DueNow: cleanupDue(st.LastRun, now)}
	// A LastRun with no status is the clock NewCleanupStore started on a
	// fresh install, not a run — so there is nothing to show yet.
	if st.LastStatus != "" {
		card.LastRun, card.LastStatus = st.LastRun, st.LastStatus
	}
	return card
}

// handleCleanup starts the dead-board cleanup: Preview (apply=false) or
// "Run now" (apply=true). Refused with a 409 while one is already running.
func handleCleanup(app *App, apply bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.runner.StartCleanup(apply) {
			if isFetch(r) {
				actionError(w, http.StatusConflict, "A dead board cleanup is already running.")
				return
			}
		}
		actionDone(w, r, "/activity")
	}
}

// activityFilters is read from the request's query params by both the page
// handler and the polling endpoint, so a poll while filters are active
// returns the SAME filtered set the page is showing — without that, the
// page's "did the set of visible runs change?" check would see the
// unfiltered server list as different from what's on screen and reload on
// every single poll tick.
type activityFilters struct {
	status        string
	action        string
	excludeAction string
	provider      string
	page          int
}

func filtersFromRequest(r *http.Request) activityFilters {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	// The two tabs split the runs: Cleanup is pinned to the dead-board
	// cleanup, Pipeline is everything else (crawls, adds, reindexes).
	action, exclude := q.Get("action"), "close-chronic-boards"
	if q.Get("view") == "cleanup" {
		action, exclude = "close-chronic-boards", ""
	}
	return activityFilters{
		status:        q.Get("status"),
		action:        action,
		excludeAction: exclude,
		provider:      strings.ToLower(strings.TrimSpace(q.Get("provider"))),
		page:          page,
	}
}

// paginate returns the requested page of runs (clamped into range) and
// the page count. The page and the status poll both go through it, for
// the same reason they share the filters.
func paginate(runs []*Run, page int) ([]*Run, int, int) {
	totalPages := (len(runs) + activityPageSize - 1) / activityPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	page = min(max(page, 1), totalPages)
	start := (page - 1) * activityPageSize
	end := min(start+activityPageSize, len(runs))
	return runs[start:end], page, totalPages
}

// pageURL is the current Activity URL with only the page number changed,
// so paging keeps whatever filters are active.
func pageURL(r *http.Request, page int) string {
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
	return (&url.URL{Path: "/activity", RawQuery: q.Encode()}).String()
}

func filterRuns(all []*Run, f activityFilters) []*Run {
	if f.status == "" && f.action == "" && f.excludeAction == "" && f.provider == "" {
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
		if f.excludeAction != "" && run.Action == f.excludeAction {
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
		matching := filterRuns(app.activity.List(), f)
		runs, page, totalPages := paginate(matching, f.page)
		data := activityPageData{
			Active:         "activity",
			Runs:           runs,
			StatusFilter:   f.status,
			ActionFilter:   f.action,
			ProviderFilter: r.URL.Query().Get("provider"),
			CleanupView:    r.URL.Query().Get("view") == "cleanup",
			Page:           page,
			TotalPages:     totalPages,
			Total:          len(matching),
			Cleanup:        buildCleanupCard(app),
		}
		if page > 1 {
			data.PrevURL = pageURL(r, page-1)
		}
		if page < totalPages {
			data.NextURL = pageURL(r, page+1)
		}
		app.tmpl.Render(w, "activity", data)
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
		runs, _, _ := paginate(filterRuns(app.activity.List(), f), f.page)
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
