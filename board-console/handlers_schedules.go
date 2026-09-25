package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

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
	// Slots is each of the day's runs with how its most recent run went, as
	// slotRun JSON (see slotRuns).
	Slots string
}

// slotRun is one planned run on a Schedules row: its time of day and how
// its most recent occurrence (within the last 24 hours) went.
type slotRun struct {
	Min     int       `json:"m"`             // minutes after 00:00 UTC
	Status  string    `json:"s"`             // success / partial / failed / running, "pending" while its window is open, "none" when nothing ran
	At      time.Time `json:"at,omitzero"`   // when that crawl started
	Summary string    `json:"sum,omitempty"` // the crawl's one-line result
	Run     int       `json:"run,omitempty"` // that job's main run, whose log a click on the square opens
}

// servedBy is the job that took care of the slot at occ, until the next
// one: the first to start from skipWindow before it (that close, it covers
// the slot — see Schedule.Due), or nil.
func servedBy(jobs []*Job, occ, until time.Time) *Job {
	var match *Job
	for _, j := range jobs {
		if !j.StartedAt.Before(occ.Add(-skipWindow)) && j.StartedAt.Before(until) && (match == nil || j.StartedAt.Before(match.StartedAt)) {
			match = j
		}
	}
	return match
}

// fill copies a job's result into the slot.
func (s *slotRun) fill(j *Job) {
	s.Status, s.At, s.Summary, s.Run = j.Status, j.StartedAt, j.Summary, j.mainStep().ID
}

// slotRuns matches each of a schedule's times to the crawl that served its
// most recent occurrence: the first crawl of the provider — scheduled or
// not — that started from skipWindow before it (a crawl that close covers
// it, see Schedule.Due) until the schedule's next time. crawls are the
// provider's crawl jobs, in any order.
func slotRuns(s Schedule, crawls []*Job, now time.Time) []slotRun {
	out := make([]slotRun, 0, len(s.Times))
	for i, m := range s.Times {
		occ := slotStart(now, 0).Add(time.Duration(m) * time.Minute)
		if occ.After(now) {
			occ = occ.Add(-24 * time.Hour)
		}
		gap := (s.Times[(i+1)%len(s.Times)] - m + 24*60) % (24 * 60)
		if gap == 0 {
			gap = 24 * 60
		}
		until := occ.Add(time.Duration(gap) * time.Minute)

		slot := slotRun{Min: m, Status: "none"}
		switch match := servedBy(crawls, occ, until); {
		case match != nil:
			slot.fill(match)
		case until.After(now):
			slot.Status = "pending" // still inside its window: due, queued, or not reached yet
		}
		out = append(out, slot)
	}
	return out
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

// handleScheduleLoad is the day's load — crawls starting per 15-minute slot
// — and the capacity: what a schedule editor checks a new time against.
func handleScheduleLoad(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capacity": scheduleCapacity(),
			"load":     loadProfile(app.schedules.List()),
		})
	}
}

// schedulesPage is the Schedules plan, as the API serves it: the scheduled
// providers, the added ones without a schedule, and the system jobs.
type schedulesPage struct {
	Schedules []scheduleRow
	DBError   string // set when the provider list fell back to the CSV's stale column
	Capacity  int
	// Unscheduled is the added providers without a schedule, listed between
	// the scheduled ones and System.
	Unscheduled []unscheduledRow
	// System is Board Console's own daily jobs, listed after the providers.
	System []systemRow
	// AddedProviders are the providers a schedule can be made for.
	AddedProviders []string
}

