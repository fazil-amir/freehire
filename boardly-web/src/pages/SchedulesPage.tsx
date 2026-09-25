import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import type { LastCrawl, ScheduleRow, SchedulesPage as PageData, SlotRun, SystemRow, UnscheduledRow } from "../api/types";
import { useConfirm } from "../components/Confirm";
import { Dialog, DialogHeader } from "../components/Dialog";
import { KebabMenu } from "../components/KebabMenu";
import { LocalTime } from "../components/LocalTime";
import { PageFooter } from "../components/PageFooter";
import { RunLog } from "../components/RunLog";
import { ScheduleModal, type ScheduleTarget } from "../components/ScheduleModal";
import { useToast } from "../components/Toasts";
import { useAppData } from "../components/AppData";
import { usePoll } from "../hooks/usePoll";
import { fmtHM, toLocalMin, toUTCMin, zoneName } from "../lib/time";

// The Schedules plan: one table whose rows are the scheduled providers,
// then the added providers without a schedule, then Boardly's own
// daily jobs — each row's runs drawn on a 24-hour track in the viewer's
// timezone. Every mark sits on the moment it stands for (an hour label on
// its gridline, a run's square centred on its start, the "now" line); each
// square is coloured by how its latest run went, a later-today one is only
// an outline, and a square that ran opens that run's log under its row.

const DAY = 1440;
const pct = (min: number) => (min / DAY) * 100 + "%";
const STATUS: Record<string, string> = { success: "success", partial: "partial", failed: "failed", running: "running", pending: "not run yet", none: "no run" };

function ago(iso: string) {
  const mins = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  return mins < 60 ? `${mins} min ago` : `${Math.round(mins / 60)} h ago`;
}
const nowMinutes = () => { const d = new Date(); return d.getHours() * 60 + d.getMinutes(); };

type Panel = { runId: number; label: string };
type Kind = "scheduled" | "system" | "adhoc";

/** A row's squares. adhoc: a provider without a schedule — only today's hand-started crawls are drawn. */
function Track({ name, slots, nextRun, kind, nowMin, selected, onOpen }: {
  name: string; slots: SlotRun[]; nextRun: string | null; kind: Kind; nowMin: number;
  selected: number | null; onOpen: (runId: number, label: string) => void;
}) {
  const next = nextRun ? new Date(nextRun) : null;
  const nextUTC = next ? next.getUTCHours() * 60 + next.getUTCMinutes() : -1;
  const today = new Date().toDateString();
  const base = "tl-block" + (kind === "system" ? " sys" : "");
  return (
    <div className="tl-track">
      {slots.map((x, i) => {
        if (kind === "adhoc" && !(x.at && new Date(x.at).toDateString() === today)) return null;
        const isNext = x.m === nextUTC;
        const left = pct(toLocalMin(x.m));
        const hm = fmtHM(toLocalMin(x.m));
        // Later today it has not run yet: its latest run was yesterday's.
        if (kind !== "adhoc" && toLocalMin(x.m) > nowMin) {
          return <div key={i} className={base + " run-upcoming" + (isNext ? " next" : "")} style={{ left }}
            title={`${name} · ${hm} — later today${isNext ? " (next run)" : ""}`} />;
        }
        const label = `${name} · ${hm} — ${STATUS[x.s]}${x.at ? ` (${ago(x.at)})` : ""}${x.sum ? ` · ${x.sum}` : ""}${isNext ? "\nNext run" : ""}`;
        const cls = base + " run-" + x.s + (isNext ? " next" : "") + (x.run && x.run === selected ? " selected" : "");
        return x.run
          ? <div key={i} className={cls} style={{ left }} data-run={x.run} title={label + "\nClick for its log"} onClick={() => onOpen(x.run!, `${name} · ${hm}`)} />
          : <div key={i} className={cls} style={{ left }} title={label} />;
      })}
    </div>
  );
}

