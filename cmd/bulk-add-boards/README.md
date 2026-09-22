# combined_boards.csv + bulk-add-boards

## How combined_boards.csv was built

1. **Pulled freehire's pre-migration board catalog** from git history — commit
   `75775470` ("Board catalog moves from YAML into Postgres") is where `sources/*.yml`
   was retired in favor of the DB-backed `boards` table. Checked out `sources/*.yml`
   as it stood at the parent commit and parsed all 216 files (159 non-empty, one
   `telegram.yml` skipped — unrelated content) → **157,451 rows** across 159 providers.
   This is effectively "freehire's own production catalog, frozen at the moment it
   moved into Postgres" — everything added since then via `harvest-boards`/`add-board`
   isn't in it, but everything before is.

2. **Resolved your `companies.csv`** (80,390 rows, `ats,name,slug,url`) through
   freehire's real URL-recognition logic — `internal/ingest/atsboard.Recognize`,
   the exact function `cmd/seed-from-inventory` uses — rather than trusting the
   `ats` column your file shipped with. 47,080 of 80,390 rows (58%) resolved to a
   provider+board freehire actually supports; the other 33,149 are vanity domains
   or ATS platforms freehire has no adapter for (dropped, not guessed at).

3. **Merged the two, deduping on (provider, board)**, production catalog taking
   precedence on overlap. Your inventory contributed **20,824 genuinely new**
   provider+board pairs the production catalog didn't have.

Final: **178,275 rows across 162 providers**. Top providers by row count: bamboohr
(14,721), paylocity (12,847), join (10,417), paycom (10,029), greenhouse (9,114),
workday (8,985).

## What it is NOT

This is a **candidate list**, not validated data. Nothing in this CSV has been
probed against the live ATS platforms — unlike `harvest-boards`, which live-checks
every candidate against the real API before it's insertable. `bulk-add-boards`
(below) inserts these as `status='active'` straight away, the same way `add-board`
would for a single row — it trusts the CSV, it doesn't re-verify each board is
still live. Given the age of the pre-migration half of this data (frozen back at
the Sept 3 migration), expect some fraction of these boards to be stale/dead by now.

## bulk-add-boards_main.go

Drop this in as `cmd/bulk-add-boards/main.go` in your freehire checkout. It's
`cmd/add-board`'s exact insertion path (`boardcatalog.Validate` +
`boardcatalog.NewInserter(...).Insert(..., StatusActive)`) — same validation, same
DB writes, same `active` status a curator addition gets — just looped over a CSV
in one process instead of one `go run` per row, which would take far too long at
this scale (178k separate Go-toolchain + DB-connect cycles).

```bash
cp bulk-add-boards_main.go freehire/cmd/bulk-add-boards/main.go
cd freehire

# 1. Dry run first — reports valid/invalid counts, writes nothing
go run ./cmd/bulk-add-boards -in combined_boards.csv

# 2. Try one small provider first to sanity-check against your DB
go run ./cmd/bulk-add-boards -in combined_boards.csv --apply -provider=ashby

# 3. Full run
DATABASE_URL="postgres://hire:hire@localhost:5432/hire?sslmode=disable" \
  go run ./cmd/bulk-add-boards -in combined_boards.csv --apply
```

Flags:
- `-in` (required) — the CSV, `provider,board,company` columns (extra columns ignored)
- `--apply` — actually writes; omit for a dry-run report only
- `-provider=X` — restrict the run to one provider, useful for testing
- `-log-every=N` — progress log interval (default 500 rows)

It does **not** call `cmd/ingest` or crawl anything — it only inserts catalog rows,
same as `add-board`. Fetching the actual jobs for these boards is a separate step
you run afterward with `cmd/ingest <provider>`.
