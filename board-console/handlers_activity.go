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
	Jobs   []*Job // this page's jobs, each with its steps (see jobs.go)
	// Fingerprint is jobsFingerprint of Jobs — the live poll re-renders the
	// table only when the server's fingerprint moves away from it.
	Fingerprint string

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

	ExplainEnabled bool           // OPENAI_API_KEY is set
	Explanations   map[int]string // cached answers by run ID, re-rendered on every refresh
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
// handler and the polling endpoint, so a poll returns the SAME page of jobs
// the page is showing. Every filter applies to JOBS: status is the job's
// status, action means "has a step of that action", provider is the job's.
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

// pageURL is the current Activity URL with only the page number changed,
// so paging keeps whatever filters are active.
func pageURL(r *http.Request, page int) string {
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
	return (&url.URL{Path: "/activity", RawQuery: q.Encode()}).String()
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

func handleActivity(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := filtersFromRequest(r)
		jobs, page, totalPages, total := activityJobs(app, f)
		data := activityPageData{
			Active:         "activity",
			Jobs:           jobs,
			Fingerprint:    jobsFingerprint(jobs),
			StatusFilter:   f.status,
			ActionFilter:   f.action,
			ProviderFilter: r.URL.Query().Get("provider"),
			CleanupView:    f.cleanupView,
			Page:           page,
			TotalPages:     totalPages,
			Total:          total,
			Cleanup:        buildCleanupCard(app),
			ExplainEnabled: app.explainer.Enabled(),
			Explanations:   app.explainer.Cached(),
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

// stepStatusJSON is one step of a job in the live poll: enough to patch
// its duration and, if its log is open, the growing output in place.
type stepStatusJSON struct {
	Dom      string `json:"dom"` // "<job key>-<run id>": a shared step renders once per job
	Duration string `json:"duration"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Err      string `json:"err"`
}

type jobStatusJSON struct {
	Key      string           `json:"key"`
	Duration string           `json:"duration"`
	Steps    []stepStatusJSON `json:"steps"`
}

// handleActivityStatus is polled by the Activity page: the same page of
// jobs, with a fingerprint — when it differs from what the page rendered
// (a new job, a step joining one, a status change) the page re-renders;
// otherwise it patches durations and open logs from the rest.
func handleActivityStatus(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobs, _, _, _ := activityJobs(app, filtersFromRequest(r))
		out := make([]jobStatusJSON, len(jobs))
		for i, j := range jobs {
			js := jobStatusJSON{Key: j.Key, Duration: j.Duration.String()}
			for _, s := range j.Steps {
				js.Steps = append(js.Steps, stepStatusJSON{
					Dom:      j.Key + "-" + strconv.Itoa(s.ID),
					Duration: s.Duration().String(),
					Stdout:   s.Stdout,
					Stderr:   s.Stderr,
					Err:      s.Err,
				})
			}
			out[i] = js
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"running":     app.activity.AnyRunning(),
			"fingerprint": jobsFingerprint(jobs),
			"jobs":        out,
		})
	}
}

// explainHistory is how many earlier runs of the same kind go with an
// explanation request — enough to spot "this board has failed every time".
const explainHistory = 5

// handleExplain is the "Explain this run" button: it asks the model (see
// explain.go) and returns {"explanation": "..."}.
func handleExplain(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.explainer.Enabled() {
			actionError(w, http.StatusServiceUnavailable, "Set OPENAI_API_KEY in .env to enable explanations.")
			return
		}
		id, _ := strconv.Atoi(r.FormValue("id"))
		run, ok := app.activity.Get(id)
		if !ok {
			actionError(w, http.StatusNotFound, "That run is no longer in the activity log.")
			return
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

		answer, err := app.explainer.Explain(r.Context(), run, history)
		if err != nil {
			actionError(w, http.StatusBadGateway, err.Error())
			return
		}
		html, err := app.tmpl.Fragment("explain-answer", answer)
		if err != nil {
			actionError(w, http.StatusInternalServerError, "Could not render the explanation.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"explanation": answer, "html": html})
	}
}
