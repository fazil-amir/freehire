package main

import (
	"encoding/json"
	"fmt"
	"html/template"
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
	ScheduleID      string // for the kebab menu's edit/delete items
	ScheduleEnabled bool
	Interval        string
	IntervalValue   string // Interval split for the schedule modal's fields
	IntervalUnit    string
	NextRun         time.Time
	ReindexAfter    bool

	LastRunAt      time.Time
	LastRunStatus  string // the run's Outcome status (success / partial / failed)
	LastRunSummary string // the Outcome's one-line result

	// Crawling is true from the instant a crawl of this provider is claimed
	// (Runner.Crawling) — before its first Activity row exists — so the row
	// shows it the moment the click lands.
	Crawling bool
}

func (p ProviderSummary) FullyAdded() bool { return p.AddedCount == p.CompanyCount }
func (p ProviderSummary) PartiallyAdded() bool {
	return p.AddedCount > 0 && p.AddedCount < p.CompanyCount
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
			// CANDIDATE rows in board-console's own CSV (added independently,
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

// newProviderForm is the "+ New provider" modal's field values — round-
// tripped back into the template on a validation failure so nothing the
// operator typed is lost.
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

type catalogPageData struct {
	Active     string
	Providers  []ProviderSummary
	KindFilter string
	AddedOnly  bool // the default view (added providers only); ?show=all widens it to the whole catalog
	Query      string
	Page       int
	TotalPages int
	Total      int
	KindTabs   []kindTab
	DBError    string // set when added status fell back to the CSV's stale column

	// "+ New provider" modal state.
	NewProviderKinds     []string
	ProviderKindsJSON    template.JS
	NewProviderForm      newProviderForm
	NewProviderError     string
	OpenNewProviderModal bool

	ScheduleModal scheduleModal
}

// buildCatalogPageData assembles everything the catalog template needs from
// the request's query params and the current CSV state. Both handleCatalog
// and handleNewProvider's error path render from this, so a failed "+ New
// provider" submit re-renders the full page — table, filters, pagination —
// exactly as it was, with only the modal's own state added on top.
func buildCatalogPageData(app *App, r *http.Request) catalogPageData {
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

	// The schedule modal's dropdown offers the added providers; a kebab
	// item for any other opens it with that provider added to the list.
	var addedProviders []string
	for p, n := range addedCounts {
		if n > 0 {
			addedProviders = append(addedProviders, p)
		}
	}
	sort.Strings(addedProviders)

	knownProviders := app.csv.DistinctProviders()
	return catalogPageData{
		Active:            "catalog",
		Providers:         pageRows,
		KindFilter:        kindFilter,
		AddedOnly:         addedOnly,
		Query:             query,
		Page:              page,
		TotalPages:        totalPages,
		Total:             total,
		KindTabs:          kindTabsList,
		DBError:           dbError,
		NewProviderKinds:  []string{KindATS, KindAggregator, KindCareerSite},
		ProviderKindsJSON: providerKindsJSON(knownProviders),
		ScheduleModal:     newScheduleModal(addedProviders),
	}
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
	lastRunByProvider := map[string]*Run{}
	for _, run := range app.activity.List() {
		if run.Provider == "" {
			continue
		}
		if _, ok := lastRunByProvider[run.Provider]; !ok {
			lastRunByProvider[run.Provider] = run
		}
	}

	for i := range rows {
		d := &rows[i]
		d.Crawling = app.runner.Crawling(d.Provider)
		if s, ok := scheduleByProvider[d.Provider]; ok {
			d.HasSchedule = true
			d.ScheduleID = s.ID
			d.ScheduleEnabled = s.Enabled
			d.Interval = formatInterval(s.Interval())
			d.IntervalValue, d.IntervalUnit = splitInterval(s.IntervalSecs)
			d.NextRun = s.NextRun()
			d.ReindexAfter = s.ReindexAfter
		}
		if run, ok := lastRunByProvider[d.Provider]; ok {
			o := run.Outcome()
			d.LastRunAt = run.StartedAt
			d.LastRunStatus, d.LastRunSummary = o.Status, o.Summary
		}
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

func handleCatalog(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app.tmpl.Render(w, "catalog", buildCatalogPageData(app, r))
	}
}

// handleCatalogResults serves the real-time search fragment: app.js fetches
// this on every keystroke (debounced) and swaps #catalog-results in place,
// so results update as you type without a full page reload or a Search
// button. It shares buildCatalogPageData with the full-page handler, so a
// search result is byte-identical whether it arrived via fragment or via a
// full navigation (e.g. Enter, a bookmarked URL, or JS being unavailable).
func handleCatalogResults(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app.tmpl.Render(w, "catalog-results", buildCatalogPageData(app, r))
	}
}

// handleCrawl is the single-row action: "Crawl" for an already-added
// provider, "Add + Crawl" for one that isn't. It kicks the run off in the
// background (a Greenhouse-sized crawl can run tens of minutes) and leaves
// the operator where they clicked — Activity is there if they want to watch.
func handleCrawl(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		provider := r.FormValue("provider")
		if provider == "" {
			http.Error(w, "provider required", http.StatusBadRequest)
			return
		}
		refetchAll := r.FormValue("refetch") == "1"
		if !app.runner.StartCrawl(provider, true, refetchAll, nil) {
			if isFetch(r) {
				actionError(w, http.StatusConflict, provider+" is already being crawled — see Activity.")
				return
			}
		}
		actionDone(w, r, "/")
	}
}