/** The provider column's last result: the same square the timeline uses, then when. */
function LastCell({ dot, title, text, at }: { dot: string; title?: string; text?: string; at: string | null }) {
  return (
    <div className="plan-last">
      {/* The explanation goes on the words when there are any (queued), else on the square. */}
      <span className={"status-dot run-" + dot} title={text ? undefined : title} />
      {text && <span title={title}>{text}</span>}
      {at && <LocalTime iso={at} short />}
    </div>
  );
}
const lastTitle = (l: LastCrawl) => `Last run: ${l.status}${l.summary ? ` · ${l.summary}` : ""}`;

function Toggle({ on, label, onClick }: { on: boolean; label: string; onClick: () => void }) {
  return (
    <button type="button" className={"toggle-switch" + (on ? " on" : "")} aria-label={label} onClick={onClick}>
      <span className="toggle-knob" />
    </button>
  );
}

function SlotPanel({ id, panel, onClose }: { id: string; panel: Panel; onClose: () => void }) {
  return (
    <tr className="run-detail slot-detail" id={id}>
      <td colSpan={4}>
        <div className="slot-log-bar">
          <span className="slot-log-title">{panel.label}</span>
          <button type="button" className="slot-log-close" aria-label="Close this log" onClick={onClose}>✕</button>
        </div>
        <RunLog runId={panel.runId} />
      </td>
    </tr>
  );
}

