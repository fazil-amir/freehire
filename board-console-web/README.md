# board-console-web

Board Console's UI as a standalone React app (Vite + React + TypeScript + plain CSS).
It talks to Board Console only through its JSON API (`/api/v1`) and shares no code
with the repository it currently sits in, so this folder can be moved to its own
repository as it is.

## Run it

1. Start Board Console (it serves the API on its port, `8040` by default).
2. Here:

   ```sh
   cp .env.example .env.local   # adjust VITE_API_BASE_URL if needed
   npm install
   npm run dev                  # http://localhost:5173
   ```

Board Console allows `http://localhost:5173` by default. Any other origin must be
listed in its `BOARD_CONSOLE_CORS_ORIGINS`.

## Configuration

| Variable | Meaning |
|---|---|
| `VITE_API_BASE_URL` | Board Console's address, e.g. `http://localhost:8040` |
| `VITE_API_KEY` | Only when Board Console sets `BOARD_CONSOLE_API_KEY`. A `VITE_` variable ships to the browser, so use it locally only — in a shared deployment the API is called by a server, never with a key in the page. |

## What is here

All of Board Console's pages, with the same flow and look (`src/styles/app.css` is a
copy of its stylesheet):

- **App shell** — sidebar (nav, scope pill, the Activity pulse, the Server card with
  Clear build cache), toasts, the confirmation dialog, the build footer.
- **Catalog** — Added/All, kind tabs, search, paging; per-provider Crawl / Add + Crawl,
  Full re-crawl, Reindex now, Add/Edit/Delete schedule (the schedule modal) and
  Remove provider; the "+ New provider" modal. Refreshes every 3s while a crawl runs.
- **Schedules** — the plan table: hour scale, Load row and "now" line; scheduled
  providers (runs coloured by result, the next one ringed, later-today ones as
  outlines), the added providers without a schedule (today's hand-started crawls,
  + to schedule, Crawl now), and the system jobs (Change time…, Run now); a square
  that ran opens its log with Explain; Enabled/Paused, result and search filters in
  the URL; pause/resume toggles; the schedule modal. Refreshes every 3s while
  anything runs, 15s otherwise.
- **Activity** — Pipeline and Cleanup tabs; jobs that open into steps and steps into
  their log (live while running) with Explain; status, action and provider filters
  and paging in the URL; Recount companies, Reindex now, cleanup Preview and Run now,
  the cleanup's last/next run. Refreshes every 2s while anything runs, 5s otherwise.

No login and no server-sent events yet: the app polls.

## Scripts

- `npm run dev` — dev server on :5173
- `npm run build` — type-check and build to `dist/`
- `npm run typecheck` — type-check only
