package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// addScheduleForm is the add/edit schedule modal's field values — round-
// tripped back into the template on a validation failure so nothing the
// operator typed is lost. ID is empty for a new schedule, set for an edit.
type addScheduleForm struct {
	ID       string
	Provider string
	Times    []int // minutes after 00:00 UTC
}

type scheduleRow struct {
	Schedule
	NextRun time.Time
	// The provider's most recent crawl (add-boards or ingest) from the
	// activity log, scheduled or manual — the expandable output under the
	// row. Nil when the log holds none.
	LatestRun *Run
	// Crawling: a crawl of this provider is in flight right now, whoever
	// started it (this schedule, a Crawl click, a bulk run).
	Crawling bool
	// Queued: the schedule is due but SCHEDULE_CAPACITY crawls are already
	// running, so it waits for one to end.
	Queued bool
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
	Capacity  int   // a slot holding this many runs is booked
	Hours     []int // the time rows' choices: 0..23
	Minutes   []int // 0, 15, 30, 45
	Form      addScheduleForm
	Error     string
	Open      bool     // reopen on load: a no-JS validation failure round-trip
	Providers []string // the provider dropdown's options
}

func newScheduleModal(providers []string) scheduleModal {
	m := scheduleModal{Providers: providers, Capacity: scheduleCapacity()}
	for h := 0; h < 24; h++ {
		m.Hours = append(m.Hours, h)
	}
	for mm := 0; mm < 60; mm += slotMinutes {
		m.Minutes = append(m.Minutes, mm)
	}
	return m
}

// timeRow is one run-time row of the modal: Minutes after 00:00 UTC, split
// into the hour and minute it shows, or -1 for an empty row.
type timeRow struct {
	Modal        scheduleModal
	Minutes      int
	Hour, Minute int
}

func newTimeRow(m scheduleModal, minutes int) timeRow {
	if minutes < 0 {
		return timeRow{Modal: m, Minutes: -1, Hour: -1, Minute: -1}
	}
	return timeRow{Modal: m, Minutes: minutes, Hour: minutes / 60, Minute: minutes % 60}
}

// handleScheduleLoad is the schedule modal's booked-slot check: fetched
// every time the modal opens, so it always reflects the schedules as they
// are now, not as they were when the page was first rendered.
func handleScheduleLoad(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capacity": scheduleCapacity(),
			"load":     loadProfile(app.schedules.List()),
		})
	}
}

type schedulesPageData struct {
	Active        string
	Schedules     []scheduleRow
	DBError       string // set when the provider list fell back to the CSV's stale column
	ScheduleModal scheduleModal

	ExplainEnabled bool           // see activityPageData
	Explanations   map[int]string // cached answers by run ID

	Capacity int
	// Timeline is the 24-hour picture above the table, as JSON for app.js to
	// draw in the viewer's timezone (see timelineData).
	Timeline template.JS
}

// timelineData is what the Schedules timeline draws: each enabled
// schedule's run starts (minutes after 00:00 UTC — every run is one
// 15-minute block, see planner.go), the load per slot, and the daily cleanup —
// app.js shifts it all into the viewer's timezone.
type timelineData struct {
	Capacity   int              `json:"capacity"`
	Rows       []timelineRow    `json:"rows"`
	Load       [slotsPerDay]int `json:"load"`
	CleanupMin int              `json:"cleanupMin"`
}

type timelineRow struct {
	Provider string `json:"provider"`
	Starts   []int  `json:"starts"` // minutes after 00:00 UTC
}

func buildTimeline(schedules []Schedule, capacity int) template.JS {
	d := timelineData{Capacity: capacity, Rows: []timelineRow{}}
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		d.Rows = append(d.Rows, timelineRow{Provider: s.Provider, Starts: s.Times})
	}
	d.Load = loadProfile(schedules)
	// The cleanup runs at cleanupHour in the container's local time.
	c := time.Date(2000, 1, 1, cleanupHour, 0, 0, 0, time.Local).UTC()
	d.CleanupMin = c.Hour()*60 + c.Minute()
	b, err := json.Marshal(d)
	if err != nil {
		return "{}"
	}
	return template.JS(b)
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

	schedules := app.schedules.List()
	full := app.runner.CrawlCount() >= scheduleCapacity()
	var rows []scheduleRow
	for _, s := range schedules {
		row := scheduleRow{
			Schedule: s, NextRun: s.NextRun(),
			LatestRun: latest[s.Provider],
			Crawling:  app.runner.Crawling(s.Provider),
		}
		row.Queued = s.Enabled && row.NextRun.IsZero() && !row.Crawling && full
		rows = append(rows, row)
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

		Capacity: scheduleCapacity(),
		Timeline: buildTimeline(schedules, scheduleCapacity()),
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
			provider := r.URL.Query().Get("provider")
			data.ScheduleModal.Form.Provider = provider
			if sch, ok := app.schedules.ByProvider(provider); ok { // one per provider: edit it
				data.ScheduleModal.Form = addScheduleForm{ID: sch.ID, Provider: sch.Provider, Times: sch.Times}
			}
			ensureProviderListed(&data.ScheduleModal, provider)
		case "edit":
			if sch, ok := app.schedules.Get(r.URL.Query().Get("id")); ok {
				data.ScheduleModal.Open = true
				data.ScheduleModal.Form = addScheduleForm{ID: sch.ID, Provider: sch.Provider, Times: sch.Times}
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

// handleScheduleSave creates a provider's schedule or updates the existing
// one in place — one endpoint for both, since the add and edit modals are
// the same form, and a provider has one schedule: saving "Add" for a
// provider that already has one adds to nothing, it replaces that
// schedule's times. A fetch from the modal gets a 204 or a 422 with the
// message to show inline; a plain form post (no JS) on a validation
// failure re-renders the schedules page with the modal open and everything
// the operator chose still in place.
func handleScheduleSave(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		times, timesErr := formTimes(r)
		form := addScheduleForm{ID: r.FormValue("id"), Provider: r.FormValue("provider"), Times: times}

		var errMsg string
		switch {
		case form.Provider == "":
			errMsg = "Select a provider."
		case timesErr != nil:
			errMsg = timesErr.Error()
		case len(form.Times) == 0:
			errMsg = "Add at least one run time."
		}
		if errMsg == "" {
			id := form.ID
			if id == "" {
				if sch, ok := app.schedules.ByProvider(form.Provider); ok {
					id = sch.ID
				}
			}
			var err error
			if id == "" {
				err = app.schedules.Add(form.Provider, form.Times)
			} else {
				err = app.schedules.Update(id, form.Provider, form.Times)
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

// formTimes reads the modal's time rows — parallel "hour" and "minute"
// fields in the operator's timezone, tz_offset minutes east of UTC (set by
// the browser; absent, the rows are UTC) — as minutes after 00:00 UTC. A
// row with either half unset is an unfinished row and is ignored.
func formTimes(r *http.Request) ([]int, error) {
	hours, minutes := r.Form["hour"], r.Form["minute"]
	tz, _ := strconv.Atoi(r.FormValue("tz_offset"))
	var out []int
	for i := range hours {
		if i >= len(minutes) || hours[i] == "" || minutes[i] == "" {
			continue
		}
		h, errH := strconv.Atoi(hours[i])
		m, errM := strconv.Atoi(minutes[i])
		if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 || m%slotMinutes != 0 {
			return nil, fmt.Errorf("every run must be a time on the 15-minute grid")
		}
		out = append(out, ((h*60+m-tz)%(24*60)+24*60)%(24*60))
	}
	return out, nil
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
