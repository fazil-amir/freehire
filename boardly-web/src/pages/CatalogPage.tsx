import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import type { Provider } from "../api/types";
import { useAppData } from "../components/AppData";
import { useConfirm } from "../components/Confirm";
import { KebabMenu } from "../components/KebabMenu";
import { LocalTime } from "../components/LocalTime";
import { NewProviderModal } from "../components/NewProviderModal";
import { PageFooter } from "../components/PageFooter";
import { ScheduleModal, type ScheduleTarget } from "../components/ScheduleModal";
import { useToast } from "../components/Toasts";
import { useDebounced } from "../hooks/useDebounced";
import { usePoll } from "../hooks/usePoll";

// The Catalog: every provider Boardly knows, filtered by Added/All,
// kind and a search (all kept in the URL), with each row's schedule and
// last run, and its actions in a ⋮ menu. While any row is crawling the table
// refreshes every 3s, and after every action.

const fullyAdded = (p: Provider) => p.addedCount === p.companyCount;

function AddedBadge({ p }: { p: Provider }) {
  const cls = fullyAdded(p) ? "badge-yes" : p.addedCount > 0 ? "badge-partial" : "badge-no";
  return <span className={"badge " + cls}>{p.addedCount}/{p.companyCount}</span>;
}

function ScheduleCell({ p }: { p: Provider }) {
  const s = p.schedule;
  if (!s) return <span className="muted">—</span>;
  return (
    <>
      {s.times.length}× a day
      {!s.enabled ? <span className="badge badge-no">paused</span>
        : s.dueNow ? <div className="cell-sub">next: due now</div>
        : s.nextRun && <div className="cell-sub">next: <LocalTime iso={s.nextRun} /></div>}
    </>
  );
}

function LastRunCell({ p }: { p: Provider }) {
  if (p.crawling) return <span className="badge badge-running">crawling</span>;
  if (!p.lastRun) return <span className="muted">never</span>;
  return (
    <>
      <span className={"badge badge-" + p.lastRun.status} title={p.lastRun.summary || undefined}>{p.lastRun.status}</span>
      <div className="cell-sub"><LocalTime iso={p.lastRun.at} /></div>
    </>
  );
}

