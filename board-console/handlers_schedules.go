package main

import (
	"net/http"
	"sort"
	"strconv"
	"time"
)

// addScheduleForm is the add/edit schedule modal's field values — round-
// tripped back into the template on a validation failure so nothing the
// operator typed is lost. ID is empty for a new schedule, set for an edit.
type addScheduleForm struct {
	ID            string
	Provider      string
	IntervalValue string
	IntervalUnit  string
	ReindexAfter  bool
}

type scheduleRow struct {
	Schedule
	NextRun  time.Time
	Interval string // "15m"/"6h", see formatInterval
	// The interval split back into the modal's value+unit fields, for the
	// kebab menu's Edit item to hand to openScheduleModal.
	IntervalValue string
	IntervalUnit  string
	// The provider's most recent crawl (add-boards or ingest) from the
	// activity log, scheduled or manual — the expandable output under the
	// row. Nil when the log holds none.
	LatestRun *Run
	// Crawling: a crawl of this provider is in flight right now, whoever
	// started it (this schedule, a Crawl click, a bulk run).
	Crawling bool
}

// Running reports whether the row should read as running: its own run, or
// any other crawl of the same provider, is in flight.
func (r scheduleRow) Running() bool { return r.LastStatus == "running" || r.Crawling }

// lastCrawlView is what the Last run cell shows.
type lastCrawlView struct {
	Status  string // badge text: running / success / partial / failed / interrupted
	Badge   string // badge class suffix
	Summary string
	At      time.Time // zero: never run
}

// LastCrawl is the provider's most recent crawl, however it was started —
// the schedule's own record and the activity log's newest crawl, whichever
// is newer. A schedule's own record alone went stale the moment the
// provider was crawled by hand: after a restart cut the scheduled run off
// it kept saying "interrupted" beside a manual crawl that was running.
func (r scheduleRow) LastCrawl() lastCrawlView {
	latest := r.LatestRun
	if r.Running() {
		at := r.LastRun
		if latest != nil && latest.StartedAt.After(at) {
			at = latest.StartedAt
		}
		return lastCrawlView{Status: "running", Badge: "running", At: at}
	}
	if latest != nil && !latest.StartedAt.Before(r.LastRun) {
		o := latest.Outcome()
		return lastCrawlView{Status: o.Status, Badge: o.Status, Summary: o.Summary, At: latest.StartedAt}
	}
	if r.LastRun.IsZero() {
		return lastCrawlView{}
	}
	status := r.LastStatus
	if status == "ok" { // files written before Outcome existed
		status = OutcomeSuccess
	}
	badge := status
	if status == "interrupted" {
		badge = OutcomeFailed
	}
	return lastCrawlView{Status: status, Badge: badge, At: r.LastRun}
}

// scheduleModal is the shared add/edit schedule dialog's state — the
// "schedule-modal" template, rendered by every page that can add or edit a
// schedule (Schedules and Catalog), each passing its own provider list.
type scheduleModal struct {
	Form      addScheduleForm
	Error     string
	Open      bool     // reopen on load: a no-JS validation failure round-trip
	Providers []string // the provider dropdown's options
}

func newScheduleModal(providers []string) scheduleModal {
	return scheduleModal{
		Form:      addScheduleForm{IntervalValue: "30", IntervalUnit: "minutes"},
		Providers: providers,
	}
}

type schedulesPageData struct {
	Active        string
	Schedules     []scheduleRow
	DBError       string // set when the provider list fell back to the CSV's stale column
	ScheduleModal scheduleModal

	ExplainEnabled bool           // see activityPageData
	Explanations   map[int]string // cached answers by run ID
}

func buildSchedulesPageData(app *App, r *http.Request) schedulesPageData {
	// List() is newest-first, so the first match per provider is its latest.
	latest := map[string]*Run{}
	for _, run := range app.activity.List() {
		if run.Action != "ingest" && run.Action != "add-boards" {
			continue
		}
		if _, ok := latest[run.Provider]; !ok {
			latest[run.Provider] = run
		}
	}

	var rows []scheduleRow
	for _, s := range app.schedules.List() {
		value, unit := splitInterval(s.IntervalSecs)
		rows = append(rows, scheduleRow{
			Schedule: s, NextRun: s.NextRun(), Interval: formatInterval(s.Interval()),
			IntervalValue: value, IntervalUnit: unit,
			LatestRun: latest[s.Provider],
			Crawling:  app.runner.Crawling(s.Provider),
		})
	}

	addedCounts, dbError := resolveAddedCounts(r.Context(), app)
	var knownProviders []string
	for p, n := range addedCounts {
		if n > 0 {
			knownProviders = append(knownProviders, p)
		}
	}
	sort.Strings(knownProviders)

	return schedulesPageData{
		Active:        "schedules",
		Schedules:     rows,
		DBError:       dbError,
		ScheduleModal: newScheduleModal(knownProviders),

		ExplainEnabled: app.explainer.Enabled(),
		Explanations:   app.explainer.Cached(),
	}
}

