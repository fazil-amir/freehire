package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// The Schedules and Activity pages' part of the JSON API (see api.go): the
// same data the HTML pages render, from the same builders, and their
// actions.

func registerPageAPI(api func(string, http.HandlerFunc), app *App) {
	api("GET /api/v1/schedules", handleAPISchedules(app))
	api("POST /api/v1/schedules/{id}/toggle", handleAPIToggleSchedule(app))
	api("POST /api/v1/system-jobs/{key}/toggle", handleAPISystemToggle(app))
	api("PUT /api/v1/system-jobs/{key}/time", handleAPISystemTime(app))
	api("POST /api/v1/system-jobs/{key}/run", handleAPISystemRun(app))
	api("GET /api/v1/runs/{id}", handleAPIRun(app))
	api("POST /api/v1/runs/{id}/explain", handleAPIExplain(app))
	api("GET /api/v1/activity", handleAPIActivity(app))
	api("POST /api/v1/cleanup/preview", handleAPICleanup(app, false))
	api("POST /api/v1/cleanup/run", handleAPICleanup(app, true))
	api("POST /api/v1/companies/refresh", handleAPICompanyRefresh(app))
}

// optTime is t, or null when it is zero.
func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// rawOr is a builder's JSON string as-is, or fallback when it is empty.
func rawOr(s, fallback string) json.RawMessage {
	if s == "" {
		s = fallback
	}
	return json.RawMessage(s)
}

// --- schedules ---

type apiLastCrawl struct {
	Status  string     `json:"status"` // running / success / partial / failed / interrupted; "" = never
	Badge   string     `json:"badge"`  // the colour it reads as
	Summary string     `json:"summary"`
	At      *time.Time `json:"at"`
}

func toAPILastCrawl(v lastCrawlView) apiLastCrawl {
	return apiLastCrawl{Status: v.Status, Badge: v.Badge, Summary: v.Summary, At: optTime(v.At)}
}

type apiScheduleRow struct {
	ID       string          `json:"id"`
	Provider string          `json:"provider"`
	Times    []int           `json:"times"` // minutes after 00:00 UTC
	Enabled  bool            `json:"enabled"`
	Running  bool            `json:"running"` // its own run, or any crawl of the provider, is in flight
	Queued   bool            `json:"queued"`  // due, but SCHEDULE_CAPACITY crawls already run
	DueNow   bool            `json:"dueNow"`
	NextRun  *time.Time      `json:"nextRun"`
	Last     apiLastCrawl    `json:"last"`
	Slots    json.RawMessage `json:"slots"` // [{m, s, at, sum, run}] — see slotRun
}

type apiUnscheduledRow struct {
	Provider string          `json:"provider"`
	Crawling bool            `json:"crawling"`
	Last     apiLastCrawl    `json:"last"`
	Runs     json.RawMessage `json:"runs"` // its crawls of the last 24 hours, as slotRun
}

type apiSystemRow struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ActivityURL string          `json:"activityUrl"` // its runs, as the page's own Activity link (query string included)
	Enabled     bool            `json:"enabled"`
	Minute      int             `json:"minute"` // its daily time, minutes after 00:00 UTC
	Running     bool            `json:"running"`
	LastRun     *time.Time      `json:"lastRun"`
	LastStatus  string          `json:"lastStatus"` // success / failed; "" = never
	NextRun     *time.Time      `json:"nextRun"`    // null while paused
	Slot        json.RawMessage `json:"slot"`       // its one daily run, as slotRun
}

