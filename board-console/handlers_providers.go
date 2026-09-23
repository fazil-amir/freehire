package main

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

// providerDetail joins the CSV (candidate rows), Postgres (added status —
// see resolveAddedCounts), the schedule store (is it on a recurring crawl,
// and when does that next fire), and the activity log (when did it last
// actually run, and how did that go).
type providerDetail struct {
	Provider     string
	Kind         string
	CompanyCount int
	AddedCount   int

	HasSchedule     bool
	ScheduleID      string // for the kebab menu's edit/delete links
	ScheduleEnabled bool
	Interval        string
	IntervalValue   string // Interval split for the schedule modal's fields
	IntervalUnit    string
	NextRun         time.Time
	ReindexAfter    bool

	LastRunAt     time.Time
	LastRunStatus RunStatus
}

func (p providerDetail) FullyAdded() bool { return p.AddedCount == p.CompanyCount }
func (p providerDetail) PartiallyAdded() bool {
	return p.AddedCount > 0 && p.AddedCount < p.CompanyCount
}

type providersPageData struct {
	Active        string
	Providers     []providerDetail
	DBError       string // set when added status fell back to the CSV's stale column
	ScheduleModal scheduleModal
}

func buildProvidersPageData(app *App, r *http.Request) providersPageData {
	type acc struct {
		kind  string
		count int
	}
	byProvider := map[string]*acc{}
	var order []string
	for _, row := range app.csv.Rows() {
		a, ok := byProvider[row.Provider]
		if !ok {
			a = &acc{kind: kindOf(row.Provider)}
			byProvider[row.Provider] = a
			order = append(order, row.Provider)
		}
		a.count++
	}
	sort.Strings(order)

	addedCounts, dbError := resolveAddedCounts(r.Context(), app)

	// One schedule per provider is the common case this tool expects; if
	// more than one exists, the last one in store order wins — good enough
	// for a details view, not a scheduling authority (Schedules is that).
	scheduleByProvider := map[string]Schedule{}
	for _, s := range app.schedules.List() {
		scheduleByProvider[s.Provider] = s
	}

	// Most recent run per provider — List() is already newest-first, so the
	// first match for a provider is its most recent run of any kind.
	lastRunByProvider := map[string]*Run{}
	for _, run := range app.activity.List() {
		if run.Provider == "" {
			continue
		}
		if _, ok := lastRunByProvider[run.Provider]; !ok {
			lastRunByProvider[run.Provider] = run
		}
	}

	var out []providerDetail
	for _, p := range order {
		a := byProvider[p]
		added := addedCounts[p]
		if added == 0 {
			continue // this page is "currently added", not the whole catalog — that's Catalog's job
		}
		if added > a.count {
			added = a.count // see buildCatalog's identical cap, same reasoning
		}
		d := providerDetail{Provider: p, Kind: a.kind, CompanyCount: a.count, AddedCount: added}
		if s, ok := scheduleByProvider[p]; ok {
			d.HasSchedule = true
			d.ScheduleID = s.ID
			d.ScheduleEnabled = s.Enabled
			d.Interval = formatInterval(s.Interval())
			d.IntervalValue, d.IntervalUnit = splitInterval(s.IntervalSecs)
			d.NextRun = s.NextRun()
			d.ReindexAfter = s.ReindexAfter
		}
		if run, ok := lastRunByProvider[p]; ok {
			d.LastRunAt = run.StartedAt
			d.LastRunStatus = run.Status
		}
		out = append(out, d)
	}
	// Every provider listed here is added, so every one may be scheduled.
	names := make([]string, len(out))
	for i, d := range out {
		names[i] = d.Provider
	}
	return providersPageData{
		Active:        "providers",
		Providers:     out,
		DBError:       dbError,
		ScheduleModal: newScheduleModal(names),
	}
}

// formatInterval renders a schedule interval the way an operator picked
// it — "15m"/"6h"/"2d" — rather than a raw second count.
func formatInterval(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
}

func handleProviders(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app.tmpl.Render(w, "providers", buildProvidersPageData(app, r))
	}
}
