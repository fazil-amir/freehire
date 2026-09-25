# board-console

An internal ops service for managing which ATS/aggregator/career-site
"boards" get added to freehire's catalog and crawled, on demand and on a
schedule. It replaces the previous by-hand workflow of editing
`combined_boards.csv` and running one-off commands against the running
freehire stack.

**It has no UI of its own.** Everything it does is a JSON API under
`/api/v1` (see API below); the UI is a separate web app in its own
repository — or any other client, such as the team's main dashboard.

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
each run their own board-console against the same repo checkout, so the
catalog and schedules both read it fresh instead:
```sql
SELECT provider, board, status, count(*)
FROM boards WHERE status IN ('active', 'pending')
GROUP BY provider, board, status
```
via a **read-only** `database/sql` connection (`db.go`, `jackc/pgx/v5/stdlib`)
against the same `DATABASE_URL` docker-compose already passes to the `app`
service — board-console's own Go code never writes to that database, ever.
If it can't be reached, it falls back to the CSV's frozen `added` column and
says so (the `dbError` field of `/catalog` and `/schedules`) rather than
silently serving wrong data or crashing. `runner.go`'s
`runAdd()` correspondingly no longer marks the CSV's `added` column — the
CSV's job is only the candidate list (`id`/`provider`/`board`/`company`)
now.

`data/schedule.json` is the only persistent store for recurring crawl
schedules, written the same atomic way.

`data/activity.jsonl` (JSON Lines — one snapshot per line) persists the run
history (`/activity`), so it survives a restart. It's
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
`activity.jsonl`, `schedule.json` and `system.json` are **per machine** and
git-ignored: each environment (a laptop, the VPS) keeps its own run
history, schedules and system-job settings, and a fresh clone starts them
empty. (`system.json` replaced `cleanup.json`, whose last run it takes over
once on first start.)

On a Linux host the container (uid 65532) must be able to write `data/`
while your own user can still `git pull` the CSV. `make up` handles this:
the one-shot `board-console-init` service (docker-compose.yml) gives `data/`
the container's group with group write, before board-console starts, on
every run — the owner is never changed, so git keeps working. Nothing to do
by hand on a fresh server. (Docker Desktop on macOS never enforces these
permissions, which is why the problem only ever showed up on the server.)

## Access

There is no login. The API is guarded by two settings:

- **`BOARD_CONSOLE_API_KEY`** — when set, every request needs
  `Authorization: Bearer <key>` (compared in constant time). **Unset, the API
  is open**: fine on a laptop, never on a reachable port. Set it anywhere
  else, or keep the port closed and reach it over an SSH tunnel or a private
  network.
