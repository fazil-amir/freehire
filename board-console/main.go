// Command board-console is a standalone internal ops tool for managing
// which ATS/aggregator/career-site "boards" get added to freehire's catalog
// and crawled. It has no code-level relationship to freehire: it never
// imports freehire's internal packages. The only WRITE interaction with
// freehire is running its already-built worker binaries (bulk-add-boards,
// ingest, reindex) as local subprocesses — they are copied into this image
// at build time from the same build stage that produces the freehire app
// image, not invoked via docker exec against a separate container. The
// only DB interaction is a read-only Postgres connection (db.go) used
// solely to answer "is this already added" — board-console's own Go code
// never writes to that database.
package main

import (
	"log"
	"net/http"
	"os"
)

// App wires together every piece the HTTP handlers need.
type App struct {
	tmpl      *Templates
	csv       *CSVStore
	activity  *ActivityLog
	schedules *ScheduleStore
	runner    *Runner
	cleanup   *CleanupStore
	explainer *Explainer
	sessions  *SessionStore
	db        *DBStore // nil when DATABASE_URL is unset or the open failed — every reader falls back gracefully
}

func main() {
	dataDir := envOr("DATA_DIR", "/app/data")
	port := envOr("PORT", "8091")

	// The data directory is normally a bind mount (docker-compose.yml maps
	// ./board-console/data -> /app/data) that already exists, but create it
	// defensively — a missing directory here would otherwise surface as an
	// opaque "no such file or directory" from whichever store opens first.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", dataDir, err)
	}

	csvStore, err := NewCSVStore(dataDir + "/combined_boards.csv")
	if err != nil {
		log.Fatalf("load combined_boards.csv: %v", err)
	}
	scheduleStore, err := NewScheduleStore(dataDir + "/schedule.json")
	if err != nil {
		log.Fatalf("load schedule.json: %v", err)
	}
	activity, err := NewActivityLog(dataDir, 500)
	if err != nil {
		log.Fatalf("load activity.jsonl: %v", err)
	}
	log.Printf("activity log: loaded %d run(s) from %s/activity.jsonl", len(activity.List()), dataDir)

	// Read-only: never opening the pool at all (DATABASE_URL unset) or a
	// bad DSN are both non-fatal — every added-status read degrades to the
	// CSV's frozen `added` column with a banner, rather than the whole app
	// refusing to start over a database that's merely unreachable right now.
	var dbStore *DBStore
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		dbStore, err = NewDBStore(dbURL)
		if err != nil {
			log.Printf("db: %v — added status will show as unreachable until this is fixed", err)
			dbStore = nil
		}
	}

	cleanupStore, err := NewCleanupStore(dataDir + "/cleanup.json")
	if err != nil {
		log.Fatalf("load cleanup.json: %v", err)
	}

	runner := NewRunner(csvStore, activity, dbStore, cleanupStore, binariesFromEnv())
	runner.OnCrawlFinished = scheduleStore.RecordProviderCrawl

	scheduler := NewScheduler(scheduleStore, runner)

	stop := make(chan struct{})
	go scheduler.Run(stop)

	app := &App{
		tmpl:      LoadTemplates(),
		csv:       csvStore,
		activity:  activity,
		schedules: scheduleStore,
		runner:    runner,
		cleanup:   cleanupStore,
		explainer: NewExplainerFromEnv(),
		sessions:  NewSessionStore(),
		db:        dbStore,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", handleLoginPage(app.tmpl))
	mux.HandleFunc("POST /login", handleLoginSubmit(app.sessions))
	mux.HandleFunc("POST /logout", handleLogout(app.sessions))

	mux.HandleFunc("GET /{$}", requireAuth(app.sessions, handleCatalog(app)))
	mux.HandleFunc("GET /catalog/results", requireAuth(app.sessions, handleCatalogResults(app)))
	mux.HandleFunc("POST /crawl", requireAuth(app.sessions, handleCrawl(app)))
	mux.HandleFunc("POST /reindex", requireAuth(app.sessions, handleReindexNow(app)))
	mux.HandleFunc("POST /providers/remove", requireAuth(app.sessions, handleRemoveProvider(app)))
	mux.HandleFunc("POST /companies/refresh", requireAuth(app.sessions, handleCompanyRefresh(app)))
	mux.HandleFunc("POST /bulk", requireAuth(app.sessions, handleBulkCrawl(app)))
	mux.HandleFunc("POST /new-provider", requireAuth(app.sessions, handleNewProvider(app)))

	// Providers was merged into Catalog as its "Added" view — now the
	// default — and the old URL keeps working for bookmarks.
	mux.Handle("GET /providers", http.RedirectHandler("/", http.StatusMovedPermanently))

	mux.HandleFunc("GET /schedules", requireAuth(app.sessions, handleSchedules(app)))
	mux.HandleFunc("POST /schedules/save", requireAuth(app.sessions, handleScheduleSave(app)))
	mux.HandleFunc("GET /schedules/load", requireAuth(app.sessions, handleScheduleLoad(app)))
	mux.HandleFunc("POST /schedules/delete", requireAuth(app.sessions, handleScheduleDelete(app)))
	mux.HandleFunc("POST /schedules/toggle", requireAuth(app.sessions, handleScheduleToggle(app)))

	mux.HandleFunc("GET /activity", requireAuth(app.sessions, handleActivity(app)))
	mux.HandleFunc("GET /activity/status", requireAuth(app.sessions, handleActivityStatus(app)))
	mux.HandleFunc("POST /activity/explain", requireAuth(app.sessions, handleExplain(app)))
	mux.HandleFunc("POST /cleanup/preview", requireAuth(app.sessions, handleCleanup(app, false)))
	mux.HandleFunc("POST /cleanup/run", requireAuth(app.sessions, handleCleanup(app, true)))

	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	log.Printf("board-console listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func binariesFromEnv() Binaries {
	b := DefaultBinaries()
	b.BulkAddBoards = envOr("BULK_ADD_BOARDS_BIN", b.BulkAddBoards)
	b.AddBoard = envOr("ADD_BOARD_BIN", b.AddBoard)
	b.Ingest = envOr("INGEST_BIN", b.Ingest)
	b.Reindex = envOr("REINDEX_BIN", b.Reindex)
	b.CloseChronicBoards = envOr("CLOSE_CHRONIC_BOARDS_BIN", b.CloseChronicBoards)
	b.RecountCompanies = envOr("RECOUNT_COMPANIES_BIN", b.RecountCompanies)
	b.ReindexCompanies = envOr("REINDEX_COMPANIES_BIN", b.ReindexCompanies)
	b.CSVPath = envOr("DATA_DIR", "/app/data") + "/combined_boards.csv"
	return b
}