// handleReindexNow is the "Reindex now" kebab-menu action: triggers just
// the reindex step, coalesced the same way a Crawl's trailing reindex is —
// if one's already running, this request rides along on one extra run
// right after it, rather than starting a redundant second one.
func handleReindexNow(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app.runner.queueReindex()
		actionDone(w, r, "/")
	}
}

// handleCompanyRefresh is the "Recount companies" button: company job
// counts and facets, then company search. Refused with a 409 while one is
// already running.
func handleCompanyRefresh(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.runner.StartCompanyRefresh() {
			if isFetch(r) {
				actionError(w, http.StatusConflict, "A company recount is already running.")
				return
			}
		}
		actionDone(w, r, "/activity")
	}
}

// handleBulkCrawl is "Add + Crawl Selected": one batch, one reindex at the
// end, covering every checked provider.
func handleBulkCrawl(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		providers := r.Form["provider"]
		if len(providers) == 0 {
			actionDone(w, r, "/")
			return
		}
		go func() {
			_ = app.runner.RunBatch(providers, true)
		}()
		actionDone(w, r, "/")
	}
}

// handleNewProvider is the "+ New provider" form: appends a row directly to
// the CSV (no DB call), optionally kicking off add+crawl for it. On a
// validation failure it re-renders the catalog page with the modal open and
// everything the operator typed still in place, rather than redirecting and
// losing it.
func handleNewProvider(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		form := newProviderForm{
			Provider: r.FormValue("provider"),
			Board:    r.FormValue("board"),
			Company:  r.FormValue("company"),
			CrawlNow: r.FormValue("crawl_now") == "on",
		}

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

		// Kind-driven field rules, applied server-side as a fallback in case
		// the client (whose JS hides/auto-fills these fields — see
		// catalog.html and app.js) didn't run: an Aggregator is a single
		// feed, not a per-company entry, so it never carries a board and
		// its company name is derived rather than typed; a Career site is
		// always boardless.
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
			data := buildCatalogPageData(app, r)
			data.NewProviderForm = form
			data.NewProviderError = errMsg
			data.OpenNewProviderModal = true
			app.tmpl.Render(w, "catalog", data)
			return
		}

		if err := app.csv.AppendRow(form.Provider, form.Board, form.Company, false); err != nil {
			http.Error(w, "failed to save row: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if form.CrawlNow {
			// Already crawling (a click on its row a moment ago): that crawl
			// covers the row just added, so there is nothing to start.
			app.runner.StartCrawl(form.Provider, true, false, nil)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// providerKindsJSON builds the provider -> kind map the "+ New provider"
// modal's JS uses to filter the provider input as the kind selector
// changes, without a page reload.
func providerKindsJSON(providers []string) template.JS {
	m := make(map[string]string, len(providers))
	for _, p := range providers {
		m[p] = kindOf(p)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return template.JS(b)
}

// boardSuffix names the board in the duplicate-row message, when there is
// one to name (an Aggregator or Career site is boardless).
func boardSuffix(board string) string {
	if board == "" {
		return ""
	}
	return " with board " + board
}