export function SchedulesPage() {
  const { reloadStats } = useAppData();
  const confirm = useConfirm();
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const state = params.get("state") ?? "";
  const status = params.get("status") ?? "";
  const q = params.get("q") ?? "";
  const query = q.trim().toLowerCase();
  const setFilter = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v); else next.delete(k);
    setParams(next, { replace: true });
  };

  const [anyRunning, setAnyRunning] = useState(false);
  const page = usePoll(api.schedules, anyRunning ? 3000 : 15000);
  const data: PageData | null = page.data;
  useEffect(() => {
    setAnyRunning(!!data && (data.scheduled.some((r) => r.running) || data.unscheduled.some((r) => r.crawling) || data.system.some((r) => r.running)));
  }, [data]);

  // The "now" line moves with the clock.
  const [nowMin, setNowMin] = useState(nowMinutes);
  useEffect(() => { const t = setInterval(() => setNowMin(nowMinutes()), 60000); return () => clearInterval(t); }, []);

  const [panels, setPanels] = useState<Record<string, Panel>>({});
  const openPanel = (key: string, runId: number, label: string) =>
    setPanels((p) => (p[key]?.runId === runId ? omit(p, key) : { ...p, [key]: { runId, label } }));
  const closePanel = (key: string) => setPanels((p) => omit(p, key));

  const [scheduleTarget, setScheduleTarget] = useState<ScheduleTarget | null>(null);
  const [retime, setRetime] = useState<SystemRow | null>(null);

  function reload() { void page.reload(); void reloadStats(); }
  async function act(run: () => Promise<unknown>, done?: string) {
    try {
      await run();
      if (done) toast(done);
    } catch (e) {
      toast((e as Error).message, true);
    }
    reload();
  }

  // Rows by their first run of the viewer's day.
  const scheduled = useMemo(() => {
    const first = (r: ScheduleRow) => Math.min(...r.times.map(toLocalMin), DAY);
    return [...(data?.scheduled ?? [])].sort((a, b) => first(a) - first(b) || (a.provider < b.provider ? -1 : 1));
  }, [data]);

  // The toolbar's filters: Enabled/Paused is a schedule's state (a provider
  // without one is neither, so it shows only under All); the result filter
  // reads the last run; search matches the name.
  const lastOf = (at: string | null, s: string) => (at ? s : "never");
  const matches = (name: string, enabled: boolean | null, last: string) =>
    (enabled === null ? !state : !state || (state === "enabled") === enabled) &&
    (!query || name.toLowerCase().includes(query)) && (!status || last === status);
  const shownScheduled = scheduled.filter((r) => matches(r.provider, r.enabled, lastOf(r.last.at, r.last.status)));
  const shownAdhoc = (data?.unscheduled ?? []).filter((r) => matches(r.provider, null, lastOf(r.last.at, r.last.status)));
  const shownSystem = (data?.system ?? []).filter((r) => matches(r.name, r.enabled, lastOf(r.lastRun, r.lastStatus)));

  // A row the filters hide takes its open log with it.
  useEffect(() => {
    const visible = new Set([
      ...shownScheduled.map((r) => "sched-" + r.id), ...shownAdhoc.map((r) => "adhoc-" + r.provider), ...shownSystem.map((r) => "sys-" + r.key),
    ]);
    setPanels((p) => Object.fromEntries(Object.entries(p).filter(([k]) => visible.has(k))));
  }, [state, status, q, data]);

  const known = useMemo(() => Object.fromEntries((data?.scheduled ?? []).map((r) => [r.provider, { id: r.id, times: r.times }])), [data]);
  const enabledRows = (data?.scheduled ?? []).filter((r) => r.enabled);
  const perDay = enabledRows.reduce((n, r) => n + r.times.length, 0);
  const peak = Math.max(0, ...(data?.load ?? [0]));
  const capacity = data?.capacity ?? 0;

  async function deleteSchedule(r: ScheduleRow) {
    const ok = await confirm(`${r.provider} will no longer be crawled automatically. Its jobs and past runs stay; you can add a schedule again any time.`,
      { title: "Delete schedule?", confirmLabel: "Delete", danger: true });
    if (ok) await act(() => api.deleteSchedule(r.id), `Schedule for ${r.provider} deleted`);
  }
  async function runSystem(r: SystemRow) {
    const ok = await confirm(`${r.description} It counts as today's run.`, { title: `Run ${r.name} now?`, confirmLabel: "Run now" });
    if (ok) await act(() => api.runSystemJob(r.key), `${r.name} started — see Activity for progress`);
  }

  const withPanel = (key: string, row: ReactNode) => (
    <Fragment key={key}>
      {row}
      {panels[key] && <SlotPanel id={"detail-" + key} panel={panels[key]} onClose={() => closePanel(key)} />}
    </Fragment>
  );
  const planCell = (track: ReactNode) => <td className="plan-cell"><span className="plan-now" aria-hidden="true" />{track}</td>;

  return (
    <main>
      <div className="page-header">
        <div>
          <h1>Schedules</h1>
          <p className="page-subtitle">Crawls run at the times you set. A time holding {capacity || "…"} runs is booked — extra runs wait for a free spot.</p>
        </div>
        <div className="page-actions">
          <button type="button" className="btn btn-primary" onClick={() => setScheduleTarget({})}>+ Add schedule</button>
        </div>
      </div>

      {data?.dbError && <p className="banner-warning">{data.dbError}</p>}
      {page.error && <p className="banner-warning">{page.error.message}</p>}

      <div className="table-card plan-card">
        <div className="toolbar toolbar-split">
          <div className="segmented" role="group" aria-label="Show">
            {[["", "All"], ["enabled", "Enabled"], ["paused", "Paused"]].map(([v, l]) => (
              <button key={v} type="button" className={state === v ? "active" : ""} onClick={() => setFilter("state", v)}>{l}</button>
            ))}
          </div>
          <div className="tabs" role="group" aria-label="Last run">
            {[["", "Any result"], ["success", "Success"], ["partial", "Partial"], ["failed", "Failed"]].map(([v, l]) => (
              <button key={v} type="button" className={status === v ? "active" : ""} onClick={() => setFilter("status", v)}>{l}</button>
            ))}
          </div>
          <div className="toolbar-end">
            <input type="search" placeholder="Search provider…" autoComplete="off" aria-label="Search provider" value={q} onChange={(e) => setFilter("q", e.target.value)} />
          </div>
        </div>

        <table className="catalog-table plan-table" style={{ ["--now" as string]: String(nowMin / DAY) }}>
          <colgroup>
            <col className="plan-col-provider" /><col /><col className="plan-col-toggle" /><col className="plan-col-actions" />
          </colgroup>
          <thead>
            <tr>
              <th>Provider</th>
              <th className="plan-cell plan-scale-cell">
                <div className="tl-track plan-scale">
                  {Array.from({ length: 25 }, (_, h) => (
                    <span key={h} className={"tl-tick" + (h === 24 ? " end" : h % 3 ? " minor" : "")} style={{ left: pct(h * 60) }}>{String(h).padStart(2, "0")}:00</span>
                  ))}
                </div>
              </th>
              <th className="col-toggle">Enabled</th>
              <th className="col-actions"></th>
            </tr>
            {data && data.scheduled.length > 0 && (
              <tr className="plan-load">
                <th title="How many scheduled crawls start in each 15-minute slot">Load</th>
                <th className="plan-cell">
                  <span className="plan-now" aria-hidden="true" />
                  <div className="tl-track">
                    {data.load.map((n, i) => {
                      if (!n) return null;
                      const hm = fmtHM(toLocalMin(i * 15));
                      return <div key={i} className={"tl-slot" + (n > capacity ? " over" : "")}
                        style={{ left: pct(toLocalMin(i * 15)), opacity: n < capacity ? 0.35 + (0.65 * n) / capacity : undefined }}
                        title={`${n} crawl${n > 1 ? "s" : ""} at ${hm}${n > capacity ? ` — ${n - capacity} will queue` : n >= capacity ? " — booked" : ""}`} />;
                    })}
                  </div>
                </th>
                <th></th><th></th>
              </tr>
            )}
          </thead>

          <tbody>
            {!data && <tr><td colSpan={4} className="muted">{page.error ? "Could not load the schedules." : "Loading…"}</td></tr>}
            {data && data.scheduled.length === 0 && <tr><td colSpan={4} className="muted">No schedules yet — add one and its runs appear here on the day's timeline.</td></tr>}
            {shownScheduled.map((r) => {
              const key = "sched-" + r.id;
              return withPanel(key, (
                <tr className={"plan-row" + (r.enabled ? "" : " paused")}>
                  <td className="plan-provider">
                    <div className="plan-name" title={r.provider}>{r.provider} <span className="tl-per">{r.times.length}×</span></div>
                    {r.queued ? <LastCell dot="pending" text="queued" title={`${capacity} crawls are already running — it starts when one ends`} at={r.last.at} />
                      : r.dueNow ? <LastCell dot="pending" text="due now" at={r.last.at} />
                      : !r.last.at ? <LastCell dot="none" text="never run" at={null} />
                      : <LastCell dot={r.last.badge} title={lastTitle(r.last)} at={r.last.at} />}
                  </td>
                  {planCell(<Track name={r.provider} slots={r.slots} nextRun={r.nextRun} kind="scheduled" nowMin={nowMin}
                    selected={panels[key]?.runId ?? null} onOpen={(id, l) => openPanel(key, id, l)} />)}
                  <td className="col-toggle">
                    <Toggle on={r.enabled} label={`${r.enabled ? "Pause" : "Resume"} ${r.provider}'s schedule`} onClick={() => act(() => api.toggleSchedule(r.id))} />
                  </td>
                  <td className="col-actions">
                    <KebabMenu label={`Actions for ${r.provider}`}>
                      {(close) => (
                        <>
                          <button type="button" className="kebab-item" onClick={() => { close(); setScheduleTarget({ provider: r.provider, id: r.id, times: r.times }); }}>Edit schedule</button>
                          <button type="button" className="kebab-item danger" onClick={() => { close(); void deleteSchedule(r); }}>Delete schedule</button>
                        </>
                      )}
                    </KebabMenu>
                  </td>
                </tr>
              ));
            })}
            {data && data.scheduled.length > 0 && shownScheduled.length === 0 && (
              <tr className="plan-nomatch"><td colSpan={4} className="muted">No schedule matches these filters.</td></tr>
            )}
          </tbody>

          {data && data.unscheduled.length > 0 && (
            <tbody className="plan-unscheduled">
              {shownAdhoc.length > 0 && (
                <tr className="plan-group">
                  <td colSpan={4}><span className="tl-tag tl-tag-neutral">Not scheduled</span><span className="plan-group-note">Added providers — crawled only by hand</span></td>
                </tr>
              )}
              {shownAdhoc.map((r: UnscheduledRow) => {
                const key = "adhoc-" + r.provider;
                return withPanel(key, (
                  <tr className="plan-row adhoc-row">
                    <td className="plan-provider">
                      <div className="plan-name" title={r.provider}>{r.provider}</div>
                      {r.last.status === "running" ? <LastCell dot="running" text="crawling" at={r.last.at} />
                        : !r.last.at ? <LastCell dot="none" text="never crawled" at={null} />
                        : <LastCell dot={r.last.badge} title={`Last crawl: ${r.last.status}${r.last.summary ? ` · ${r.last.summary}` : ""}`} at={r.last.at} />}
                    </td>
                    {planCell(<Track name={r.provider} slots={r.runs} nextRun={null} kind="adhoc" nowMin={nowMin}
                      selected={panels[key]?.runId ?? null} onOpen={(id, l) => openPanel(key, id, l)} />)}
                    <td className="col-toggle">
                      <button type="button" className="btn btn-secondary btn-sm plan-add" title={`Add a schedule for ${r.provider}`}
                        aria-label={`Add a schedule for ${r.provider}`} onClick={() => setScheduleTarget({ provider: r.provider })}>+</button>
                    </td>
                    <td className="col-actions">
                      <KebabMenu label={`Actions for ${r.provider}`}>
                        {(close) => (
                          <>
                            <button type="button" className="kebab-item" onClick={() => { close(); setScheduleTarget({ provider: r.provider }); }}>Add schedule</button>
                            <button type="button" className="kebab-item" onClick={() => { close(); void act(() => api.crawl(r.provider), `Crawl started for ${r.provider}`); }}>Crawl now</button>
                            <Link className="kebab-item" to={`/activity?provider=${encodeURIComponent(r.provider)}`} onClick={close}>View runs in Activity</Link>
                          </>
                        )}
                      </KebabMenu>
                    </td>
                  </tr>
                ));
              })}
            </tbody>
          )}

          {data && data.system.length > 0 && (
            <tbody className="plan-system">
              {shownSystem.length > 0 && (
                <tr className="plan-group">
                  <td colSpan={4}><span className="tl-tag">System</span><span className="plan-group-note">Boardly's own daily jobs — not a provider's crawls</span></td>
                </tr>
              )}
              {shownSystem.map((r) => {
                const key = "sys-" + r.key;
                return withPanel(key, (
                  <tr className={"plan-row system-row" + (r.enabled ? "" : " paused")}>
                    <td className="plan-provider">
                      <div className="plan-name" title={r.name}><span className="sys-icon" aria-hidden="true">⚙</span>{r.name}</div>
                      {r.running ? <LastCell dot="running" text="running" at={r.lastRun} />
                        : !r.lastRun ? <LastCell dot="none" text="never run" at={null} />
                        : <LastCell dot={r.lastStatus} title={`Last run: ${r.lastStatus}`} at={r.lastRun} />}
                    </td>
                    {planCell(<Track name={r.name} slots={r.slot} nextRun={r.nextRun} kind="system" nowMin={nowMin}
                      selected={panels[key]?.runId ?? null} onOpen={(id, l) => openPanel(key, id, l)} />)}
                    <td className="col-toggle">
                      <Toggle on={r.enabled} label={`${r.enabled ? "Pause" : "Resume"} ${r.name}`} onClick={() => act(() => api.toggleSystemJob(r.key))} />
                    </td>
                    <td className="col-actions">
                      <KebabMenu label={`Actions for ${r.name}`}>
                        {(close) => (
                          <>
                            <button type="button" className="kebab-item" onClick={() => { close(); setRetime(r); }}>Change time…</button>
                            <button type="button" className="kebab-item" onClick={() => { close(); void runSystem(r); }}>Run now</button>
                            <Link className="kebab-item" to={r.activityUrl} onClick={close}>View runs in Activity</Link>
                          </>
                        )}
                      </KebabMenu>
                    </td>
                  </tr>
                ));
              })}
            </tbody>
          )}
        </table>

        {data && <div className="plan-foot">
          <div className="plan-foot-start">
            <span className={"plan-summary" + (peak > capacity ? " over" : "")}>
              {enabledRows.length} schedule{enabledRows.length === 1 ? "" : "s"} · {perDay} crawls a day · busiest slot {peak} of {capacity}
            </span>
          </div>
          <div className="timeline-legend">
            <span className="legend-group">Runs</span>
            <span><i className="lg lg-success" />success</span>
            <span><i className="lg lg-partial" />partial</span>
            <span><i className="lg lg-failed" />failed</span>
            <span><i className="lg lg-none" />no run</span>
            <span><i className="lg lg-upcoming" />later today</span>
            <span><i className="lg lg-next" />next</span>
            <span className="legend-group">Load</span>
            <span><i className="lg lg-1" />1</span>
            <span><i className="lg lg-2" />{capacity} booked</span>
            <span><i className="lg lg-over" />over</span>
          </div>
        </div>}
      </div>

      <ScheduleModal
        target={scheduleTarget}
        providers={data?.addedProviders ?? []}
        known={known}
        onClose={() => setScheduleTarget(null)}
        onSaved={(provider) => { setScheduleTarget(null); toast("Schedule saved for " + provider); reload(); }}
      />
      <SystemTimeDialog job={retime} onClose={() => setRetime(null)} onSaved={() => { setRetime(null); toast("Time saved"); reload(); }} />
      <PageFooter />
    </main>
  );
}