// handleAPISchedules is the Schedules page: the load strip, the scheduled
// providers, the added ones without a schedule and the system jobs — the
// same rows buildSchedulesPageData renders.
func handleAPISchedules(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := buildSchedulesPageData(app, r)
		scheduled := make([]apiScheduleRow, 0, len(d.Schedules))
		for _, s := range d.Schedules {
			scheduled = append(scheduled, apiScheduleRow{
				ID: s.ID, Provider: s.Provider, Times: s.Times, Enabled: s.Enabled,
				Running: s.Running(), Queued: s.Queued,
				DueNow:  s.Enabled && !s.Running() && s.NextRun.IsZero(),
				NextRun: optTime(s.NextRun), Last: toAPILastCrawl(s.LastCrawl()), Slots: rawOr(s.Slots, "[]"),
			})
		}
		unscheduled := make([]apiUnscheduledRow, 0, len(d.Unscheduled))
		for _, u := range d.Unscheduled {
			unscheduled = append(unscheduled, apiUnscheduledRow{Provider: u.Provider, Crawling: u.Crawling, Last: toAPILastCrawl(u.Last), Runs: rawOr(u.Runs, "[]")})
		}
		system := make([]apiSystemRow, 0, len(d.System))
		for _, s := range d.System {
			row := apiSystemRow{
				Key: s.Key, Name: s.Info.Name, Description: s.Info.Description, ActivityURL: s.Info.ActivityURL,
				Enabled: s.Enabled, Minute: s.Minute, Running: s.Running,
				LastRun: optTime(s.LastRun), NextRun: optTime(s.NextRun), Slot: rawOr(s.Slot, "[]"),
			}
			if !s.LastRun.IsZero() {
				row.LastStatus = s.LastBadge()
			}
			system = append(system, row)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"capacity":       d.Capacity,
			"load":           loadProfile(app.schedules.List()),
			"dbError":        d.DBError,
			"scheduled":      scheduled,
			"unscheduled":    unscheduled,
			"system":         system,
			"addedProviders": addedProviderNames(d.ScheduleModal),
		})
	}
}

