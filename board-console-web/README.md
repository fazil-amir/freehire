# board-console-web

Board Console's UI as a standalone React app (Vite + React + TypeScript + plain CSS).
It talks to Board Console only through its JSON API (`/api/v1`) and shares no code
with the repository it currently sits in, so this folder can be moved to its own
repository as it is.

## Run it (development)

1. Start Board Console (it serves the API on its port, `8040` by default).
2. Here:

   ```sh
   cp .env.example .env.local   # uncomment the VITE_* lines and adjust
   npm install
   npm run dev                  # http://localhost:5173
   ```

## Run it with Docker (production)

The image builds the app (`npm ci` + `npm run build`) and serves it with nginx. The
API's address is **not** baked into the build: the container writes it into
`/config.js` from its environment at every start, so one image serves any
environment.

```sh
cp .env.example .env         # set API_BASE_URL (and API_KEY if the API needs one)
docker compose up -d --build # http://<host>:5173, restarts on crash and on reboot
docker compose logs -f       # first line says which API it points at
```

- **Update to new code:** `git pull && docker compose up -d --build`
- **Change the API address or key:** edit `.env`, then `docker compose up -d` — a
  recreate, no rebuild.
- **Stop:** `docker compose down`

The container refuses to start without `API_BASE_URL` (its log says so), rather than
serve a page that silently calls the viewer's own `localhost`.

## Allowing the browser in (CORS)

The page calls Board Console straight from the browser, so Board Console must list
this app's address in its `BOARD_CONSOLE_CORS_ORIGINS` (a comma list), e.g.
`http://localhost:5173,http://<server>:5173`. It allows `http://localhost:5173` by
default.

## Configuration

| Variable | Where | Meaning |
|---|---|---|
| `API_BASE_URL` | Docker (`.env`) | Board Console's address **as the browser reaches it**, e.g. `http://<server>:8040` |
| `API_KEY` | Docker (`.env`) | Only when Board Console sets `BOARD_CONSOLE_API_KEY` |
| `WEB_PORT` | Docker (`.env`) | Host port the app is served on (default `5173`) |
| `VITE_API_BASE_URL` / `VITE_API_KEY` | dev (`.env.local`) | The same two, for `npm run dev` |

The key reaches the browser either way, so anyone who can open the page can read it:
it keeps out passers-by on an open port, not someone who can load the app.

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
