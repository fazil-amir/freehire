import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api/client";
import type { ScheduleLoad } from "../api/types";
import { toLocalMin, toUTCMin, zoneName } from "../lib/time";
import { useAppData } from "./AppData";
import { Dialog, DialogHeader } from "./Dialog";

// The add/edit schedule dialog. A schedule is one provider's list of daily
// run times; each row is an hour and a minute on the 15-minute grid in the
// viewer's timezone, saved as minutes after 00:00 UTC. Nothing blocks while
// filling it in: rows can be added freely, an empty one is ignored on save,
// a half-filled one is only called out once a save is tried. Each row says
// how taken its slot already is — booked is a warning, never a refusal.

export type ScheduleTarget = { provider?: string; id?: string; times?: number[] };
type Row = { h: string; m: string };

const HOURS = Array.from({ length: 24 }, (_, i) => i);
const MINUTES = [0, 15, 30, 45];
const emptyRow: Row = { h: "", m: "" };

function rowsFrom(utcTimes: number[]): Row[] {
  const local = utcTimes.map(toLocalMin).sort((a, b) => a - b);
  return local.length ? local.map((t) => ({ h: String(Math.floor(t / 60)), m: String(t % 60) })) : [{ ...emptyRow }];
}
const rowMin = (r: Row) => (r.h === "" || r.m === "" ? -1 : Number(r.h) * 60 + Number(r.m));
const halfFilled = (r: Row) => (r.h === "") !== (r.m === "");

