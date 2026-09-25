package main

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const catalogPageSize = 50

// ProviderSummary aggregates the CSV's per-company rows into one row per
// provider for the catalog table, joined (by attachRunState, for the rows
// on the current page only) with the schedule store and the activity log.
type ProviderSummary struct {
	Provider     string
	Kind         string
	CompanyCount int
	AddedCount   int

	HasSchedule     bool
	ScheduleID      string
	ScheduleEnabled bool
	ScheduleTimes   []int // its runs, minutes after 00:00 UTC
	NextRun         time.Time

	LastRunAt      time.Time
	LastRunStatus  string // the run's Outcome status (success / partial / failed)
	LastRunSummary string // the Outcome's one-line result

	// RecentCrawl ("4 minutes ago") is set when the provider's last good
	// crawl ended inside its window — see recentCrawlWindow — so the Crawl
	// item asks before crawling it again.
	RecentCrawl string

	// Crawling is true from the instant a crawl of this provider is claimed
	// (Runner.Crawling) — before its first Activity row exists — so the row
	// shows it the moment the click lands.
	Crawling bool
}

// buildCatalog groups rows by provider, after filtering rows whose provider
// or company doesn't match query — a match on company name surfaces the
// whole provider, since the table shows providers not raw rows.
// addedCounts is the per-provider "how many rows are actually added" figure
// — from Postgres when reachable, the CSV's frozen column otherwise (see
// resolveAddedCounts) — since the CSV's own row-level `added` flags are no
// longer written and can't be trusted as current.
func buildCatalog(rows []BoardRow, kindFilter, query string, addedCounts map[string]int) []ProviderSummary {
	terms := searchTerms(query)

	type acc struct {
		kind    string
		count   int
		matched bool
	}
	byProvider := map[string]*acc{}
	var order []string
	boards := boardCounts(rows)

	for _, row := range rows {
		a, ok := byProvider[row.Provider]
		if !ok {
			a = &acc{kind: kindOf(row.Provider)}
			byProvider[row.Provider] = a
			order = append(order, row.Provider)
		}
		a.count = boards[row.Provider]
		if rowMatchesSearch(row, terms) {
			a.matched = true
		}
	}

	sort.Strings(order)

	var out []ProviderSummary
	for _, provider := range order {
		a := byProvider[provider]
		if !a.matched {
			continue
		}
		if kindFilter != "" && a.kind != kindFilter {
			continue
		}
		added := addedCounts[provider]
		if added > a.count {
			// The live DB can show more added boards than this provider has
			// CANDIDATE rows in boardly-api's own CSV (added independently,
			// e.g. by a curator editing the database directly) — cap the
			// display at the candidate count rather than an X/Y figure with
			// X > Y, which would read as a bug.
			added = a.count
		}
		out = append(out, ProviderSummary{
			Provider:     provider,
			Kind:         a.kind,
			CompanyCount: a.count,
			AddedCount:   added,
		})
	}
	return out
}

// searchTerms splits a query into lowercase whitespace-separated words. A
// single literal-substring match (the old behavior) missed "green house"
// against a stored "Greenhouse" — reordered or differently-spaced words are
// a normal way to type a remembered name, not a typo.
func searchTerms(query string) []string {
	return strings.Fields(strings.ToLower(query))
}