- **`BOARD_CONSOLE_CORS_ORIGINS`** — the comma-separated browser origins
  allowed to call it (default `http://localhost:5173`, the UI's dev server).
  A server-to-server caller needs no entry.

## Running it

board-console is wired into the root `docker-compose.yml` as its own
service, alongside `app`/`db`/`meilisearch`/`redis`/`web`. It comes up
automatically with:

```bash
make up
```

No separate build or run step is needed — `make up` (`docker compose up
--build -d`) builds and starts it like everything else. Once running, its
API is at:

```
http://localhost:8040/api/v1
```

(override the host port with `BOARD_CONSOLE_HOST_PORT`). The root, `/`,
only answers where the API is. For the UI, run the separate web app and add
its address to `BOARD_CONSOLE_CORS_ORIGINS`.

### Ports

Every host port `make up` publishes, from `docker-compose.yml`. Each one can
be moved by setting its variable in `.env`.

| Service | URL on the host | Container port | Override with |
|---|---|---|---|
| Board Console API | http://localhost:8040/api/v1 | 8091 | `BOARD_CONSOLE_HOST_PORT` |
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
crawl/reindex actions to actually run something in this mode. The API and
CSV/schedule management work regardless.

### Environment variables

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8091` | HTTP listen port |
| `DATA_DIR` | `/app/data` | Where `combined_boards.csv` / `schedule.json` live |
| `DATABASE_URL` | — | Read-only Postgres connection for live added status (`db.go`), **and** passed through to the `ingest`/`reindex` subprocesses |
| `MEILI_URL` / `MEILI_MASTER_KEY` | — | Passed through to `reindex` |
| `BULK_ADD_BOARDS_BIN` / `INGEST_BIN` / `REINDEX_BIN` | `/app/bulk-add-boards` / `/app/ingest` / `/app/reindex` | Override the binary paths (for running outside the container) |
| `BOARD_CONSOLE_API_KEY` | — | Required as `Authorization: Bearer <key>` when set; unset leaves the API open (see Access) |
| `BOARD_CONSOLE_CORS_ORIGINS` | `http://localhost:5173` | Browser origins allowed to call the API, comma-separated |
| `SCHEDULE_CAPACITY` | `2` | How many scheduled crawls run at once; a time holding this many runs is booked |
| `DOCKER_PROXY_URL` | — (compose sets `http://docker-proxy:2375`) | Docker access for the build-cache figure and prune; empty turns it off |
| `OPENAI_API_KEY` | — | Enables **Explain this run** (`POST /runs/{id}/explain`: the run's details and log tail go to the model with a built-in briefing on this tool). Set it in `.env`, never in a committed file; unset makes the endpoint answer 503 |
| `OPENAI_MODEL` | `gpt-4o-mini` | The model that writes the explanation |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Any OpenAI-compatible `/chat/completions` endpoint |

## API

JSON in and out; every error is `{"error": "…"}` with its status (400, 404,
409 for "already running", 422 for a validation failure, 503). Times of day
are minutes after 00:00 UTC; instants are RFC 3339.

| Method and path | What it does |
|---|---|
| `GET /api/v1/meta` | Scope (`CATALOGUE_TECH_ONLY`), build, schedule capacity, kind tabs, the provider → kind map |
| `GET /api/v1/catalog?show=all&kind=&q=&page=` | One page of providers (added only unless `show=all`), each with its schedule and last run |
| `POST /api/v1/providers` | "+ New provider": `{provider, board, company, crawlNow}` — 201, or 422 with the message |
| `POST /api/v1/providers/{p}/crawl` | Crawl (Add + Crawl when not fully added); `{"refetchAll": true}` for a full re-crawl — 202, or 409 |
| `POST /api/v1/providers/{p}/remove` | Remove provider (retire its boards, drop its schedule, purge its activity) — 202, 409 or 503 |
| `POST /api/v1/reindex` | Queue a reindex (coalesced with one already running) |
| `GET /api/v1/schedules` | The plan: load per slot, scheduled providers (each run's latest result), added providers without a schedule, system jobs |
| `GET /api/v1/schedules/load` | Load per 15-minute slot and the capacity, for checking a new time |
| `PUT /api/v1/schedules` | Save a provider's schedule `{id?, provider, times}` (one per provider: an existing one is updated) |
| `POST /api/v1/schedules/{id}/toggle` · `DELETE /api/v1/schedules/{id}` | Pause/resume · delete |
| `POST /api/v1/system-jobs/{key}/toggle` · `PUT …/time {minute}` · `POST …/run` | A system job (`cleanup`, `recount`): pause/resume, re-time, run now |
| `GET /api/v1/activity?view=cleanup&status=&action=&provider=&page=` | Jobs with their steps, paged (25), plus the cleanup's last and next run |
| `GET /api/v1/runs/{id}` · `POST /api/v1/runs/{id}/explain` | One run's log (and a cached explanation) · ask the model about it |
| `POST /api/v1/cleanup/preview` · `POST /api/v1/cleanup/run` | Dead-board cleanup: dry run · for real |
| `POST /api/v1/companies/refresh` | Recount companies |
| `GET /api/v1/system/stats` · `POST /api/v1/system/build-cache/prune` | Disk, memory, CPU, jobs running, Docker build cache · clear it |

## Behaviour notes

- **"+ New provider" fields follow the kind**: an ATS platform needs
  provider + board + company (many companies per platform, each with its
  own board); an Aggregator needs the provider only — board is forced blank
  and company is derived from the provider slug (`remoteok` → `Remoteok`,
  imperfect by design, hand-fixable in the CSV afterward), and it always
  crawls immediately, there being no "add without crawling" step for a
  single feed; a Career site needs provider + company — board is forced
  blank. Enforced server-side (`addProviderRow`), whatever the caller sent.
- **Concurrency is per-purpose, not one global lock** (`runner.go`):
  `ingest` is bounded by a semaphore (`maxConcurrentIngest`, 4 at once,
  across every caller) — different providers genuinely crawl in parallel,
  and a request that arrives once all 4 slots are busy still gets its own
  activity run at once, shown as **`queued`** until a slot frees up. One
  provider is never crawled twice at once (a second request is a 409).
  `reindex` is a full-catalog operation, so it stays serialized instead —
  but *coalesced*, not blocking: a request that arrives while one is already
  running is satisfied by one extra run right after the current one
  finishes. A single crawl reindexes once, immediately after.
- **Schedules run at the times you set**: a provider has one schedule, a
  list of daily run times on the 15-minute grid (stored as minutes after
  00:00 UTC). It crawls once at each, every day, and nothing moves those
  times. A time already holding `SCHEDULE_CAPACITY` (default 2) runs is
  **booked** — a warning, not a refusal. At run time the scheduler starts due
  runs oldest first only while fewer than `SCHEDULE_CAPACITY` crawls (from
  any source) are in flight; the rest are "queued" and start on the first
  tick after a crawl ends. Every run is one 15-minute slot: nothing guesses
  how long a crawl takes. A new or re-timed schedule first runs at its NEXT
  time. A run is skipped when a good crawl of the provider (from any source)
  ended within 15 minutes before it; after downtime only the latest missed
  run happens, once. Scheduled crawls don't reindex one by one: the first
  tick of each hour runs ONE reindex for all of them (a shared step of each
  crawl's job). Each planned run carries the result of the crawl that served
  its latest occurrence, and that run's id. Older `schedule.json` files are
  converted on load, and written back: "every N" becomes the nearest
  crawls-per-day from its last run's time, "N a day from a first run"
  becomes those N times, and several schedules of one provider merge into
  one.
- **Activity log**: the last 500 runs, backed by `data/activity.jsonl` — a
  working log, not an audit trail (it's trimmed and only throttled-persisted
  while a run is in progress, so a crash can lose the last few seconds of a
  still-running command's output). Survives a restart. A run's output grows
  live while it runs (`GET /runs/{id}`).
- **Activity lists jobs, not runs** (`jobs.go`): one entry per thing asked
  for — a Crawl (add-boards → ingest → reindex), a Cleanup (close-chronic-
  boards → reindex → recount → reindex-companies), a Reindex, … — with its
  steps, each with its log. Every run records its job(s) (`Run.Jobs`); a
  reindex that served several crawls at once belongs to each and says
  "shared with …". The main step decides the job's status; a later step
  that failed makes it partial ("… · reindex failed"). Runs recorded before
  jobs existed show as single-step jobs. Filters (status, action, provider)
  all apply to JOBS — the action filter means "has a step of that action".
- **The catalog** joins the three stores per provider — how fully it is
  added, its schedule if any, and its most recent run. It defaults to the
  added providers (at least one board added); `show=all` widens it to the
  whole catalog. **Search** is multi-word and order-independent: every word
  must appear somewhere across the provider and company text, so
  "green house" matches "Greenhouse".
- **Already-added providers only ever get crawled, never re-added** — the
  "fully added" check (`CSVStore.FullyAddedProviders`, `Runner.FullyAdded`)
  requires every one of a provider's candidate rows to be added, not just
  one.
- **System jobs** are Board Console's own daily chores: the **dead-board
  cleanup** (`close-chronic-boards --apply`, then reindex and recount) and
  **recount companies** (`recount-companies`, then `reindex-companies`).
  Each can be paused, re-timed (15-minute grid) and run now. They live in
  `data/system.json`; a run started by hand counts as the day's run, and a
  re-timed or resumed job waits for its next time rather than firing at
  once. They never count toward the schedule load.
- **Server stats** (`/system/stats`): the host's disk, memory and CPU (from
  `/proc` and `statfs` — inside Docker these describe the host), how many
  jobs are running, the reindex disk floor (`REINDEX_MIN_FREE_GB`), and
  Docker's build cache — the usual reason a small catalogue fills the disk —
  which `/system/build-cache/prune` clears (`docker builder prune -af`,
  recorded as an activity job). Docker is reached only through the compose
  file's `docker-proxy` (tecnativa/docker-socket-proxy), which allows just
  `/system/df` and `/build`; board-console never holds the raw socket.
- **Remove provider** retires every live board of the provider through
  `add-board --retire` (rows kept, jobs untouched) and deletes its schedule.
  Once every board is retired it also purges the provider's runs from the
  activity log — in memory and in `data/activity.jsonl` — and their cached
  explanations. A removal that could not retire every board purges nothing:
  its log stays to show what failed.
- Subprocess calls run with a 30-minute timeout so a hung command can't
  block the server indefinitely, while still allowing a genuinely slow
  crawl to finish.

## Requirements

board-console's own binary has no Docker dependency — it runs
`bulk-add-boards`/`ingest`/`reindex` as plain local subprocesses inside its
own container, never via `docker exec`. `docker` (or an equivalent Compose
tool) is only needed on the host to build and run the container itself, the
same as for every other service in this repo.
