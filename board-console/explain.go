package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// explainSystemPrompt is the standing context the model gets with every
// run: what this tool is, what each action does, and the failures an
// operator has actually hit, so an explanation is specific to freehire
// rather than generic advice about logs.
const explainSystemPrompt = `You explain runs of "Board Console", an internal ops tool for freehire (an open-source IT job aggregator). The reader is the operator who manages it; answer for them, briefly and concretely.

What the tool does: it keeps a catalog of job "boards" (a company's page on an ATS such as greenhouse/lever/ashby/adp/keka, an aggregator feed such as jobdanmark/bayt, or a company career site), adds them to freehire's database, crawls them, and rebuilds search. Each run on the Activity page is one of freehire's own command-line workers, started by a button, a schedule or the daily cleanup:
- add-boards: inserts a provider's boards from the CSV catalog into the database. "duplicate" rows are boards already there — harmless.
- ingest: crawls every board of ONE provider and stores the jobs. Boards are fetched 8 at a time. "progress N/M boards crawled" is a once-a-minute heartbeat; a single big board can show 0/1 for many minutes while it works. The program exits non-zero if ANY board failed, even when thousands of jobs were ingested, so the page shows an Outcome instead of the exit status: success, partial (jobs came in but some boards failed — the crawl worked), or failed (nothing came in, or the program itself broke). Use the Outcome you are given; never call a partial run a failure. "ingest health: N unhealthy board(s)" lists boards across ALL providers that failed on recent runs (fails=count, cooled_until=time the board is skipped until); it is background information, not necessarily about this run. "in cooldown — skipping" means a board failed repeatedly and is paused until cooled_until.
- reindex: rebuilds the Meilisearch jobs index from the database. It refuses to start when free disk space is below REINDEX_MIN_FREE_GB ("disk-guard") — fix by freeing disk (docker builder prune / docker image prune) or lowering that floor; the index itself is small.
- close-chronic-boards: the daily dead-board cleanup; closes (never deletes) jobs of boards unreachable for 60 days or empty for 30. "dry run" = Preview, changes nothing.
- recount-companies / reindex-companies: recompute each company's open-job count, then rebuild company search.
- remove-boards: "Remove provider" — retires every live board of one provider through freehire's add-board --retire (status 'retired', nothing deleted, its jobs left as they are) and deletes its schedules. A board that could not be retired usually was already retired. Add + Crawl on the Catalog brings the provider back.

Common causes: HTTP 429 = the source is rate-limiting us (too many requests; retry later, crawl less often). 403 = the source blocks our IP (bayt.com blocks everything except a paid scraping service and yields almost no tech jobs — advise dropping it). "invalid character ... looking for beginning of value" on a JSON API = the source returned an HTML page instead of data, usually because that company left the platform or changed its URL — the board is probably dead. EOF / connection reset = the source dropped the connection, often transient. 5xx = the source's own server error, transient. "permission denied" on /app/data = the container cannot write its data folder. "interrupted (board-console restarted)" = the container restarted mid-run; just run it again. A laptop that went to sleep pauses runs, which shows as long gaps between heartbeat lines. CATALOGUE_TECH_ONLY=false means non-technical jobs are accepted too — expected, not an error.

What the operator can do in the UI: Catalog row menu (Crawl, Full re-crawl, Reindex now, add/edit/delete a schedule), Activity (Reindex now, Recount companies, Cleanup tab: Preview / Run now), Schedules page (pause, edit interval). Only suggest shell commands when the fix genuinely needs the server.

Answer in plain text (no markdown symbols like ** or #), under 180 words, in exactly three short sections:
What happened: one or two sentences, including how much succeeded.
Why: the cause, citing the specific log line.
What to do: 1-3 concrete steps, or "Nothing — this is expected." when that is the truth.`

// explainLogBudget caps how much of a run's output is sent — the tail,
// where a failure is explained. A crawl's log can reach 2MB.
const explainLogBudget = 12_000

// explainLineCap trims each log line: one "ingest health" line lists 20
// board IDs and can run to thousands of characters.
const explainLineCap = 600

// Explainer asks an OpenAI-compatible chat model to explain one Activity
// run. Answers for finished runs are cached in memory by run ID, so the
// page's live refresh can re-render them and asking again costs nothing.
type Explainer struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client

	mu    sync.Mutex
	cache map[int]string
}

// NewExplainerFromEnv reads OPENAI_API_KEY (unset disables the feature),
// OPENAI_MODEL and OPENAI_BASE_URL (for an OpenAI-compatible gateway).
func NewExplainerFromEnv() *Explainer {
	return &Explainer{
		apiKey:  os.Getenv("OPENAI_API_KEY"),
		model:   envOr("OPENAI_MODEL", "gpt-4o-mini"),
		baseURL: strings.TrimRight(envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		client:  &http.Client{Timeout: 90 * time.Second},
		cache:   map[int]string{},
	}
}

func (e *Explainer) Enabled() bool { return e.apiKey != "" }

// Cached returns every cached explanation, for the page to render.
func (e *Explainer) Cached() map[int]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[int]string, len(e.cache))
	for id, s := range e.cache {
		out[id] = s
	}
	return out
}

// Forget drops the cached answers for runs that no longer exist.
func (e *Explainer) Forget(ids []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range ids {
		delete(e.cache, id)
	}
}