// rowMatchesSearch requires every search term to appear SOMEWHERE across
// the row's provider and company (AND, not a single combined-string OR) —
// "google recruit" matches company "Recruit at Google" as readily as
// "recruit google", which a single substring check never would.
func rowMatchesSearch(row BoardRow, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	haystack := strings.ToLower(row.Provider) + " " + strings.ToLower(row.Company)
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// newProviderForm is a "+ New provider" request's fields; addProviderRow
// returns it as applied (the kind's field rules filled in).
type newProviderForm struct {
	Kind     string
	Provider string
	Board    string
	Company  string
	CrawlNow bool
}

type kindTab struct {
	Value string
	Label string
}

var kindTabsList = []kindTab{
	{"", "All"},
	{KindATS, "ATS platforms"},
	{KindAggregator, "Aggregators"},
	{KindCareerSite, "Career sites"},
	{KindUnclassified, "Unclassified"},
}

// catalogPage is one page of the Catalog, as the API serves it.
type catalogPage struct {
	Providers  []ProviderSummary
	KindFilter string
	AddedOnly  bool // the default view (added providers only); ?show=all widens it to the whole catalog
	Query      string
	Page       int
	TotalPages int
	Total      int
	DBError    string // set when added status fell back to the CSV's stale column
	// AddedProviders are the providers with at least one added board — the
	// ones a schedule can be made for.
	AddedProviders []string
}

// buildCatalogPage assembles one page of the Catalog from the request's
// query params (?show=all, ?kind=, ?q=, ?page=) and the current CSV state.
func buildCatalogPage(app *App, r *http.Request) catalogPage {
	q := r.URL.Query()
	kindFilter := q.Get("kind")
	// Added is the default; only ?show=all widens to the whole catalog (so an
	// old ?show=added link still lands on Added).
	addedOnly := q.Get("show") != "all"
	query := q.Get("q")
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	addedCounts, dbError := resolveAddedCounts(r.Context(), app)
	all := buildCatalog(app.csv.Rows(), kindFilter, query, addedCounts)
	if addedOnly {
		all = keepAdded(all)
	}
	total := len(all)
	totalPages := (total + catalogPageSize - 1) / catalogPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * catalogPageSize
	end := start + catalogPageSize
	if start > len(all) {
		start = len(all)
	}
	if end > len(all) {
		end = len(all)
	}

	pageRows := all[start:end]
	attachRunState(app, pageRows)

	return catalogPage{
		Providers:      pageRows,
		KindFilter:     kindFilter,
		AddedOnly:      addedOnly,
		Query:          query,
		Page:           page,
		TotalPages:     totalPages,
		Total:          total,
		DBError:        dbError,
		AddedProviders: addedProviders(addedCounts),
	}
}

// addedProviders are the providers with at least one added board, sorted.
func addedProviders(addedCounts map[string]int) []string {
	out := []string{}
	for p, n := range addedCounts {
		if n > 0 {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func keepAdded(all []ProviderSummary) []ProviderSummary {
	var out []ProviderSummary
	for _, p := range all {
		if p.AddedCount > 0 {
			out = append(out, p)
		}
	}
	return out
}

// attachRunState fills each row's schedule and last-run columns in place.
// One schedule per provider is the common case this tool expects; if more
// than one exists, the last one in store order wins — good enough for a
// summary column, not a scheduling authority (Schedules is that).
func attachRunState(app *App, rows []ProviderSummary) {
	scheduleByProvider := map[string]Schedule{}
	for _, s := range app.schedules.List() {
		scheduleByProvider[s.Provider] = s
	}

	// Most recent run per provider — List() is already newest-first, so the
	// first match for a provider is its most recent run of any kind.
	// And the most recent crawl that got something done, for RecentCrawl.
	lastRunByProvider := map[string]*Run{}
	lastGoodCrawl := map[string]*Run{}
	for _, run := range app.activity.List() {
		if run.Provider == "" {
			continue
		}
		if _, ok := lastRunByProvider[run.Provider]; !ok {
			lastRunByProvider[run.Provider] = run
		}
		if _, ok := lastGoodCrawl[run.Provider]; !ok && run.Action == "ingest" {
			if o := run.Outcome().Status; o == OutcomeSuccess || o == OutcomePartial {
				lastGoodCrawl[run.Provider] = run
			}
		}
	}
	now := time.Now()

	for i := range rows {
		d := &rows[i]
		d.Crawling = app.runner.Crawling(d.Provider)
		if s, ok := scheduleByProvider[d.Provider]; ok {
			d.HasSchedule = true
			d.ScheduleID = s.ID
			d.ScheduleEnabled = s.Enabled
			d.ScheduleTimes = s.Times
			d.NextRun = s.NextRun()
		}
		if run, ok := lastRunByProvider[d.Provider]; ok {
			o := run.Outcome()
			d.LastRunAt = run.StartedAt
			d.LastRunStatus, d.LastRunSummary = o.Status, o.Summary
		}
		if run, ok := lastGoodCrawl[d.Provider]; ok {
			if since := now.Sub(run.FinishedAt); since < recentCrawlWindow {
				d.RecentCrawl = agoText(since)
			}
		}
	}
}

// recentCrawlWindow is how recent a crawl must be for a manual Crawl to ask
// first.
const recentCrawlWindow = 30 * time.Minute

// agoText renders a short duration as "just now" / "4 minutes ago" /
// "2 hours ago".
func agoText(d time.Duration) string {
	switch m := int(d.Minutes()); {
	case m < 1:
		return "just now"
	case m == 1:
		return "1 minute ago"
	case m < 60:
		return fmt.Sprintf("%d minutes ago", m)
	case m < 120:
		return "1 hour ago"
	default:
		return fmt.Sprintf("%d hours ago", m/60)
	}
}

// startRemoveProvider retires provider's live boards, drops its schedule
// and, once every board is retired, purges its activity (see
// Runner.StartRemoveProvider). The board list comes from the database, so
// it is refused when that is unreachable rather than retiring only what the
// CSV knows. A refusal returns its HTTP status and message; 0 means it
// started.
func startRemoveProvider(app *App, r *http.Request, provider string) (int, string) {
	if app.db == nil {
		return http.StatusServiceUnavailable, "The database is not configured, so the boards to retire are unknown."
	}
	boards, err := app.db.LiveBoards(r.Context(), provider)
	if err != nil {
		return http.StatusServiceUnavailable, "Could not read " + provider + "'s boards from the database: " + err.Error()
	}
	deleteSchedules := func() int {
		n, err := app.schedules.DeleteByProvider(provider)
		if err != nil {
			log.Printf("remove %s: delete schedules: %v", provider, err)
		}
		return n
	}
	purge := func() {
		ids := app.activity.PurgeProvider(provider)
		app.explainer.Forget(ids)
		log.Printf("remove %s: purged %d activity run(s)", provider, len(ids))
	}
	if !app.runner.StartRemoveProvider(provider, boards, deleteSchedules, purge) {
		return http.StatusConflict, provider + " is being crawled right now — remove it once that finishes."
	}
	return 0, ""
}

// addProviderRow is "+ New provider": it validates the request (applying
// the kind's field rules), appends the row to the CSV (no DB call) and,
// when asked, starts add + crawl for it. It returns the fields as applied
// and, on a validation or save failure, the message to show.
func addProviderRow(app *App, form newProviderForm) (newProviderForm, string) {
	knownProviders := app.csv.DistinctProviders()
	known := false
	for _, p := range knownProviders {
		if p == form.Provider {
			known = true
			break
		}
	}
	if known {
		form.Kind = kindOf(form.Provider)
	}

	// Kind-driven field rules, whatever the caller sent (a UI hides these
	// fields, but the rule lives here): an Aggregator is a single feed,
	// not a per-company entry, so it never carries a board and its company
	// name is derived rather than typed; a Career site is always boardless.
	switch form.Kind {
	case KindAggregator:
		form.Board = ""
		if form.Company == "" {
			form.Company = displayName(form.Provider)
		}
		form.CrawlNow = true // no separate "add without crawling" step for a single feed
	case KindCareerSite:
		form.Board = ""
	}

	var errMsg string
	switch {
	case form.Provider == "":
		errMsg = "Select a provider."
	case !known:
		errMsg = "Provider must be one already known to freehire."
	case form.Kind == KindATS && form.Board == "":
		errMsg = "Board is required for ATS platforms."
	case form.Company == "":
		errMsg = "Company is required."
	case app.csv.HasBoard(form.Provider, form.Board):
		errMsg = form.Provider + " is already in the catalog" + boardSuffix(form.Board) + " — use Crawl on its row instead."
	}
	if errMsg != "" {
		return form, errMsg
	}
	if err := app.csv.AppendRow(form.Provider, form.Board, form.Company, false); err != nil {
		return form, "Could not save the row: " + err.Error()
	}
	if form.CrawlNow {
		// Already crawling (a click on its row a moment ago): that crawl
		// covers the row just added, so there is nothing to start.
		app.runner.StartCrawl(form.Provider, true, false, nil)
	}
	return form, ""
}

// boardSuffix names the board in the duplicate-row message, when there is
// one to name (an Aggregator or Career site is boardless).
func boardSuffix(board string) string {
	if board == "" {
		return ""
	}
	return " with board " + board
}