export function CatalogPage() {
  const { meta, reloadStats } = useAppData();
  const confirm = useConfirm();
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const show = params.get("show") === "all" ? "all" : "";
  const kind = params.get("kind") ?? "";
  const q = params.get("q") ?? "";
  const page = Number(params.get("page")) || 1;

  // The search box updates the URL once typing settles; the table follows it.
  const [search, setSearch] = useState(q);
  useEffect(() => setSearch(q), [q]); // the URL changed from elsewhere (the sidebar link, back)
  const settled = useDebounced(search, 250);
  useEffect(() => {
    if (settled === q) return;
    update({ q: settled, page: "" });
  }, [settled]);

  function update(patch: Record<string, string>) {
    const next = new URLSearchParams(params);
    Object.entries(patch).forEach(([k, v]) => (v ? next.set(k, v) : next.delete(k)));
    setParams(next, { replace: true });
  }

  const [crawlingAny, setCrawlingAny] = useState(false);
  const catalog = usePoll(() => api.catalog({ show, kind, q, page }), crawlingAny ? 3000 : 0, [show, kind, q, page]);
  const data = catalog.data;
  useEffect(() => setCrawlingAny(!!data?.providers.some((p) => p.crawling)), [data]);

  const known = useMemo(() => {
    const m: Record<string, { id: string; times: number[] }> = {};
    data?.providers.forEach((p) => { if (p.schedule) m[p.provider] = { id: p.schedule.id, times: p.schedule.times }; });
    return m;
  }, [data]);

  const [scheduleTarget, setScheduleTarget] = useState<ScheduleTarget | null>(null);
  const [newProviderOpen, setNewProviderOpen] = useState(false);

  function refresh() {
    void catalog.reload();
    void reloadStats();
  }

  // Runs an action and reports it: a toast either way, then a refresh.
  async function act(run: () => Promise<unknown>, done: string) {
    try {
      await run();
      toast(done);
    } catch (e) {
      toast((e as Error).message, true);
    }
    refresh();
  }

  async function crawl(p: Provider) {
    if (fullyAdded(p) && p.recentCrawl) {
      const ok = await confirm(
        `${p.provider} was crawled ${p.recentCrawl}${p.schedule ? ` and is scheduled ${p.schedule.times.length}× a day` : ""}. Crawling it again this soon mostly re-fetches the same jobs and adds load on the source.`,
        { title: "Crawl again?", confirmLabel: "Crawl again" });
      if (!ok) return;
    }
    await act(() => api.crawl(p.provider), `${fullyAdded(p) ? "Crawl" : "Add + crawl"} started for ${p.provider}`);
  }

  async function fullRecrawl(p: Provider) {
    const ok = await confirm(
      "Every stored posting is re-fetched and re-written, not just new ones — one request per posting, so a large provider takes a long time. Use it after an adapter fix.",
      { title: `Full re-crawl of ${p.provider}?`, confirmLabel: "Re-crawl" });
    if (ok) await act(() => api.crawl(p.provider, true), `Full re-crawl started for ${p.provider}`);
  }

  async function deleteSchedule(p: Provider) {
    if (!p.schedule) return;
    const ok = await confirm(
      `${p.provider} will no longer be crawled automatically. Its jobs and past runs stay; you can add a schedule again any time.`,
      { title: "Delete schedule?", confirmLabel: "Delete", danger: true });
    if (ok) await act(() => api.deleteSchedule(p.schedule!.id), `Schedule for ${p.provider} deleted`);
  }

  async function removeProvider(p: Provider) {
    const ok = await confirm(
      `Its ${p.addedCount} board(s) are retired and it stops being crawled${p.schedule ? ", and its schedule is deleted" : ""}. Once every board is retired, its activity history is cleared too. Its jobs stay as they are, and Add + Crawl brings it back.`,
      { title: `Remove ${p.provider}?`, confirmLabel: "Remove", danger: true });
    if (ok) await act(() => api.removeProvider(p.provider), `Removing ${p.provider} — see Activity for progress`);
  }

  const kindTabs = meta?.kindTabs ?? [{ value: "", label: "All" }];

  return (
    <main>
      <div className="page-header">
        <h1>Catalog</h1>
        <button type="button" className="btn btn-primary" onClick={() => setNewProviderOpen(true)}>+ New provider</button>
      </div>

      {data?.dbError && <p className="banner-warning">{data.dbError}</p>}
      {catalog.error && <p className="banner-warning">{catalog.error.message}</p>}

      <div className="table-card">
        <div className="toolbar toolbar-split">
          <div className="segmented" role="group" aria-label="Show">
            <button type="button" className={show ? "" : "active"} onClick={() => update({ show: "", page: "" })}>Added</button>
            <button type="button" className={show ? "active" : ""} onClick={() => update({ show: "all", page: "" })}>All</button>
          </div>
          <div className="tabs">
            {kindTabs.map((t) => (
              <button key={t.value} type="button" className={t.value === kind ? "active" : ""} onClick={() => update({ kind: t.value, page: "" })}>{t.label}</button>
            ))}
          </div>
          <div className="search-form">
            <input type="search" value={search} placeholder="Search provider or company…" autoComplete="off" onChange={(e) => setSearch(e.target.value)} />
          </div>
        </div>

        <table className="catalog-table">
          <colgroup>
            <col style={{ width: "22%" }} /><col style={{ width: "15%" }} /><col style={{ width: "13%" }} /><col style={{ width: "22%" }} /><col style={{ width: "22%" }} /><col style={{ width: 60 }} />
          </colgroup>
          <thead>
            <tr>
              <th>Provider</th>
              <th>Kind</th>
              <th>Added</th>
              <th>Schedule</th>
              <th>Last run</th>
              <th className="col-actions"></th>
            </tr>
          </thead>
          <tbody>
            {!data && <tr><td colSpan={6} className="muted">{catalog.error ? "Could not load the catalog." : "Loading…"}</td></tr>}
            {data?.providers.map((p) => (
              <tr key={p.provider}>
                <td>{p.provider}</td>
                <td className="muted">{p.kind}</td>
                <td><AddedBadge p={p} /></td>
                <td><ScheduleCell p={p} /></td>
                <td><LastRunCell p={p} /></td>
                <td className="col-actions">
                  <KebabMenu label={`Actions for ${p.provider}`}>
                    {(close) => (
                      <>
                        <button type="button" className="kebab-item" onClick={() => { close(); void crawl(p); }}>{fullyAdded(p) ? "Crawl" : "Add + Crawl"}</button>
                        {p.addedCount > 0 && <button type="button" className="kebab-item" onClick={() => { close(); void fullRecrawl(p); }}>Full re-crawl</button>}
                        <button type="button" className="kebab-item" onClick={() => { close(); void act(api.reindex, "Reindex queued"); }}>Reindex now</button>
                        <div className="kebab-sep" />
                        {p.schedule ? (
                          <>
                            <button type="button" className="kebab-item" onClick={() => { close(); setScheduleTarget({ provider: p.provider, id: p.schedule!.id, times: p.schedule!.times }); }}>Edit schedule</button>
                            <button type="button" className="kebab-item danger" onClick={() => { close(); void deleteSchedule(p); }}>Delete schedule</button>
                          </>
                        ) : (
                          <button type="button" className="kebab-item" onClick={() => { close(); setScheduleTarget({ provider: p.provider }); }}>Add schedule</button>
                        )}
                        {p.addedCount > 0 && (
                          <>
                            <div className="kebab-sep" />
                            <button type="button" className="kebab-item danger" onClick={() => { close(); void removeProvider(p); }}>Remove provider</button>
                          </>
                        )}
                      </>
                    )}
                  </KebabMenu>
                </td>
              </tr>
            ))}
            {data && data.providers.length === 0 && (
              <tr><td colSpan={6} className="muted">{data.addedOnly ? "No added providers match — add one from All." : "No providers match."}</td></tr>
            )}
          </tbody>
        </table>

        {data && (
          <div className="table-footer">
            {data.totalPages > 1 ? (
              <>
                {data.page > 1 && <button type="button" className="page-link" onClick={() => update({ page: String(data.page - 1) })}>← Prev</button>}
                <span className="muted">Page {data.page} / {data.totalPages} · {data.total} providers</span>
                {data.page < data.totalPages && <button type="button" className="page-link" onClick={() => update({ page: String(data.page + 1) })}>Next →</button>}
              </>
            ) : (
              <span className="muted">{data.total} providers</span>
            )}
          </div>
        )}
      </div>

      <ScheduleModal
        target={scheduleTarget}
        providers={data?.addedProviders ?? []}
        known={known}
        onClose={() => setScheduleTarget(null)}
        onSaved={(provider) => { setScheduleTarget(null); toast("Schedule saved for " + provider); refresh(); }}
      />
      <NewProviderModal
        open={newProviderOpen}
        kinds={meta?.newProviderKinds ?? []}
        providerKinds={meta?.providerKinds ?? {}}
        onClose={() => setNewProviderOpen(false)}
        onAdded={(provider, crawled) => { setNewProviderOpen(false); toast(crawled ? `Added ${provider} — crawl started` : `Added ${provider}`); refresh(); }}
      />
      <PageFooter />
    </main>
  );
}