func buildSchedulesPage(app *App, r *http.Request) schedulesPage {
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

	crawls := map[string][]*Job{}
	systemJobs := map[string][]*Job{} // newest first, like buildJobs
	for _, j := range buildJobs(app.activity.List()) {
		switch j.Kind {
		case "Crawl", "Full re-crawl":
			crawls[j.Provider] = append(crawls[j.Provider], j)
		case "Cleanup": // a Preview is a dry run and serves no slot
			systemJobs[sysCleanup] = append(systemJobs[sysCleanup], j)
		case "Recount companies":
			systemJobs[sysRecount] = append(systemJobs[sysRecount], j)
		}
	}
	now := time.Now().Round(0)

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
		if b, err := json.Marshal(slotRuns(s, crawls[s.Provider], now)); err == nil {
			row.Slots = string(b)
		}
		rows = append(rows, row)
	}

	addedCounts, dbError := resolveAddedCounts(r.Context(), app)
	knownProviders := addedProviders(addedCounts)

	scheduled := map[string]bool{}
	for _, sch := range schedules {
		scheduled[sch.Provider] = true
	}
	var unscheduled []unscheduledRow
	for _, p := range knownProviders {
		if scheduled[p] {
			continue
		}
		unscheduled = append(unscheduled, newUnscheduledRow(p, latest[p], app.runner.Crawling(p), crawls[p], now))
	}

	return schedulesPage{
		Schedules:      rows,
		DBError:        dbError,
		Capacity:       scheduleCapacity(),
		Unscheduled:    unscheduled,
		System:         buildSystemRows(app.system.List(), systemJobs, now),
		AddedProviders: knownProviders,
	}
}

// unscheduledRow is an added provider with no schedule, listed on the
// Schedules plan so it covers the whole fleet: its last crawl, and
// the crawls started by hand as squares on the day.
type unscheduledRow struct {
	Provider string
	Last     lastCrawlView
	Crawling bool
	// Runs is its crawls of the last 24 hours as slotRun JSON, each at the
	// minute it started; a UI shows the ones of its viewer's today.
	Runs string
}

func newUnscheduledRow(provider string, latest *Run, crawling bool, crawls []*Job, now time.Time) unscheduledRow {
	// The same last-crawl rule the scheduled rows use, with no schedule of
	// its own to add to it.
	row := unscheduledRow{Provider: provider, Crawling: crawling,
		Last: scheduleRow{LatestRun: latest, Crawling: crawling}.LastCrawl()}
	runs := []slotRun{}
	for _, j := range crawls {
		if j.StartedAt.Before(now.Add(-24 * time.Hour)) {
			continue
		}
		at := j.StartedAt.UTC()
		slot := slotRun{Min: at.Hour()*60 + at.Minute()}
		slot.fill(j)
		runs = append(runs, slot)
	}
	if b, err := json.Marshal(runs); err == nil {
		row.Runs = string(b)
	}
	return row
}

// systemRow is one system job on the Schedules plan.
type systemRow struct {
	SystemJob
	Info    systemJobInfo
	NextRun time.Time // zero while paused
	Running bool      // a run of it is in flight, however it was started
	Slot    string    // its one daily run as slotRun JSON, like a schedule's Slots
}

// LastBadge is the last run's status in the words the provider rows use.
func (r systemRow) LastBadge() string {
	if r.LastStatus == StatusFailed {
		return OutcomeFailed
	}
	return OutcomeSuccess
}

func buildSystemRows(settings []SystemJob, jobs map[string][]*Job, now time.Time) []systemRow {
	var rows []systemRow
	for _, j := range settings {
		info, _ := systemJobInfoOf(j.Key)
		row := systemRow{SystemJob: j, Info: info, NextRun: j.NextRun(now)}
		for _, job := range jobs[j.Key] {
			row.Running = row.Running || job.Running()
		}
		// Its square: the job that served its latest slot — the same rule
		// as a schedule's (slotRuns) — however it was started.
		occ := slotAt(now, j.Minute)
		slot := slotRun{Min: j.Minute, Status: "none"}
		switch match := servedBy(jobs[j.Key], occ, occ.Add(24*time.Hour)); {
		case match != nil:
			slot.fill(match)
		case j.Due(now):
			slot.Status = "pending"
		}
		if b, err := json.Marshal([]slotRun{slot}); err == nil {
			row.Slot = string(b)
		}
		rows = append(rows, row)
	}
	return rows
}

// saveSchedule creates a provider's schedule or updates the existing one —
// a provider has one schedule, so saving "Add" for a provider that already
// has one replaces that schedule's times. times are minutes after 00:00 UTC.
// Its error is the message to show the operator.
func saveSchedule(app *App, id, provider string, times []int) error {
	switch {
	case provider == "":
		return fmt.Errorf("Select a provider.")
	case len(times) == 0:
		return fmt.Errorf("Add at least one run time.")
	}
	if id == "" {
		if sch, ok := app.schedules.ByProvider(provider); ok {
			id = sch.ID
		}
	}
	if id == "" {
		return app.schedules.Add(provider, times)
	}
	return app.schedules.Update(id, provider, times)
}