function omit<T>(o: Record<string, T>, key: string): Record<string, T> {
  const { [key]: _gone, ...rest } = o;
  void _gone;
  return rest;
}

/** "Change time…" on a system job: its one daily time, in the viewer's timezone. */
function SystemTimeDialog({ job, onClose, onSaved }: { job: SystemRow | null; onClose: () => void; onSaved: () => void }) {
  const [h, setH] = useState("");
  const [m, setM] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!job) return;
    const local = toLocalMin(job.minute);
    setH(String(Math.floor(local / 60))); setM(String(local % 60)); setError("");
  }, [job]);
  const zone = zoneName();

  async function save() {
    if (!job) return;
    if (h === "" || m === "") { setError("Pick the hour and the minutes."); return; }
    setSaving(true);
    try {
      await api.setSystemJobTime(job.key, toUTCMin(Number(h) * 60 + Number(m)));
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={!!job} onClose={onClose} className="dialog-sm">
      {job && (
        <form noValidate onSubmit={(e) => { e.preventDefault(); void save(); }}>
          <DialogHeader title={`Change time — ${job.name}`} subtitle="Runs once a day at this time, in your timezone." onClose={onClose} />
          <div className="dialog-body">
            {error && <p className="field-error">{error}</p>}
            <div className="schedule-times">
              <div className="schedule-times-head">
                <span className="field-label">Run at</span>
                <span className="field-hint">{zone ? `time in ${zone}` : "in your timezone"}</span>
              </div>
              <ol className="time-rows">
                <li className="time-row">
                  <span className="time-index" aria-hidden="true">1</span>
                  <select aria-label="Hour" value={h} onChange={(e) => setH(e.target.value)}>
                    <option value="">HH</option>
                    {Array.from({ length: 24 }, (_, i) => <option key={i} value={i}>{String(i).padStart(2, "0")}</option>)}
                  </select>
                  <span className="time-sep">:</span>
                  <select aria-label="Minute" value={m} onChange={(e) => setM(e.target.value)}>
                    <option value="">MM</option>
                    {[0, 15, 30, 45].map((x) => <option key={x} value={x}>{String(x).padStart(2, "0")}</option>)}
                  </select>
                </li>
              </ol>
              <p className="field-hint">It first runs at the new time's next occurrence, not straight away.</p>
            </div>
          </div>
          <div className="dialog-actions">
            <button type="button" className="btn btn-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn btn-primary" disabled={saving}>Save time</button>
          </div>
        </form>
      )}
    </Dialog>
  );
}
