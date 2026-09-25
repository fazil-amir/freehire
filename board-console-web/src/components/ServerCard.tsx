import { useState } from "react";
import { api } from "../api/client";
import { size } from "../lib/time";
import { useAppData } from "./AppData";
import { useConfirm } from "./Confirm";
import { useToast } from "./Toasts";

// The sidebar's Server card: disk, memory, CPU and Docker's build cache.
// A meter with no reading is hidden; a failed poll greys the "live" dot, so
// a number is never shown as current when it is not.
function Meter({ label, pct, detail, note }: { label: string; pct: number | null; detail: string; note?: string }) {
  if (pct == null) return null;
  const p = Math.max(0, Math.min(100, pct));
  const level = p >= 90 ? " crit" : p >= 75 ? " warn" : "";
  return (
    <div className={"meter" + level}>
      <div className="meter-row">
        <span className="meter-label">{label}</span>
        <span className="meter-detail">{detail}</span>
        <span className="meter-pct">{Math.round(p)}%</span>
      </div>
      <div className="meter-track"><div className="meter-bar" style={{ width: p + "%" }} /></div>
      {note && <p className="meter-note">{note}</p>}
    </div>
  );
}

export function ServerCard() {
  const { stats: s, statsError, reloadStats } = useAppData();
  const confirm = useConfirm();
  const toast = useToast();
  const [pruning, setPruning] = useState(false);
  if (!s) return null;

  const GB = 1024 ** 3;
  const d = s.disk, m = s.mem;
  const floorNote = d && s.reindexFloorGB && d.free < s.reindexFloorGB * GB
    ? `${size(d.free)} free — reindex needs ${s.reindexFloorGB} GB` : undefined;

  async function prune() {
    const ok = await confirm(
      "Deletes Docker's cached image build steps only. Containers, the images they run and all data are untouched; the next image build is just slower.",
      { title: "Clear build cache?", confirmLabel: "Clear cache" });
    if (!ok) return;
    setPruning(true);
    try {
      const r = await api.pruneBuildCache();
      toast("Cleared " + (r.reclaimedText || "the build cache"));
    } catch (e) {
      toast((e as Error).message, true);
    } finally {
      setPruning(false);
      void reloadStats();
    }
  }

  return (
    <section className="server-card" aria-label="Server">
      <header className="server-head">
        <span className="server-title">Server</span>
        <span className={"server-live" + (statsError ? " stale" : "")} title={statsError ? "Could not refresh — showing the last reading" : "Updated every 5 seconds"}>
          {statsError ? "stale" : "live"}
        </span>
      </header>
      <Meter label="Disk" pct={d && d.total ? (d.used / d.total) * 100 : null} detail={d ? `${size(d.used)} of ${size(d.total)}` : ""} note={floorNote} />
      <Meter label="Memory" pct={m && m.total ? (m.used / m.total) * 100 : null} detail={m ? `${size(m.used)} of ${size(m.total)}` : ""} />
      <Meter label="CPU" pct={s.cpu ? s.cpu.pct : null} detail={s.cpu ? `${s.cpu.cores} ${s.cpu.cores === 1 ? "core" : "cores"}` : ""} />
      {s.buildCache ? (
        <div className="server-cache">
          <div className="meter-row">
            <span className="meter-label">Build cache</span>
            <span className="meter-pct">{size(s.buildCache.total)}</span>
          </div>
          <p className="server-sub">{s.buildCache.reclaimable > 0 ? `${size(s.buildCache.reclaimable)} can be cleared` : "Nothing to clear"}</p>
          <button type="button" className="btn btn-secondary btn-sm server-prune" disabled={pruning || s.buildCache.reclaimable <= 0} onClick={prune}>
            {pruning ? "Clearing…" : "Clear build cache"}
          </button>
        </div>
      ) : (
        <p className="server-sub server-cache-off" title={s.dockerError || "Set DOCKER_PROXY_URL (see docker-compose.yml) to show and clear it."}>
          {s.docker ? "Build cache: Docker not reachable" : "Build cache: Docker not connected"}
        </p>
      )}
    </section>
  );
}
