package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// The JSON API — Board Console's only interface (/api/v1). A separate UI
// drives it; there are no pages here. It has no login of its own: a caller
// is trusted by an optional shared key and, from a browser, by an allowed
// origin (apiMiddleware) — so set the key anywhere the port is reachable.

// apiMiddleware guards every /api/v1 route:
//   - BOARD_CONSOLE_API_KEY set: a request needs "Authorization: Bearer
//     <key>" (compared in constant time). Unset, the API is open — for local
//     development; never expose it publicly that way.
//   - BOARD_CONSOLE_CORS_ORIGINS (comma list, default the Vite dev server,
//     http://localhost:5173): a browser on one of them may call it, and its
//     preflight OPTIONS is answered here. List every place a UI runs from —
//     e.g. a laptop's dev server and the deployed app — so both can reach the
//     same API.
func apiMiddleware(next http.HandlerFunc) http.HandlerFunc {
	key := os.Getenv("BOARD_CONSOLE_API_KEY")
	origins := map[string]bool{}
	for _, o := range strings.Split(envOr("BOARD_CONSOLE_CORS_ORIGINS", "http://localhost:5173"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			origins[o] = true
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); origins[o] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if key != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				actionError(w, http.StatusUnauthorized, "missing or wrong API key")
				return
			}
		}
		next(w, r)
	}
}

// registerAPI mounts the API on mux.
func registerAPI(mux *http.ServeMux, app *App) {
	api := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, apiMiddleware(h)) }
	api("OPTIONS /api/v1/{path...}", func(http.ResponseWriter, *http.Request) {}) // preflight, answered by the middleware
	api("GET /api/v1/meta", handleAPIMeta(app))
	api("GET /api/v1/catalog", handleAPICatalog(app))
	api("POST /api/v1/providers", handleAPINewProvider(app))
	api("POST /api/v1/providers/{provider}/crawl", handleAPICrawl(app))
	api("POST /api/v1/providers/{provider}/remove", handleAPIRemoveProvider(app))
	api("POST /api/v1/reindex", handleAPIReindex(app))
	api("GET /api/v1/schedules/load", handleScheduleLoad(app))
	api("PUT /api/v1/schedules", handleAPISaveSchedule(app))
	api("DELETE /api/v1/schedules/{id}", handleAPIDeleteSchedule(app))
	api("GET /api/v1/system/stats", handleSystemStats(app))
	api("POST /api/v1/system/build-cache/prune", handleBuildCachePrune(app))
	registerPageAPI(api, app)
}

// actionError answers a refused request with {"error": msg} — a 422 for a
// validation failure, a 409 for a conflict, and so on.
func actionError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON reads a request's JSON body into v, answering 400 itself when
// it cannot.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		actionError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// --- meta ---

type apiKindTab struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// handleAPIMeta is what a UI needs once, up front: the catalogue's scope,
// the build, the schedule capacity, and the provider kinds the Catalog's
// tabs and "+ New provider" form use.
func handleAPIMeta(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kinds := map[string]string{}
		for _, p := range app.csv.DistinctProviders() {
			kinds[p] = kindOf(p)
		}
		var tabs []apiKindTab
		for _, t := range kindTabsList {
			tabs = append(tabs, apiKindTab{t.Value, t.Label})
		}
		var built *time.Time
		if t := buildDate(); !t.IsZero() {
			built = &t
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"techOnly":         catalogueTechOnly(),
			"buildId":          buildID,
			"buildDate":        built,
			"scheduleCapacity": scheduleCapacity(),
			"kindTabs":         tabs,
			"newProviderKinds": []string{KindATS, KindAggregator, KindCareerSite},
			"providerKinds":    kinds,
		})
	}
}

// --- catalog ---

type apiProviderSchedule struct {
	ID      string     `json:"id"`
	Enabled bool       `json:"enabled"`
	Times   []int      `json:"times"`   // minutes after 00:00 UTC
	NextRun *time.Time `json:"nextRun"` // null while paused or due now
	DueNow  bool       `json:"dueNow"`
}

type apiLastRun struct {
	At      time.Time `json:"at"`
	Status  string    `json:"status"` // success / partial / failed / running / queued
	Summary string    `json:"summary"`
}

