import { Fragment, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import type { Job } from "../api/types";
import { useAppData } from "../components/AppData";
import { useConfirm } from "../components/Confirm";
import { LocalTime } from "../components/LocalTime";
import { PageFooter } from "../components/PageFooter";
import { RunLog } from "../components/RunLog";
import { useToast } from "../components/Toasts";
import { useDebounced } from "../hooks/useDebounced";
import { usePoll } from "../hooks/usePoll";

// Activity: one row per job (a crawl with its reindex, a recount, …), newest
// first; click a job for its steps, a step for its log. Pipeline and Cleanup
// are two tabs over the same shape, so switching never moves the table. The
// filters live in the URL. Polled every 2s while anything runs and every 5s
// otherwise — a job a schedule or another tab starts appears within seconds —
// with open jobs and logs kept open.

const ACTIONS = ["add-boards", "ingest", "reindex", "recount-companies", "reindex-companies", "remove-boards", "prune-build-cache"];

export function ActivityPage() {
  const { reloadStats } = useAppData();
  const confirm = useConfirm();
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const cleanupView = params.get("view") === "cleanup";
  const status = params.get("status") ?? "";
  const action = params.get("action") ?? "";
  const provider = params.get("provider") ?? "";

  function update(patch: Record<string, string>) {
    const next = new URLSearchParams(params);
    Object.entries(patch).forEach(([k, v]) => (v ? next.set(k, v) : next.delete(k)));
    setParams(next, { replace: true });
  }
  const switchTab = (cleanup: boolean) => setParams(cleanup ? new URLSearchParams({ view: "cleanup" }) : new URLSearchParams(), { replace: true });

  // The provider filter follows typing once it settles.
  const [search, setSearch] = useState(provider);
  useEffect(() => setSearch(provider), [provider]);
  const settled = useDebounced(search, 300);
  useEffect(() => {
    if (settled !== provider) update({ provider: settled, page: "" });
  }, [settled]);

  const [running, setRunning] = useState(false);
  const key = params.toString();
  const page = usePoll(() => api.activity(params), running ? 2000 : 5000, [key]);
  const data = page.data;
  useEffect(() => setRunning(!!data?.running), [data]);

  const [openJobs, setOpenJobs] = useState<Set<string>>(new Set());
  const [openLogs, setOpenLogs] = useState<Set<string>>(new Set());
  const toggleSet = (set: Set<string>, k: string) => { const n = new Set(set); if (n.has(k)) n.delete(k); else n.add(k); return n; };
  function toggleJob(j: Job) {
    const opening = !openJobs.has(j.key);
    setOpenJobs(toggleSet(openJobs, j.key));
    // Closing a job also closes its steps' open logs.
    if (!opening) setOpenLogs((l) => new Set([...l].filter((k) => !k.startsWith(j.key + "-"))));
  }

  async function act(run: () => Promise<unknown>, done: string) {
    try {
      await run();
      toast(done);
    } catch (e) {
      toast((e as Error).message, true);
    }
    void page.reload();
    void reloadStats();
  }
  async function runCleanup() {
    const ok = await confirm(
      "Closes the open jobs of every dead board, then recounts companies. Closed jobs drop out of search; nothing is deleted, and a board that comes back reopens them.",
      { title: "Run the dead board cleanup now?", confirmLabel: "Run now" });
    if (ok) await act(api.cleanupRun, "Dead board cleanup started");
  }

  const c = data?.cleanup;
  return (
    <main>
      <div className="page-header">
        <div>
          <h1>Activity</h1>
          {cleanupView
            ? <p className="page-subtitle" title="Closed jobs drop out of search; nothing is deleted, and a board that comes back reopens its jobs on the next crawl. Company job counts and company search are refreshed after each run.">
                Runs daily. Closes jobs from boards dead for 60 days or empty for 30, then recounts companies.</p>
            : <p className="page-subtitle">One row per job — a crawl with its reindex, a recount, … — newest first. Click a job for its steps.</p>}
        </div>
        <div className="page-actions">
          {cleanupView ? (
            <>
              <button type="button" className="btn btn-secondary" onClick={() => act(api.cleanupPreview, "Preview started — it changes nothing; open its row below for the report")}>Preview</button>
              <button type="button" className="btn btn-primary" onClick={() => void runCleanup()}>Run now</button>
            </>
          ) : (
            <>
              <button type="button" className="btn btn-secondary" title="Recompute every company's open-job count and facets, then rebuild company search"
                onClick={() => act(api.recountCompanies, "Company recount started — job counts, then company search")}>Recount companies</button>
              <button type="button" className="btn btn-primary" onClick={() => act(api.reindex, "Reindex started — rebuilding the search index from everything stored")}>Reindex now</button>
            </>
          )}
        </div>
      </div>

      {page.error && <p className="banner-warning">{page.error.message}</p>}

      <div className="table-card">
        <div className="toolbar toolbar-split">
          <div className="segmented" role="group" aria-label="View">
            <button type="button" className={cleanupView ? "" : "active"} onClick={() => switchTab(false)}>Pipeline</button>
            <button type="button" className={cleanupView ? "active" : ""} onClick={() => switchTab(true)}>Cleanup</button>
          </div>
          <div className="toolbar-filters">
            <select value={status} onChange={(e) => update({ status: e.target.value, page: "" })}>
              <option value="">All statuses</option>
              <option value="running">Running</option>
              <option value="success">Success</option>
              <option value="partial">Partial</option>
              <option value="failed">Failed</option>
            </select>
            {!cleanupView && (
              <select value={action} onChange={(e) => update({ action: e.target.value, page: "" })}>
                <option value="">All actions</option>
                {ACTIONS.map((a) => <option key={a} value={a}>{a}</option>)}
              </select>
            )}
          </div>
          {cleanupView ? (
            // Cleanup runs carry no provider, so this slot holds its schedule.
            <div className="toolbar-end cleanup-status">
              <span className="muted">Last</span>
              {c?.lastRun ? <><span className={"badge badge-" + c.lastStatus}>{c.lastStatus}</span><LocalTime iso={c.lastRun} /></> : <span className="muted">never</span>}
              <span className="cleanup-sep" />
              <span className="muted">Next</span>
              {c?.paused ? <span className="badge badge-no">paused</span>
                : c?.dueNow ? <span className="badge badge-running">due now</span>
                : c?.nextRun ? <LocalTime iso={c.nextRun} /> : null}
            </div>
          ) : (
            <div className="toolbar-end">
              <input type="search" value={search} placeholder="Filter by provider…" autoComplete="off" onChange={(e) => setSearch(e.target.value)} />
            </div>
          )}
        </div>

        <table className="catalog-table" id="activity-table">
          <colgroup>
            <col style={{ width: "20%" }} /><col style={{ width: "20%" }} /><col style={{ width: "30%" }} /><col style={{ width: "10%" }} /><col style={{ width: "20%" }} />
          </colgroup>
          <thead>
            <tr><th>Provider</th><th>Job</th><th>Status</th><th>Took</th><th>Started</th></tr>
          </thead>
          <tbody>
            {!data && <tr><td colSpan={5} className="muted">{page.error ? "Could not load the activity." : "Loading…"}</td></tr>}
            {data?.jobs.map((j) => {
              const open = openJobs.has(j.key);
              return (
                <Fragment key={j.key}>
                  <tr className={"job-row" + (open ? " open" : "")} onClick={() => toggleJob(j)}>
                    <td><span className="job-chevron" aria-hidden="true">›</span>{j.provider || <span className="muted">—</span>}</td>
                    <td>
                      {j.kind}
                      <div className="cell-sub step-pills">{j.actions.map((a) => <span key={a} className={"pill pill-sm pill-" + a}>{a}</span>)}</div>
                    </td>
                    <td>
                      <span className={"badge badge-" + j.status}>{j.status}</span>
                      <div className="cell-sub">{j.summary}</div>
                    </td>
                    <td className="job-duration">{j.duration}</td>
                    <td><LocalTime iso={j.startedAt} /></td>
                  </tr>
                  {open && j.steps.map((s) => {
                    const dom = `${j.key}-${s.id}`;
                    const logOpen = openLogs.has(dom);
                    return (
                      <Fragment key={dom}>
                        <tr className="run-row step-row" onClick={() => setOpenLogs(toggleSet(openLogs, dom))}>
                          <td className="step-cell"><span className="step-branch" aria-hidden="true">└</span><span className={"pill pill-" + s.action}>{s.action}</span>{s.label && <> <span className="pill">{s.label}</span></>}</td>
                          <td className="cell-sub">{s.sharedWith && `shared with ${s.sharedWith}`}</td>
                          <td>
                            <span className={"badge badge-" + s.status}>{s.status}</span>
                            <div className="cell-sub">{s.summary}</div>
                          </td>
                          <td className="duration-cell">{s.duration}</td>
                          <td><LocalTime iso={s.startedAt} /></td>
                        </tr>
                        {logOpen && (
                          <tr className="run-detail">
                            <td colSpan={5}><RunLog runId={s.id} showHead={false} /></td>
                          </tr>
                        )}
                      </Fragment>
                    );
                  })}
                </Fragment>
              );
            })}
            {data && data.jobs.length === 0 && (
              <tr><td colSpan={5} className="muted">{cleanupView ? "No cleanup runs yet — try Preview." : "No runs yet."}</td></tr>
            )}
          </tbody>
        </table>

        {data && (
          <div className="table-footer">
            {data.page > 1 && <button type="button" className="page-link" onClick={() => update({ page: String(data.page - 1) })}>← Prev</button>}
            <span className="muted">Page {data.page} / {data.totalPages} · {data.total} jobs</span>
            {data.page < data.totalPages && <button type="button" className="page-link" onClick={() => update({ page: String(data.page + 1) })}>Next →</button>}
          </div>
        )}
      </div>
      <PageFooter />
    </main>
  );
}