// Explain returns the model's explanation of run. history is the same
// provider's (or action's) recent runs, oldest context for patterns like a
// board that has failed three times running.
func (e *Explainer) Explain(ctx context.Context, run *Run, history []*Run) (string, error) {
	finished := run.Status == StatusDone || run.Status == StatusFailed
	if finished {
		e.mu.Lock()
		cached, ok := e.cache[run.ID]
		e.mu.Unlock()
		if ok {
			return cached, nil
		}
	}

	body, err := json.Marshal(map[string]any{
		"model": e.model,
		"messages": []map[string]string{
			{"role": "system", "content": explainSystemPrompt},
			{"role": "user", "content": runContext(run, history)},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach the model: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("model answered %s with an unreadable body", resp.Status)
	}
	if out.Error != nil {
		return "", fmt.Errorf("model error: %s", out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK || len(out.Choices) == 0 {
		return "", fmt.Errorf("model answered %s with no explanation", resp.Status)
	}
	answer := strings.TrimSpace(out.Choices[0].Message.Content)

	// A running run's log is still growing: its explanation is a snapshot,
	// so only a finished run's answer is kept.
	if finished {
		e.mu.Lock()
		e.cache[run.ID] = answer
		e.mu.Unlock()
	}
	return answer, nil
}

// runContext is the user message: the run's facts, the tail of its output,
// the environment settings that change what the log means, and recent
// history for the same provider.
func runContext(run *Run, history []*Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Action: %s", run.Action)
	if run.Label != "" {
		fmt.Fprintf(&b, " (%s)", run.Label)
	}
	provider := run.Provider
	if provider == "" {
		provider = "(none — catalogue-wide)"
	}
	o := run.Outcome()
	fmt.Fprintf(&b, "\nProvider: %s\nExit status: %s\nOutcome shown to the operator: %s",
		provider, run.Status, o.Status)
	if o.Summary != "" {
		fmt.Fprintf(&b, " (%s)", o.Summary)
	}
	fmt.Fprintf(&b, "\nDuration: %s\nStarted: %s\n", run.Duration(), run.StartedAt.UTC().Format(time.RFC3339))
	if run.Err != "" {
		fmt.Fprintf(&b, "Exit error: %s\n", run.Err)
	}
	scope := "IT jobs only"
	if !catalogueTechOnly() {
		scope = "all jobs (CATALOGUE_TECH_ONLY=false)"
	}
	fmt.Fprintf(&b, "Catalogue scope: %s\n", scope)
	if v := os.Getenv("REINDEX_MIN_FREE_GB"); v != "" {
		fmt.Fprintf(&b, "REINDEX_MIN_FREE_GB: %s\n", v)
	}

	if len(history) > 0 {
		b.WriteString("\nEarlier runs of the same kind (newest first):\n")
		for _, h := range history {
			line := fmt.Sprintf("- %s %s %s, %s", h.StartedAt.UTC().Format("2006-01-02 15:04"), h.Action, h.Status, h.Duration())
			if h.Err != "" {
				line += ", error: " + h.Err
			}
			b.WriteString(capLine(line) + "\n")
		}
	}

	b.WriteString("\nstderr (tail):\n")
	b.WriteString(logTail(run.Stderr, explainLogBudget*3/4))
	if strings.TrimSpace(run.Stdout) != "" {
		b.WriteString("\n\nstdout (tail):\n")
		b.WriteString(logTail(run.Stdout, explainLogBudget/4))
	}
	return b.String()
}

// logTail keeps the last budget bytes of s, whole lines only, each capped
// at explainLineCap.
func logTail(s string, budget int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var kept []string
	used := 0
	for i := len(lines) - 1; i >= 0; i-- {
		line := capLine(lines[i])
		if used+len(line)+1 > budget {
			kept = append(kept, "[...earlier output omitted...]")
			break
		}
		kept = append(kept, line)
		used += len(line) + 1
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return strings.Join(kept, "\n")
}

func capLine(s string) string {
	if len(s) <= explainLineCap {
		return s
	}
	return s[:explainLineCap] + " [...]"
}

// explainSection is one labelled part of an answer ("Why", ...).
type explainSection struct {
	Label string
	Body  string
}

// explainLabels are the section headings the system prompt asks for.
var explainLabels = []string{"What happened", "Why", "What to do"}

// explainSections splits an answer at the labels explainSystemPrompt asks
// for, so the page can set them as headings. An answer that ignored the
// format comes back as one unlabelled section, never lost.
func explainSections(answer string) []explainSection {
	var out []explainSection
	for _, line := range strings.Split(answer, "\n") {
		trimmed := strings.TrimSpace(line)
		label := ""
		for _, l := range explainLabels {
			if strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(l)+":") {
				label = l
				trimmed = strings.TrimSpace(trimmed[len(l)+1:])
				break
			}
		}
		switch {
		case label != "":
			out = append(out, explainSection{Label: label, Body: trimmed})
		case trimmed == "":
			continue
		case len(out) == 0:
			out = append(out, explainSection{Body: trimmed})
		default:
			out[len(out)-1].Body = strings.TrimSpace(out[len(out)-1].Body + "\n" + trimmed)
		}
	}
	return out
}