type apiProvider struct {
	Provider     string               `json:"provider"`
	Kind         string               `json:"kind"`
	CompanyCount int                  `json:"companyCount"`
	AddedCount   int                  `json:"addedCount"`
	Schedule     *apiProviderSchedule `json:"schedule"`
	LastRun      *apiLastRun          `json:"lastRun"`
	// RecentCrawl ("4 minutes ago") is set when a manual Crawl should ask
	// first: the provider's last good crawl ended inside recentCrawlWindow.
	RecentCrawl string `json:"recentCrawl"`
	Crawling    bool   `json:"crawling"`
}

func toAPIProvider(p ProviderSummary) apiProvider {
	out := apiProvider{
		Provider: p.Provider, Kind: p.Kind, CompanyCount: p.CompanyCount, AddedCount: p.AddedCount,
		RecentCrawl: p.RecentCrawl, Crawling: p.Crawling,
	}
	if p.HasSchedule {
		s := &apiProviderSchedule{ID: p.ScheduleID, Enabled: p.ScheduleEnabled, Times: p.ScheduleTimes}
		if p.NextRun.IsZero() {
			s.DueNow = p.ScheduleEnabled
		} else {
			next := p.NextRun
			s.NextRun = &next
		}
		out.Schedule = s
	}
	if !p.LastRunAt.IsZero() {
		out.LastRun = &apiLastRun{At: p.LastRunAt, Status: p.LastRunStatus, Summary: p.LastRunSummary}
	}
	return out
}

// handleAPICatalog is the Catalog: ?show=all (else added only), ?kind=,
// ?q=, ?page= — one page of providers with their schedule and last run
// (buildCatalogPage).
func handleAPICatalog(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := buildCatalogPage(app, r)
		providers := make([]apiProvider, 0, len(d.Providers))
		for _, p := range d.Providers {
			providers = append(providers, toAPIProvider(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"providers":      providers,
			"page":           d.Page,
			"totalPages":     d.TotalPages,
			"total":          d.Total,
			"addedOnly":      d.AddedOnly,
			"kind":           d.KindFilter,
			"q":              d.Query,
			"dbError":        d.DBError,
			"addedProviders": d.AddedProviders,
		})
	}
}

// --- providers ---

// handleAPINewProvider is "+ New provider": 201 with the row as applied,
// or 422 with the message to show.
func handleAPINewProvider(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Provider string `json:"provider"`
			Board    string `json:"board"`
			Company  string `json:"company"`
			CrawlNow bool   `json:"crawlNow"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		form, errMsg := addProviderRow(app, newProviderForm{Provider: in.Provider, Board: in.Board, Company: in.Company, CrawlNow: in.CrawlNow})
		if errMsg != "" {
			actionError(w, http.StatusUnprocessableEntity, errMsg)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"provider": form.Provider, "board": form.Board, "company": form.Company, "kind": form.Kind, "crawlNow": form.CrawlNow,
		})
	}
}

// handleAPICrawl is Crawl (Add + Crawl for a provider not fully added), or
// with {"refetchAll": true} a Full re-crawl: 202, or 409 while the provider
// is already being crawled. An empty body is a plain crawl.
func handleAPICrawl(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.PathValue("provider")
		var in struct {
			RefetchAll bool `json:"refetchAll"`
		}
		if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
			return
		}
		if !app.runner.StartCrawl(provider, true, in.RefetchAll, nil) {
			actionError(w, http.StatusConflict, provider+" is already being crawled — see Activity.")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"provider": provider, "refetchAll": in.RefetchAll})
	}
}

// handleAPIRemoveProvider is "Remove provider": 202, or 409/503 with why not.
func handleAPIRemoveProvider(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.PathValue("provider")
		if status, msg := startRemoveProvider(app, r, provider); status != 0 {
			actionError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"provider": provider})
	}
}

// handleAPIReindex is "Reindex now", coalesced with any reindex already
// running (see Runner.queueReindex).
func handleAPIReindex(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job := app.activity.NewJob()
		app.runner.queueReindex(job)
		writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
	}
}

// --- schedules ---

// handleAPISaveSchedule saves a schedule: {id?, provider,
// times} with times in minutes after 00:00 UTC. A provider has one
// schedule, so saving for one that already has a schedule updates it.
func handleAPISaveSchedule(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Times    []int  `json:"times"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		if err := saveSchedule(app, in.ID, in.Provider, in.Times); err != nil {
			actionError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		sch, _ := app.schedules.ByProvider(in.Provider)
		writeJSON(w, http.StatusOK, map[string]any{"id": sch.ID, "provider": sch.Provider, "times": sch.Times, "enabled": sch.Enabled})
	}
}

func handleAPIDeleteSchedule(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := app.schedules.Delete(r.PathValue("id")); err != nil {
			actionError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
