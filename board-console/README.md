# board-console

An internal ops tool for managing which ATS/aggregator/career-site "boards"
get added to freehire's catalog and crawled. It replaces the previous
by-hand workflow of editing `combined_boards.csv` and running one-off
commands against the running freehire stack.

## Isolation from freehire

board-console is a **separate Go module** (`board-console/go.mod`, its own
module path) with **zero imports** of freehire's `internal/...` packages.
Nothing in this folder is needed to build, test, or run the rest of the
freehire repo, and vice versa.

board-console has two, and only two, ways of reaching freehire, neither of
which is a code-level dependency:

1. Running freehire's already-built worker binaries — `bulk-add-boards`,
   `ingest`, `reindex` — as **local subprocesses inside board-console's own
   container**. Those binaries are copied into board-console's Docker
   image at build time from freehire's own build stage (see the
   `board-console-build` / `board-console` stages in the root
   `Dockerfile`) — they are not recompiled here, and board-console's own Go
   code never links against them, it only `exec.Command`s them.
2. A **read-only** `database/sql` connection to freehire's own Postgres
   database (`db.go`), against the same `DATABASE_URL` docker-compose
   already passes to the `app` service, used solely to answer "is this
   provider's board already added" (see Data below). board-console's own
   Go code never writes a single row to that database — every write path
   in this repo still goes through freehire's own binaries.

## Data

`data/combined_boards.csv` is board-console's own candidate list — moved
(not copied) from freehire's `cmd/bulk-add-boards/combined_boards.csv`. It
carries two columns beyond what `cmd/bulk-add-boards` itself reads
(`provider`, `board`, `company`, matched by header name):

- `id` — a stable identifier per row (a short hash of
  `provider|board|company`), backfilled once on first load for any row
  that predates it.
- `added` — a legacy column, no longer written by board-console's own
  code (see below). Read only as a stale fallback when the database can't
  be reached.

Every write is atomic (temp file + rename), so a crash mid-write can't
corrupt the CSV, and the extra columns are safe for `cmd/bulk-add-boards`
to ignore if it's ever run directly against this file.