// splitInterval is formatInterval's inverse, for pre-filling the edit
// modal's separate value+unit fields from a schedule's stored seconds.
func splitInterval(secs int64) (value, unit string) {
	d := time.Duration(secs) * time.Second
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d / (24 * time.Hour))), "days"
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.Itoa(int(d / time.Hour)), "hours"
	default:
		return strconv.Itoa(int(d / time.Minute)), "minutes"
	}
}

// handleSchedules also opens the add/edit modal pre-filled, via
// ?open=add&provider=X or ?open=edit&id=Y — the no-JS fallback behind the
// kebab menus' Add/Edit items, which with JS open the modal in place.
func handleSchedules(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := buildSchedulesPageData(app, r)

		switch r.URL.Query().Get("open") {
		case "add":
			data.ScheduleModal.Open = true
			data.ScheduleModal.Form.Provider = r.URL.Query().Get("provider")
			ensureProviderListed(&data.ScheduleModal, data.ScheduleModal.Form.Provider)
		case "edit":
			if sch, ok := app.schedules.Get(r.URL.Query().Get("id")); ok {
				value, unit := splitInterval(sch.IntervalSecs)
				data.ScheduleModal.Open = true
				data.ScheduleModal.Form = addScheduleForm{
					ID:            sch.ID,
					Provider:      sch.Provider,
					IntervalValue: value,
					IntervalUnit:  unit,
					ReindexAfter:  sch.ReindexAfter,
				}
				ensureProviderListed(&data.ScheduleModal, sch.Provider)
			}
		}

		app.tmpl.Render(w, "schedules", data)
	}
}

// ensureProviderListed guarantees the modal's provider dropdown always
// contains whichever provider it's pre-filled with — a schedule being
// edited might target a provider that's since fallen out of the "added"
// set (dropped in the DB after the schedule was created), and without
// this the pre-filled <option> simply wouldn't exist to select.
func ensureProviderListed(m *scheduleModal, provider string) {
	if provider == "" {
		return
	}
	for _, p := range m.Providers {
		if p == provider {
			return
		}
	}
	m.Providers = append(m.Providers, provider)
	sort.Strings(m.Providers)
}

// handleScheduleSave creates a new schedule (form.ID empty) or updates an
// existing one in place (form.ID set) — one endpoint for both, since the
// add and edit modals are the same form. A fetch from the modal gets a 204
// or a 422 with the message to show inline; a plain form post (no JS) on a
// validation failure re-renders the schedules page with the modal open and
// everything the operator typed still in place.
func handleScheduleSave(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		form := addScheduleForm{
			ID:            r.FormValue("id"),
			Provider:      r.FormValue("provider"),
			IntervalValue: r.FormValue("interval_value"),
			IntervalUnit:  r.FormValue("interval_unit"),
			ReindexAfter:  r.FormValue("reindex_after") == "on",
		}

		value, convErr := strconv.Atoi(form.IntervalValue)
		var interval time.Duration
		switch form.IntervalUnit {
		case "hours":
			interval = time.Duration(value) * time.Hour
		case "days":
			interval = time.Duration(value) * 24 * time.Hour
		default:
			interval = time.Duration(value) * time.Minute
		}

		var errMsg string
		switch {
		case form.Provider == "":
			errMsg = "Select a provider."
		case convErr != nil || value <= 0:
			errMsg = "Enter a valid interval."
		case interval < minScheduleInterval:
			errMsg = "Interval must be at least 2 minutes."
		}
		if errMsg == "" {
			var err error
			if form.ID == "" {
				err = app.schedules.Add(form.Provider, interval, form.ReindexAfter)
			} else {
				err = app.schedules.Update(form.ID, form.Provider, interval, form.ReindexAfter)
			}
			if err != nil {
				errMsg = err.Error()
			}
		}

		if errMsg != "" {
			if isFetch(r) {
				actionError(w, http.StatusUnprocessableEntity, errMsg)
				return
			}
			data := buildSchedulesPageData(app, r)
			data.ScheduleModal.Form = form
			data.ScheduleModal.Error = errMsg
			data.ScheduleModal.Open = true
			ensureProviderListed(&data.ScheduleModal, form.Provider)
			app.tmpl.Render(w, "schedules", data)
			return
		}
		actionDone(w, r, "/schedules")
	}
}

// handleScheduleDelete removes a schedule. The confirmation prompt lives
// client-side (a plain confirm() on the delete form, no custom modal
// needed for something this reversible-by-re-adding).
func handleScheduleDelete(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		_ = app.schedules.Delete(r.FormValue("id"))
		actionDone(w, r, "/schedules")
	}
}

func handleScheduleToggle(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		id := r.FormValue("id")
		_ = app.schedules.Toggle(id)
		http.Redirect(w, r, "/schedules", http.StatusSeeOther)
	}
}