// handleAPIToggleSchedule pauses or resumes a schedule.
func handleAPIToggleSchedule(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := app.schedules.Toggle(r.PathValue("id")); err != nil {
			actionError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// systemKey is the path's system job, or a 404 already answered.
func systemKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	if _, ok := systemJobInfoOf(key); !ok {
		actionError(w, http.StatusNotFound, "unknown system job "+strconv.Quote(key))
		return "", false
	}
	return key, true
}

func handleAPISystemToggle(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := systemKey(w, r)
		if !ok {
			return
		}
		if err := app.system.Toggle(key); err != nil {
			actionError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleAPISystemTime re-times a system job: {"minute": <minutes after
// 00:00 UTC>}, on the 15-minute grid.
func handleAPISystemTime(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := systemKey(w, r)
		if !ok {
			return
		}
		var in struct {
			Minute *int `json:"minute"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		if in.Minute == nil {
			actionError(w, http.StatusUnprocessableEntity, "Pick the hour and the minutes.")
			return
		}
		if err := app.system.SetTime(key, *in.Minute); err != nil {
			actionError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleAPISystemRun is a system job's "Run now": the cleanup for real, or
// the company recount. 409 while one is already running.
func handleAPISystemRun(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := systemKey(w, r)
		if !ok {
			return
		}
		// The same refusals the page's own Run now buttons give.
		if key == sysCleanup && !app.runner.StartCleanup(true) {
			actionError(w, http.StatusConflict, "A dead board cleanup is already running.")
			return
		}
		if key == sysRecount && !app.runner.StartCompanyRefresh() {
			actionError(w, http.StatusConflict, "A company recount is already running.")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"key": key})
	}
}

// --- runs ---

type apiExplainSection struct {
	Label string `json:"label"`
	Body  string `json:"body"`
}

func toAPISections(answer string) []apiExplainSection {
	out := []apiExplainSection{}
	for _, s := range explainSections(answer) {
		out = append(out, apiExplainSection{s.Label, s.Body})
	}
	return out
}

// handleAPIRun is one run's log: what it is, how it went, its output, and
// its explanation when one was asked for already.
func handleAPIRun(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		run, ok := app.activity.Get(id)
		if err != nil || !ok {
			actionError(w, http.StatusNotFound, "run not found — it may have been trimmed from the log")
			return
		}
		o := run.Outcome()
		var sections []apiExplainSection
		if answer, ok := app.explainer.Cached()[run.ID]; ok {
			sections = toAPISections(answer)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": run.ID, "action": run.Action, "provider": run.Provider, "label": run.Label,
			"status": o.Status, "summary": o.Summary,
			"startedAt": run.StartedAt, "finishedAt": optTime(run.FinishedAt), "duration": run.Duration().String(),
			"stdout": run.Stdout, "stderr": run.Stderr, "err": run.Err,
			"explanation":    sections, // null until asked for
			"explainEnabled": app.explainer.Enabled(),
		})
	}
}

// handleAPIExplain asks the model about one run: {"sections": [...]}.
func handleAPIExplain(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		answer, status, msg := explainRun(app, r.Context(), id)
		if status != 0 {
			actionError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sections": toAPISections(answer)})
	}
}

// --- activity ---

type apiStep struct {
	ID         int       `json:"id"`
	Action     string    `json:"action"`
	Label      string    `json:"label"`
	SharedWith string    `json:"sharedWith"`
	Status     string    `json:"status"`
	Summary    string    `json:"summary"`
	Duration   string    `json:"duration"`
	StartedAt  time.Time `json:"startedAt"`
	Running    bool      `json:"running"`
}

type apiJob struct {
	Key       string    `json:"key"`
	Kind      string    `json:"kind"`
	Provider  string    `json:"provider"`
	Status    string    `json:"status"`
	Summary   string    `json:"summary"`
	Duration  string    `json:"duration"`
	StartedAt time.Time `json:"startedAt"`
	Actions   []string  `json:"actions"`
	Steps     []apiStep `json:"steps"`
}

// handleAPIActivity is the Activity page: ?view=cleanup for the Cleanup tab,
// ?status=, ?action=, ?provider=, ?page= — the same jobs, filters and paging
// as the page — plus the cleanup's last and next run.
func handleAPIActivity(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := filtersFromRequest(r)
		jobs, page, totalPages, total := activityJobs(app, f)
		out := make([]apiJob, 0, len(jobs))
		for _, j := range jobs {
			aj := apiJob{
				Key: j.Key, Kind: j.Kind, Provider: j.Provider, Status: j.Status, Summary: j.Summary,
				Duration: j.Duration.String(), StartedAt: j.StartedAt, Actions: j.Actions, Steps: []apiStep{},
			}
			for _, s := range j.Steps {
				aj.Steps = append(aj.Steps, apiStep{
					ID: s.ID, Action: s.Action, Label: s.Label, SharedWith: s.SharedWith,
					Status: s.Outcome.Status, Summary: s.Outcome.Summary, Duration: s.Duration().String(),
					StartedAt: s.StartedAt, Running: s.Status == StatusRunning || s.Status == StatusQueued,
				})
			}
			out = append(out, aj)
		}
		c := buildCleanupCard(app)
		writeJSON(w, http.StatusOK, map[string]any{
			"jobs": out, "page": page, "totalPages": totalPages, "total": total,
			"running":        app.activity.AnyRunning(),
			"explainEnabled": app.explainer.Enabled(),
			"cleanup": map[string]any{
				"lastRun": optTime(c.LastRun), "lastStatus": c.LastStatus,
				"nextRun": optTime(c.NextRun), "dueNow": c.DueNow, "paused": c.Paused,
			},
		})
	}
}

// handleAPICleanup is the Cleanup tab's Preview (apply=false, changes
// nothing) and Run now. 409 while one is already running.
func handleAPICleanup(app *App, apply bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.runner.StartCleanup(apply) {
			actionError(w, http.StatusConflict, "A dead board cleanup is already running.")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"apply": apply})
	}
}

// handleAPICompanyRefresh is "Recount companies". 409 while one runs.
func handleAPICompanyRefresh(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.runner.StartCompanyRefresh() {
			actionError(w, http.StatusConflict, "A company recount is already running.")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{})
	}
}