export function ScheduleModal({
  target, providers, known, onClose, onSaved,
}: {
  target: ScheduleTarget | null; // null = closed
  providers: string[]; // the added providers, for the picker
  known: Record<string, { id: string; times: number[] }>; // schedules we know of, by provider
  onClose: () => void;
  onSaved: (provider: string) => void;
}) {
  const [provider, setProvider] = useState("");
  const [id, setId] = useState<string | undefined>();
  const [rows, setRows] = useState<Row[]>([{ ...emptyRow }]);
  const [ownTimes, setOwnTimes] = useState<number[]>([]); // this schedule's saved runs, left out of the load
  const [checked, setChecked] = useState(false);
  const [error, setError] = useState("");
  const [providerError, setProviderError] = useState("");
  const [load, setLoad] = useState<ScheduleLoad | null>(null);
  const { meta } = useAppData();
  const capacity = load?.capacity ?? meta?.scheduleCapacity;
  const [saving, setSaving] = useState(false);
  // "+ Add another time" puts the cursor on the new row's hour.
  const focusNew = useRef(false);

  function editFor(p: string, sch?: { id?: string; times?: number[] }) {
    setProvider(p);
    setId(sch?.id);
    setRows(rowsFrom(sch?.times ?? []));
    setOwnTimes(sch?.times ?? []);
    setChecked(false);
    setError("");
    setProviderError("");
  }

  useEffect(() => {
    if (!target) return;
    const p = target.provider ?? "";
    // One schedule per provider: "Add" for a provider that has one edits it.
    editFor(p, target.id ? target : known[p]);
    setLoad(null);
    api.scheduleLoad().then(setLoad).catch(() => setLoad(null)); // no warnings rather than wrong ones
  }, [target]);

  const list = useMemo(() => (provider && !providers.includes(provider) ? [...providers, provider].sort() : providers), [providers, provider]);

  // Per row: duplicate, booked, some taken, free — or, after a save attempt,
  // which half is missing.
  const notes = useMemo(() => {
    const l = load ? [...load.load] : null;
    if (l) ownTimes.forEach((t) => { const s = Math.floor(t / 15) % 96; l[s] = Math.max(0, l[s] - 1); });
    const seen = new Set<number>();
    return rows.map((r) => {
      const m = rowMin(r);
      if (m < 0) return checked && halfFilled(r) ? { state: "invalid", msg: r.h === "" ? "pick the hour" : "pick the minutes" } : { state: "", msg: "" };
      if (seen.has(m)) return { state: "dup", msg: "same time twice" };
      seen.add(m);
      if (!l || !load) return { state: "", msg: "" };
      const n = l[Math.floor(toUTCMin(m) / 15) % 96];
      if (n >= load.capacity) return { state: "booked", msg: "booked · will queue" };
      if (n > 0) return { state: "busy", msg: `${n} of ${load.capacity} taken` };
      return { state: "free", msg: "free" };
    });
  }, [rows, load, ownTimes, checked]);

  const filled = rows.filter((r) => rowMin(r) >= 0);
  const zone = zoneName();

  async function save() {
    setChecked(true);
    // The provider's message sits under its select; the times' in the banner.
    setProviderError(provider ? "" : "Select a provider.");
    const half = rows.map((r, i) => (halfFilled(r) ? i + 1 : 0)).filter(Boolean);
    let msg = "";
    if (half.length) msg = (half.length === 1 ? `Time ${half[0]} is` : `Times ${half.join(", ")} are`) + " missing the hour or the minutes.";
    else if (!filled.length) msg = "Add at least one run time.";
    setError(msg);
    if (!provider || msg) return;
    setSaving(true);
    try {
      await api.saveSchedule({ id, provider, times: filled.map((r) => toUTCMin(rowMin(r))) });
      onSaved(provider);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  const setRow = (i: number, patch: Partial<Row>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  return (
    <Dialog open={!!target} onClose={onClose} className="dialog-sm">
      <form noValidate onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <DialogHeader title={id ? "Edit schedule" : "Add schedule"} subtitle="Crawl a provider every day at the times you pick." onClose={onClose} />
        <div className="dialog-body">
          {error && <p className="field-error">{error}</p>}
          <label>Provider
            <select value={provider} onChange={(e) => {
              const p = e.target.value;
              if (!id && known[p]) editFor(p, known[p]);
              else setProvider(p);
            }}>
              <option value="" disabled>Select an added provider…</option>
              {list.map((p) => <option key={p} value={p}>{p}</option>)}
            </select>
            {providerError && <span className="field-error-inline">{providerError}</span>}
          </label>
          <div className="schedule-times">
            <div className="schedule-times-head">
              <span className="field-label">Run times</span>
              <span className="field-hint">{(filled.length ? `${filled.length}× a day · ` : "") + (zone ? `times in ${zone}` : "in your timezone")}</span>
            </div>
            <ol className="time-rows">
              {rows.map((r, i) => (
                <li key={i} className="time-row" data-state={notes[i].state}>
                  <span className="time-index" aria-hidden="true">{i + 1}</span>
                  <select aria-label="Hour" value={r.h} onChange={(e) => setRow(i, { h: e.target.value })}
                    ref={(el) => { if (el && focusNew.current && i === rows.length - 1) { focusNew.current = false; el.focus(); } }}>
                    <option value="">HH</option>
                    {HOURS.map((h) => <option key={h} value={h}>{String(h).padStart(2, "0")}</option>)}
                  </select>
                  <span className="time-sep">:</span>
                  <select aria-label="Minute" value={r.m} onChange={(e) => setRow(i, { m: e.target.value })}>
                    <option value="">MM</option>
                    {MINUTES.map((m) => <option key={m} value={m}>{String(m).padStart(2, "0")}</option>)}
                  </select>
                  <span className="time-note">{notes[i].msg}</span>
                  <button type="button" className="time-remove" aria-label="Remove this time" hidden={rows.length === 1}
                    onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>✕</button>
                </li>
              ))}
            </ol>
            <button type="button" className="add-time" onClick={() => { focusNew.current = true; setRows((rs) => [...rs, { ...emptyRow }]); }}>+ Add another time</button>
            <p className="field-hint">
              A time already holding {capacity ?? "its limit of"} runs is <strong>booked</strong>. You can still pick it; the crawl then
              waits for a free spot. Search refreshes once an hour after scheduled crawls.
            </p>
          </div>
        </div>
        <div className="dialog-actions">
          <button type="button" className="btn btn-secondary" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={saving}>{id ? "Save changes" : "Save schedule"}</button>
        </div>
      </form>
    </Dialog>
  );
}