**Added status is read live from Postgres, not the CSV.** A CSV `added`
column can't stay in sync across separate dev/prod databases that might
each run their own board-console against the same repo checkout, so
Catalog and Schedules both read it fresh instead:
```sql
SELECT provider, board, status, count(*)
FROM boards WHERE status IN ('active', 'pending')
GROUP BY provider, board, status
```
via a **read-only** `database/sql` connection (`db.go`, `jackc/pgx/v5/stdlib`)
against the same `DATABASE_URL` docker-compose already passes to the `app`
service — board-console's own Go code never writes to that database, ever.
If it can't be reached, every page falls back to the CSV's frozen `added`
column and shows a banner ("Couldn't reach the database — added status may
be stale") rather than silently showing wrong data or crashing. `runner.go`'s
`runAdd()` correspondingly no longer marks the CSV's `added` column — the
CSV's job is only the candidate list (`id`/`provider`/`board`/`company`)
now.

`data/schedule.json` is the only persistent store for recurring crawl
schedules, written the same atomic way.

`data/activity.jsonl` (JSON Lines — one snapshot per line) persists the run
history shown on the Activity page, so it survives a restart. It's
append-only rather than atomically rewritten on every update: each run gets
a line on start, periodically while still running (throttled, so a chatty
crawl doesn't hammer the disk), and always on finish. The last 500 distinct
runs are loaded back into memory on startup; a run still marked "running"
at load time means the process was killed or crashed mid-run, so it's
recovered as failed rather than left stuck.

The file is bounded from three directions, checked at runtime (not just
startup, so a long-lived process can't grow it unbounded between restarts):
kept to the last **10,000 lines**, kept under **50MB** even if those lines
individually carry a lot of captured output, and any single run's
stdout/stderr is itself capped at 2MB (oldest output dropped first, since
the tail — recent progress, or the error that ended it — is what a huge
dump gets read for).

All of them live under `data/`, which is mounted as a volume in
`docker-compose.yml` so they survive container rebuilds. Only
`combined_boards.csv` is tracked in git — it is the shared board catalog.
`activity.jsonl`, `schedule.json` and `cleanup.json` are **per machine** and
git-ignored: each environment (a laptop, the VPS) keeps its own run
history, schedules and cleanup clock, and a fresh clone starts them empty.

On a Linux host the container (uid 65532) must be able to write `data/`,
while your own user must still be able to `git pull` the CSV. Once per
clone:

```bash
sudo chown -R "$USER":65532 board-console/data
sudo chmod -R g+rwX board-console/data
sudo chmod g+s board-console/data   # new files inherit the group
```

(Docker Desktop on macOS does not enforce this, which is why it only bites
on the server.)

## Credentials

Login is a single hardcoded username/password, checked in
[`auth.go`](auth.go):

```go
const (
	consoleUsername = "admin"
	consolePassword = "change-me"
)
```

**Change these before deploying anywhere reachable by more than you** —
edit the constants directly and rebuild. Sessions are random tokens held in
an in-memory map; restarting the process logs everyone out, which is fine
for a private internal tool.

## Running it

board-console is wired into the root `docker-compose.yml` as its own
service, alongside `app`/`db`/`meilisearch`/`redis`/`web`. It comes up
automatically with:

```bash
make up
```

No separate build or run step is needed — `make up` (`docker compose up
--build -d`) builds and starts it like everything else. Once running, it's
reachable at:

```
http://localhost:8040
```

(override the host port with `BOARD_CONSOLE_HOST_PORT`).

### Ports

Every host port `make up` publishes, from `docker-compose.yml`. Each one can
be moved by setting its variable in `.env`.

| Service | URL on the host | Container port | Override with |
|---|---|---|---|
| Board Console | http://localhost:8040 | 8091 | `BOARD_CONSOLE_HOST_PORT` |
| Web (the site) | http://localhost:8090 | 80 | `WEB_HOST_PORT` |
| API (Go server) | http://localhost:8080 | 8080 | `HIRE_HOST_PORT` |
| Postgres | localhost:5432 | 5432 | `DB_HOST_PORT` |
| Meilisearch | http://localhost:7700 | 7700 | `MEILI_HOST_PORT` |
| MinIO (S3) | http://localhost:9000 | 9000 | `MINIO_HOST_PORT` |
| Redis | not published | 6379 | — |

8091 is deliberately NOT a host port: Beszel uses it. Board Console still
listens on 8091 *inside* its container; only the published port moved.

### Building/running outside Docker (e.g. for local development on this
folder alone)

```bash
cd board-console
go build -o board-console .
DATA_DIR=./data PORT=8091 ./board-console
```

Outside the Docker Compose network, `bulk-add-boards`/`ingest`/`reindex`
won't be on `PATH` at their default container locations (`/app/...`) — set
`BULK_ADD_BOARDS_BIN`, `INGEST_BIN`, `REINDEX_BIN` to point at locally built
copies (`go build -o ... ./cmd/<name>` from the repo root) if you want the
Crawl/Reindex actions to actually run something in this mode. The web UI
and CSV/schedule management work regardless.

### Environment variables

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8091` | HTTP listen port |
| `DATA_DIR` | `/app/data` | Where `combined_boards.csv` / `schedule.json` live |
| `DATABASE_URL` | — | Read-only Postgres connection for live added status (`db.go`), **and** passed through to the `ingest`/`reindex` subprocesses |
| `MEILI_URL` / `MEILI_MASTER_KEY` | — | Passed through to `reindex` |
| `BULK_ADD_BOARDS_BIN` / `INGEST_BIN` / `REINDEX_BIN` | `/app/bulk-add-boards` / `/app/ingest` / `/app/reindex` | Override the binary paths (for running outside the container) |

## Behaviour notes

- **"+ New provider" fields follow the Kind selected**: an ATS platform
  needs Provider + Board + Company (many companies per platform, each with
  its own board); an Aggregator needs Provider only — Board is forced
  blank and Company is auto-derived from the provider slug (`remoteok` →
  `Remoteok`, imperfect by design, hand-fixable in the CSV afterward), and
  it always crawls immediately since there's no meaningful "add without
  crawling" step for a single feed; a Career site needs Provider + Company
  — Board is forced blank. Enforced both in the browser (field
  show/hide) and server-side (`handleNewProvider` in
  `handlers_catalog.go`) as a fallback if JS didn't run.
- **Reindex batching**: a single-row Crawl reindexes once, immediately
  after. A scheduled run with multiple providers (via `RunBatch`) ingests
  each one sequentially and reindexes **once**, at the end — never once
  per provider.
- **Concurrency is per-purpose, not one global lock** (`runner.go`):
  `ingest` is bounded by a semaphore (`maxConcurrentIngest`, 4 at once,
  across every caller) — different providers genuinely crawl in parallel,
  and a click that arrives once all 4 slots are busy still gets its own
  Activity row immediately, shown as **`queued`** until a slot frees up,
  rather than looking silently dropped (the previous single global mutex's
  failure mode). `reindex` is a full-catalog operation, so it stays
  serialized instead — but *coalesced*, not blocking: a request that
  arrives while one is already running is satisfied by one extra run right
  after the current one finishes, never by stacking a redundant second run
  or by the caller waiting for it. `add-boards` has no limit of its own —
  fast, scoped to one provider, and (since it no longer writes to the CSV)
  nothing to race on.
- **Schedules**: checked once a minute; the floor is 2 minutes and a
  shorter value is rejected inline. Each due schedule starts in its own
  goroutine, so a slow crawl never delays another schedule, but a schedule
  never overlaps ITSELF — while its run is in flight it is skipped, and if
  the run outlasted its interval it starts again on the next tick. The
  interval is measured from a run's START, which is stamped (status
  `running`) the moment it begins; the Schedules page refreshes itself while
  a run is in flight, and each row expands to its provider's latest crawl
  output. A host that sleeps (a laptop) pauses all of this — the tick and
  the 30-minute subprocess timeout alike — so a missed slot there is the
  sleep, not the scheduler. A schedule that has
  never run is due on the very next tick — fixed a bug where it never
  became due at all, because "never run" resolved to a fresh `time.Now()`
  on every check, a moving target a fixed comparison could never catch up
  to.
- **Activity log**: the last 500 runs, backed by `data/activity.jsonl` — a
  working log, not an audit trail (it's trimmed and only throttled-persisted
  while a run is in progress, so a crash can lose the last few seconds of a
  still-running command's output). Survives a restart.
- **Live logs**: a run's stdout/stderr streams into the Activity page while
  it's still going — expand a row to watch it grow, polled every 2s.
- **Activity filters and pages**: status/action/provider and `page` (25
  runs a page), as query params on `/activity` — the live-poll endpoint
  carries the same ones, so a filtered page keeps patching in place. When
  the set of runs on the page changes, the table re-renders in place with
  expanded rows kept open. A "Reindex now" button sits in the header.
- **Catalog is also the providers view**: each row joins the three stores —
  how fully the provider is added, its schedule if any (interval, next
  run, paused), and its most recent activity run. The "Added" switch
  (`?show=added`) narrows it to providers with at least one added board,
  which is what the old Providers page showed; `/providers` redirects there.
- **Search**: multi-word, order-independent — every word must appear
  somewhere across the provider and company text, so "green house" matches
  "Greenhouse" as readily as "greenhouse" does.
- **Already-added providers only ever get crawled, never re-added** — the
  "fully added" check (`CSVStore.FullyAddedProviders`, `Runner.FullyAdded`)
  requires every one of a provider's candidate rows to be added, not just
  one; a fix from when this still read the CSV, now carried over to the
  live DB read.
- **Schedules support edit and delete, not just add** — one modal handles
  both (`POST /schedules/save`, branching on a hidden `id` field: empty
  creates, set updates the existing entry in place), reachable either from
  the Schedules page's own kebab menu or from Catalog's. `POST
  /schedules/delete` removes one; the confirmation is a plain
  `confirm()` (the form's `data-confirm`), no custom dialog needed for
  something reversible by re-adding.
- **Catalog's row actions are a kebab (⋮) menu**, not separate buttons:
  Crawl/Add+Crawl, Reindex now, and Add/Edit/Delete schedule (whichever
  apply). One delegated click listener (`app.js`) opens/closes every
  menu on the page — clicking a toggle opens its own menu and closes every
  other one; clicking anywhere else closes all of them.
- Subprocess calls run with a 30-minute timeout so a hung command can't
  block the server indefinitely, while still allowing a genuinely slow
  crawl to finish.

## Requirements

board-console's own binary has no Docker dependency — it runs
`bulk-add-boards`/`ingest`/`reindex` as plain local subprocesses inside its
own container, never via `docker exec`. `docker` (or an equivalent Compose
tool) is only needed on the host to build and run the container itself, the
same as for every other service in this repo.
