import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { ExplainSection, RunDetail } from "../api/types";
import { LocalTime } from "./LocalTime";
import { useToast } from "./Toasts";

// One run's log: optionally its head line (what ran, how it went, when, how
// long), its output, and "Explain this run". A run still going is re-read
// every 2s, keeping where the log was scrolled; an explanation, once asked
// for, is cached by Boardly and comes back with the run.

function outputText(r: RunDetail) {
  let t = `stdout:\n${r.stdout}\n\nstderr:\n${r.stderr}`;
  if (r.err) t += `\n\nerror: ${r.err}`;
  return t;
}

export function ExplainAnswer({ sections }: { sections: ExplainSection[] }) {
  return (
    <div className="explain-answer">
      <div className="explain-title">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 2l2.2 6.6L21 11l-6.8 2.4L12 20l-2.2-6.6L3 11l6.8-2.4z" /></svg>
        AI summary
      </div>
      {sections.map((s, i) => (
        <div key={i} className="explain-section">
          {s.label && <div className="explain-label">{s.label}</div>}
          <div className="explain-body">{s.body}</div>
        </div>
      ))}
    </div>
  );
}

// An answer reaches every open copy of the run — a shared step shows under
// each of its jobs.
const EXPLAINED = "boardly:explained";

export function RunLog({ runId, showHead = true }: { runId: number; showHead?: boolean }) {
  const toast = useToast();
  const [run, setRun] = useState<RunDetail | null>(null);
  const [error, setError] = useState("");
  const [explaining, setExplaining] = useState(false);
  const [answer, setAnswer] = useState<ExplainSection[] | null>(null);
  const pre = useRef<HTMLPreElement>(null);

  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout>;
    setRun(null); setError(""); setAnswer(null);
    const load = async () => {
      try {
        const r = await api.run(runId);
        if (!alive) return;
        const top = pre.current?.scrollTop ?? 0;
        setRun(r);
        requestAnimationFrame(() => { if (pre.current) pre.current.scrollTop = top; });
        const going = r.status === "running" || r.status === "queued";
        if (going) timer = setTimeout(load, 2000);
      } catch (e) {
        if (alive) setError((e as Error).message);
      }
    };
    void load();
    const onExplained = (e: Event) => {
      const d = (e as CustomEvent<{ id: number; sections: ExplainSection[] }>).detail;
      if (d.id === runId) setAnswer(d.sections);
    };
    window.addEventListener(EXPLAINED, onExplained);
    return () => { alive = false; clearTimeout(timer); window.removeEventListener(EXPLAINED, onExplained); };
  }, [runId]);

  async function explain() {
    setExplaining(true);
    try {
      const { sections } = await api.explain(runId);
      window.dispatchEvent(new CustomEvent(EXPLAINED, { detail: { id: runId, sections } }));
    } catch (e) {
      toast((e as Error).message, true);
    } finally {
      setExplaining(false);
    }
  }

  if (error) return <span className="muted">{error}</span>;
  if (!run) return <span className="muted">Loading…</span>;
  const sections = answer ?? run.explanation;
  return (
    <>
      {showHead && (
        <div className="run-detail-head">
          {run.provider && <>{run.provider}: </>}<span className={"pill pill-" + run.action}>{run.action}</span>
          <span className={"badge badge-" + run.status}>{run.status}</span>
          {run.summary && <><span>{run.summary}</span> ·</>}
          <LocalTime iso={run.startedAt} />
          {" · "}{run.duration}
        </div>
      )}
      <pre ref={pre} className="run-output">{outputText(run)}</pre>
      <div className="explain">
        {sections ? <ExplainAnswer sections={sections} /> : (
          <button type="button" className="btn btn-secondary btn-sm" disabled={!run.explainEnabled || explaining}
            title={run.explainEnabled ? undefined : "Set OPENAI_API_KEY in .env to enable"} onClick={explain}>
            {explaining ? "Thinking…" : "✦ Explain this run"}
          </button>
        )}
      </div>
    </>
  );
}
