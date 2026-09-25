# AGENTS.md

Guidance for AI agents working in this directory.

## What this is

An internal ops service for managing which ATS/aggregator/career-site
"boards" get added to freehire's catalog and crawled — a plain Go HTTP
service (stdlib `net/http`, no framework) serving a JSON API that replaces
what used to be hand-editing `combined_boards.csv` and running one-off
commands against the running freehire stack.

**It has no UI.** The UI is a separate web app in its own repository that
only ever talks to this API; a change that needs a screen goes there, and a
change here that alters a response shape must be matched there.

**It is not a freehire feature.** It is a separate Go module living in a
subfolder of this repo so that pulling upstream freehire updates
(`git merge`/`git pull`) never conflicts with it. See [README.md](README.md)
for what it does and how to run it — this file is about working on the
code itself.

## The isolation contract — read before touching anything here

- **Own Go module** (`go.mod`, `go 1.26`, its own `go.sum`). `go build
  ./...` at the repo root never touches this directory, and building this
  directory never touches the root module.
- **Zero imports of freehire's `internal/...` packages.** Not "avoid
  importing them" — the module boundary makes it impossible; there is no
  `replace` directive back to the root module.
- **Exactly two ways of reaching freehire, both narrow and explicit:**
  1. `exec.Command`-ing freehire's already-built worker binaries
     (`bulk-add-boards`, `ingest`, `reindex`) as local subprocesses inside
     board-console's own container — never `docker exec` into a separate
     one. `runner.go` is the only file that shells out.
  2. A **read-only** `database/sql` connection to freehire's own Postgres
     (`db.go`, `jackc/pgx/v5/stdlib`) used for exactly one query: how many
     of a provider's boards are `active`/`pending`. **board-console's own
     Go code must never execute an INSERT/UPDATE/DELETE against that
     database** — every write path stays inside freehire's own binaries,
     called through path 1. If you're tempted to add a second query or a
     write, stop and reconsider; this connection exists for one read, not
     as a general-purpose DB client.
- **No ORM, no sqlc, no migrations** — this isn't part of freehire's
  `internal/platform/db` generated layer, and never should be. `db.go`'s
  query is a literal string because there is exactly one of them.

A change that imports anything under the repo root's `internal/`, adds a
`replace` directive to `go.mod`, or has board-console write to Postgres is
wrong regardless of how convenient it looks in the moment — say so and
suggest the alternative rather than doing it.

## Layout

```
board-console/
  main.go              wiring: stores, Runner, Scheduler, routes, App struct
  api.go               the API's middleware (key, CORS), errors, and the
                        catalog/providers/schedule-save routes
  api_pages.go         the schedules, system-jobs, runs and activity routes
  csvstore.go           combined_boards.csv: load/backfill/atomic save,
                        DistinctProviders/AddedProviders/FullyAddedProviders
  db.go                 read-only Postgres: DBStore.AddedCounts,
                        resolveAddedCounts (the DB-or-stale-CSV-fallback
                        pattern every view builder uses)
  runner.go             drives the freehire binaries as subprocesses —
                        the ingest semaphore, the reindex coalescing loop,
                        Runner.FullyAdded
  schedule.go            schedule.json: Schedule, ScheduleStore
                        (Add/Update/Delete/Toggle), Scheduler (the 1-minute
                        tick)
  system_jobs.go         system.json: the dead-board cleanup and company
                        recount's time, pause and last run
  activity.go            activity.jsonl: ActivityLog ring buffer + JSONL
                        persistence, the file-size bounding, Run/RunStatus
  jobs.go / outcome.go   runs grouped into jobs; a run's result read from
                        its own output
  providerkind.go        the hand-maintained provider -> kind map,
                        displayName() (Aggregator's auto-filled company)
  handlers_*.go          the views the API serves — catalog, schedules,
                        activity, system — each owns its build* function
                        and the domain helpers behind its actions
  data/                  the persistent files (see README) — a
                        docker-compose bind mount. Only combined_boards.csv
                        is tracked; activity.jsonl, schedule.json and
                        system.json are per-machine and git-ignored
  *_test.go              table-driven tests beside the code they cover
                        (no separate test package)
```

## Non-obvious things

- **Rebuilding the binary is not the same as rebuilding the deployment.**
  This has bitten every round of work on this tool: `go build` locally
  proves the code compiles, but the actual running service is a Docker
  image (`docker-compose.yml`'s `board-console` service, built from the
  `board-console-build`/`board-console` stages in the root `Dockerfile`).
  A change isn't visible to anyone using the deployed tool until
  `docker compose up --build -d board-console` runs. If a change "doesn't
  seem to have landed" when someone reports back, this is the first thing
  to check — not a bug in the code.
