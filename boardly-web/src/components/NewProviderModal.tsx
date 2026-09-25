import { useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import { Dialog, DialogHeader } from "./Dialog";

// "+ New provider": add a board to the catalog, optionally crawling it at
// once. The kind decides which fields are even relevant — an ATS platform is
// many companies each with a board (provider, board, company); an Aggregator
// is one feed (provider only; it is always crawled); a Career site has no
// board (provider, company). The server applies the same rules.
const ATS = "ATS platform", AGG = "Aggregator", CAREER = "Career site";

export function NewProviderModal({
  open, kinds, providerKinds, onClose, onAdded,
}: {
  open: boolean;
  kinds: string[];
  providerKinds: Record<string, string>;
  onClose: () => void;
  onAdded: (provider: string, crawled: boolean) => void;
}) {
  const [kind, setKind] = useState(ATS);
  const [provider, setProvider] = useState("");
  const [board, setBoard] = useState("");
  const [company, setCompany] = useState("");
  const [crawlNow, setCrawlNow] = useState(false);
  const [error, setError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<{ provider?: string; board?: string; company?: string }>({});
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setKind(ATS); setProvider(""); setBoard(""); setCompany(""); setCrawlNow(false); setError(""); setFieldErrors({});
  }, [open]);

  const options = useMemo(() => Object.keys(providerKinds).filter((p) => providerKinds[p] === kind).sort(), [providerKinds, kind]);
  const showBoard = kind === ATS;
  const showCompany = kind === ATS || kind === CAREER;

  // Checked here first, each message under its own field; the server
  // applies the same rules and has the last word.
  async function submit() {
    const errs: typeof fieldErrors = {};
    if (!provider || providerKinds[provider] === undefined) errs.provider = "Select a provider from the list.";
    if (showBoard && !board.trim()) errs.board = "Board is required for an ATS platform.";
    if (showCompany && !company.trim()) errs.company = "Company is required.";
    setFieldErrors(errs);
    if (Object.keys(errs).length) return;
    setSaving(true);
    try {
      const crawl = kind === AGG ? true : crawlNow;
      await api.newProvider({ provider, board: showBoard ? board : "", company: showCompany ? company : "", crawlNow: crawl });
      onAdded(provider, crawl);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={open} onClose={onClose}>
      <form noValidate onSubmit={(e) => { e.preventDefault(); void submit(); }}>
        <DialogHeader title="New provider" subtitle="Add a board to the catalog, and optionally crawl it right away." onClose={onClose} />
        <div className="dialog-body">
          {error && <p className="field-error">{error}</p>}
          <div className="field-grid">
            <label>Kind
              <select value={kind} onChange={(e) => { setKind(e.target.value); setProvider(""); setError(""); setFieldErrors({}); }}>
                {kinds.map((k) => <option key={k} value={k}>{k}</option>)}
              </select>
            </label>
            <label>Provider
              <input type="text" list="new-provider-datalist" value={provider} autoComplete="off" required
                className={fieldErrors.provider ? "input-invalid" : undefined}
                placeholder={`Type to search ${kind} providers…`} onChange={(e) => setProvider(e.target.value)} />
              <datalist id="new-provider-datalist">{options.map((p) => <option key={p} value={p} />)}</datalist>
              {fieldErrors.provider && <span className="field-error-inline">{fieldErrors.provider}</span>}
            </label>
            {showBoard && (
              <label>Board
                <input type="text" value={board} required className={fieldErrors.board ? "input-invalid" : undefined} onChange={(e) => setBoard(e.target.value)} />
                <span className="field-hint">The board ID from freehire's adapter — one ATS hosts many boards.</span>
                {fieldErrors.board && <span className="field-error-inline">{fieldErrors.board}</span>}
              </label>
            )}
            {showCompany && (
              <label>Company
                <input type="text" value={company} required className={fieldErrors.company ? "input-invalid" : undefined} onChange={(e) => setCompany(e.target.value)} />
                <span className="field-hint">The employer's display name.</span>
                {fieldErrors.company && <span className="field-error-inline">{fieldErrors.company}</span>}
              </label>
            )}
          </div>
          {kind !== AGG && (
            <label className="checkbox-label">
              <input type="checkbox" checked={crawlNow} onChange={(e) => setCrawlNow(e.target.checked)} /> Crawl immediately after adding
            </label>
          )}
          <details className="dialog-help">
            <summary>Provider not listed?</summary>
            <p>freehire has no adapter for it yet. To add a brand-new provider:</p>
            <ol>
              <li>Write and merge the Go adapter in <code>internal/ingest/sources/</code></li>
              <li>Rebuild the stack: <code>make up --build</code></li>
              <li>Add one row for it to <code>boardly-api/data/combined_boards.csv</code> (provider, board, company columns)</li>
              <li>Optionally add it to <code>providerKind</code> in <code>providerkind.go</code>, or it'll show under the "Unclassified" tab</li>
            </ol>
          </details>
        </div>
        <div className="dialog-actions">
          <button type="button" className="btn btn-secondary" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={saving}>{kind === AGG ? "Add + Crawl" : "Add row"}</button>
        </div>
      </form>
    </Dialog>
  );
}
