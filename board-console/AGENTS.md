# AGENTS.md

Guidance for AI agents working in this directory.

## What this is

An internal ops tool for managing which ATS/aggregator/career-site "boards"
get added to freehire's catalog and crawled — a plain server-rendered Go
web app (stdlib `net/http` + `html/template`, no framework) that replaces
what used to be hand-editing `combined_boards.csv` and running one-off
commands against the running freehire stack.

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
  auth.go               hardcoded creds (see README's Credentials section),
                        session cookie, requireAuth middleware
  csvstore.go           combined_boards.csv: load/backfill/atomic save,
                        DistinctProviders/AddedProviders/FullyAddedProviders
  db.go                 read-only Postgres: DBStore.AddedCounts,
                        resolveAddedCounts (the DB-or-stale-CSV-fallback
                        pattern every page-data builder uses)
  runner.go             drives the freehire binaries as subprocesses —
                        the ingest semaphore, the reindex coalescing loop,
                        Runner.FullyAdded
  schedule.go            schedule.json: Schedule, ScheduleStore
                        (Add/Update/Delete/Toggle/Get), Scheduler (the
                        1-minute tick)
  activity.go            activity.jsonl: ActivityLog ring buffer + JSONL
                        persistence, the file-size bounding, Run/RunStatus
  providerkind.go        the hand-maintained provider -> kind map,
                        displayName() (Aggregator's auto-filled company)
  templates.go           embed.FS + html/template loading
  respond.go             how a POST action answers: 204/422 to app.js's
                        fetch, a redirect back to the referring page
                        otherwise (isFetch, actionDone, actionError)
  handlers_*.go          one file per screen: catalog, providers,
                        schedules, activity — each owns its
                        build*PageData function and HTTP handlers
  templates/*.html        one {{define}} per page/fragment; catalog.html +
                        catalog-results.html split because the latter is
                        also the real-time search AJAX fragment;
                        schedule_modal.html is the ONE add/edit schedule
                        dialog, included by every page that needs it
  static/{style.css,app.js}  the whole frontend — no build step, no bundler
  data/                  the three persistent files (see README) — a
                        docker-compose bind mount, so it's also where a
                        fresh clone's runtime state lives if you test
                        locally with DATA_DIR=./data
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
- **Every page's "added" figure goes through `resolveAddedCounts` in
  `db.go`.** It reads Postgres when reachable and falls back to the CSV's
  frozen `added` column (with a banner) when it isn't. A new page or query
  that needs added status should call this, not read `BoardRow.Added`
  directly or query the DB itself — the fallback-and-banner behavior is
  the point, and duplicating the DB call bypasses it.
- **"Fully added" has exactly one correct definition**:
  `CSVStore.FullyAddedProviders()` / `Runner.FullyAdded()` — every one of
  a provider's candidate rows added, not just one. An earlier version
  checked "any row added," which silently left a partially-added
  provider's remaining rows un-added forever; if you're about to write a
  new added-ness check, use the existing one instead.
- **Row actions never navigate.** Kebab items and Catalog's Crawl buttons
  are `<form data-async>`, and the schedule dialog submits the same way:
  app.js POSTs them with the `X-Board-Console-Fetch` header, confirms with
  a toast, and re-renders the page's `[data-live-region]` in place when
  the table changed. Handlers finish through `actionDone` (`respond.go`),
  never a hard-coded redirect — and never to `/activity`, which is a page
  the operator visits, not one they are sent to. The forms keep a real
  `action` so they still work without JS.
- **Every add/edit form round-trips on a validation failure** — the
  pattern in `handleNewProvider` and `handleScheduleSave`: a fetch caller
  gets a 422 with the message (`actionError`); a plain form post re-renders
  the SAME page template with the submitted values filled back in and the
  modal flagged to reopen, never a redirect that loses what was typed.
  Client-side JS validation exists too but is never the source of truth;
  the server check is what actually protects the data.
- **Kebab menus are `position: fixed`**, placed at their button by app.js
  and flipped upward near the viewport bottom. `.table-card` clips its
  overflow for its rounded corners, so an absolutely positioned menu was
  cut off in a short table. Layering lives in two tokens, `--z-dropdown`
  and `--z-toast`; use them rather than a literal z-index.
- **Kind-driven fields in "+ New provider"** (`handlers_catalog.go`,
  `app.js`): an ATS platform needs Provider+Board+Company, an Aggregator
  needs Provider only (Board forced blank, Company auto-derived via
  `displayName()`), a Career site needs Provider+Company (Board forced
  blank). Enforced in JS for the live show/hide AND server-side as a
  fallback — the two must be kept in sync if the rules change; JS's
  `displayName()` in `app.js` deliberately mirrors Go's in
  `providerkind.go` line for line.
- **Templates are one global namespace.** `templates.go` parses every file
  under `templates/*.html` together, so every `{{define "name"}}` must be
  unique across the whole directory — two files defining the same name
  silently shadow each other with no compile error.
- **CSS is one file, one fixed dark palette** (`static/style.css`) — no
  light/dark media query, unlike freehire's own design system. The tokens
  at the top (`--bg`, `--surface`, `--accent`, ...) are board-console's
  own identity, deliberately not a copy of freehire's brand green; keep
  new component styles referencing those custom properties rather than
  literal colors.
- **`data/*` files you create while testing locally are real, tracked
  files if you're pointed at the repo's own `data/` directory** — always
  test against a copied/temp `DATA_DIR`, never the tracked one, unless
  you mean to commit what you produce.

## Commands

```bash
cd board-console
gofmt -l .                 # must print nothing before committing
go build ./...
go vet ./...
go test ./...               # table-driven, no external deps, no testcontainers

go build -o /tmp/bc .        # local smoke-testing binary
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