- **Concurrency is per-purpose, not one lock** (`runner.go`). `ingest` runs
  through a bounded semaphore (`maxConcurrentIngest`, currently 4) — a
  click that arrives once all slots are busy still gets an Activity row
  immediately, shown `queued` rather than silently doing nothing (a real
  bug in an earlier version, when everything shared one mutex). `reindex`
  is deliberately still serialized — it's a full-catalog rebuild, and two
  running at once is wasted work, not extra throughput — but *coalesced*
  rather than blocking: a request arriving mid-run is satisfied by one
  extra run right after, never a stacked second one. Adding a new
  subprocess action means deciding which of these two shapes it needs, not
  reaching for a third pattern.
- **Subprocesses inherit board-console's environment**, `CATALOGUE_TECH_ONLY`
  included, so docker-compose must give board-console the SAME value as the
  app (both read `${CATALOGUE_TECH_ONLY:-false}`). A crawl that disagrees
  with the site about the catalogue's scope stores or drops the wrong jobs.
  `/api/v1/meta`'s `techOnly` (`scope.go`) mirrors freehire's parsing rule.
- **Catalogue-wide Meilisearch rebuilds share `Runner.heavyMu`** (jobs
  reindex, reindex-companies). freehire guards them with an advisory lock
  that makes the loser SKIP and exit 0, so without the mutex a company
  refresh during a reindex would silently do nothing.
- **"Remove provider" retires through freehire's own `add-board --retire`**,
  one call per board, never by writing SQL — the isolation contract holds.
  It must also delete the provider's schedules: a crawl adds whatever boards
  are missing, so a surviving schedule would silently re-add them all.
- **`ActivityLog.List()` returns snapshot copies.** Running Runs are written
  by their subprocess goroutine; never hand a live `*Run` to a response.
- **Every view's "added" figure goes through `resolveAddedCounts` in
  `db.go`.** It reads Postgres when reachable and falls back to the CSV's
  frozen `added` column (reported as `dbError`) when it isn't. A new view or
  query that needs added status should call this, not read `BoardRow.Added`
  directly or query the DB itself — the fallback-and-report behavior is the
  point, and duplicating the DB call bypasses it.
- **"Fully added" has exactly one correct definition**:
  `CSVStore.FullyAddedProviders()` / `Runner.FullyAdded()` — every one of
  a provider's candidate rows added, not just one. An earlier version
  checked "any row added," which silently left a partially-added
  provider's remaining rows un-added forever; if you're about to write a
  new added-ness check, use the existing one instead.
- **The API is the only interface, and it has no login.** `apiMiddleware`
  (`api.go`) wraps every route: `BOARD_CONSOLE_API_KEY` set means a Bearer
  key is required, unset means open (local only); browsers are admitted by
  `BOARD_CONSOLE_CORS_ORIGINS`, whose preflight the middleware answers. Every
  error is `{"error": msg}` through `actionError`, with the status a UI can
  act on (409 "already running", 422 validation, 503 database/model
  unavailable). A new route goes through the same `api(...)` registration so
  it is never mounted without the guard.
- **Validation lives here, not in a UI.** A UI may check fields first for a
  nicer message, but `addProviderRow` (the kind rules: an ATS platform needs
  provider+board+company, an Aggregator the provider only with board forced
  blank and company derived by `displayName()`, a Career site provider+company
  with board forced blank) and `saveSchedule` are what protect the data, and
  their messages are the ones a UI shows.
- **Runtime state never goes through git.** `activity.jsonl`,
  `schedule.json` and `system.json` are git-ignored because committing them
  copied one machine's schedules and history onto another (a laptop's
  15-minute schedule started crawling on the VPS). Every store must keep
  starting cleanly from a missing file. `combined_boards.csv` IS tracked —
  still test against a copied/temp `DATA_DIR` so a test's appended rows
  never reach a commit.

## Commands

```bash
cd board-console
gofmt -l .                 # must print nothing before committing
go build ./...
go vet ./...
go test ./...               # table-driven, no external deps, no testcontainers

go build -o /tmp/bc .        # local smoke-testing binary (API on :8091/api/v1)
DATA_DIR=/tmp/some-copy PORT=8091 \
  INGEST_BIN=... REINDEX_BIN=... BULK_ADD_BOARDS_BIN=... \
  /tmp/bc                    # override binaries to test without the real ones
```

From the repo root, to actually deploy a change:
```bash
docker compose up --build -d board-console   # rebuilds just this service
```

No linked linters beyond `gofmt`/`go vet` — this module is small enough
that `golangci-lint`'s root config (which targets the freehire module) does
not apply here.
