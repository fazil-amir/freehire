package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

const subprocessTimeout = 30 * time.Minute

// maxConcurrentIngest bounds how many `ingest` subprocesses run at once —
// enough for several providers to crawl in parallel without a Greenhouse-
// sized one blocking every other click behind it, but not so many that a
// burst of clicks saturates the host running this alongside the crawl
// fleet it's managing.
const maxConcurrentIngest = 4

// Runner drives the three freehire binaries (bulk-add-boards, ingest,
// reindex) as local subprocesses — they were copied into this image at
// build time from the same freehire build stage, not invoked via docker
// exec against a separate container.
//
// Concurrency is per-purpose, not one global lock: ingest is bounded by a
// semaphore (up to maxConcurrentIngest at once, across ALL callers —
// different providers genuinely run in parallel); reindex is a full-
// catalog operation, so it's serialized and coalesced instead (never two
// running at once, and a request that arrives mid-run is satisfied by one
// extra run right after, not by starting a redundant second one);
// add-boards has no limit of its own — it's fast, scoped to one provider,
// and (since it no longer writes to the CSV) has nothing to race on.
type Runner struct {
	csv      *CSVStore
	activity *ActivityLog
	db       *DBStore // read-only; nil falls back to the CSV's frozen added column
	cleanup  *CleanupStore
	bin      Binaries

	crawlMu  sync.Mutex
	crawling map[string]bool // providers with a crawl in flight — see claimCrawl

	// heavyMu serializes the catalogue-wide Meilisearch rebuilds board-console
	// runs: the jobs reindex and reindex-companies. freehire's own guard
	// between them is a Postgres advisory lock (worker.HoldHeavyIndexLock)
	// that makes the loser SKIP and exit 0 — so without this, a company
	// refresh started during a reindex would quietly do nothing.
	heavyMu sync.Mutex

	// companyMu admits one company refresh at a time; a second is refused.
	companyMu sync.Mutex

	// cleanupMu admits one dead-board cleanup (Preview or real) at a time;
	// a second request is refused rather than queued — see StartCleanup.
	cleanupMu sync.Mutex

	ingestSem chan struct{} // buffered to maxConcurrentIngest

	reindexMu      sync.Mutex // guards the two fields below, not subprocess execution itself
	reindexRunning bool
	reindexAgain   bool // a request arrived while one was already running — run once more after
}

// Binaries holds the paths to the freehire worker binaries, overridable for
// local (non-container) testing.
type Binaries struct {
	BulkAddBoards      string
	Ingest             string
	Reindex            string
	CloseChronicBoards string
	RecountCompanies   string
	ReindexCompanies   string
	CSVPath            string
}

func DefaultBinaries() Binaries {
	return Binaries{
		BulkAddBoards:      "/app/bulk-add-boards",
		Ingest:             "/app/ingest",
		Reindex:            "/app/reindex",
		CloseChronicBoards: "/app/close-chronic-boards",
		RecountCompanies:   "/app/recount-companies",
		ReindexCompanies:   "/app/reindex-companies",
		CSVPath:            "/app/data/combined_boards.csv",
	}
}

func NewRunner(csv *CSVStore, activity *ActivityLog, db *DBStore, cleanup *CleanupStore, bin Binaries) *Runner {
	return &Runner{
		csv:       csv,
		activity:  activity,
		db:        db,
		cleanup:   cleanup,
		bin:       bin,
		ingestSem: make(chan struct{}, maxConcurrentIngest),
		crawling:  map[string]bool{},
	}
}

// FullyAdded reports whether every one of a provider's CSV candidate boards
// (distinct by boardKey, not by row) is already added — read from Postgres (the live, correct answer) when
// reachable, falling back to the CSV's own frozen `added` column
// otherwise. Used to decide whether a Crawl action needs an add-boards
// step first; a false positive here just means an extra, idempotent
// add-boards call, never a skipped one.
func (r *Runner) FullyAdded(ctx context.Context, provider string) bool {
	total := boardCounts(r.csv.Rows())[provider]
	if total == 0 {
		return false
	}
	if r.db != nil {
		if counts, err := r.db.AddedCounts(ctx); err == nil {
			return counts[provider] >= total
		}
	}
	return r.csv.FullyAddedProviders()[provider]
}

// runSubprocess runs one subprocess against an already-created (running)
// Run, streaming its stdout/stderr as they're written (not buffering until
// exit) so the Activity page can show a live-updating log while the
// command is still going.
//
// extraEnv (KEY=value) is added on top of board-console's own environment,
// which every subprocess inherits — CATALOGUE_TECH_ONLY included.
func (r *Runner) runSubprocess(run *Run, extraEnv []string, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		r.activity.Finish(run, err)
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		r.activity.Finish(run, err)
		return err
	}

	if err := cmd.Start(); err != nil {
		r.activity.Finish(run, err)
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamInto(r.activity, run, stdoutPipe, false)
	}()
	go func() {
		defer wg.Done()
		streamInto(r.activity, run, stderrPipe, true)
	}()
	wg.Wait()

	err = cmd.Wait()
	r.activity.Finish(run, err)
	return err
}

// exec runs one subprocess immediately, with its own Activity row starting
// straight in "running" — used for add-boards and reindex, neither of
// which is concurrency-limited the way ingest is.
func (r *Runner) exec(action, provider, name string, args ...string) error {
	run := r.activity.Start(action, provider)
	return r.runSubprocess(run, nil, name, args...)
}

// execIngestQueued creates the Activity row up front as "queued" — visible
// the instant it's requested — and only flips it to "running" once a
// concurrency slot is actually free, so a click that arrives while
// maxConcurrentIngest ingests are already busy still shows up immediately
// instead of looking silently dropped.
func (r *Runner) execIngestQueued(provider, label string, extraEnv []string, name string, args ...string) error {
	run := r.activity.StartQueued("ingest", provider, label)
	r.ingestSem <- struct{}{}
	defer func() { <-r.ingestSem }()
	r.activity.MarkRunning(run)
	return r.runSubprocess(run, extraEnv, name, args...)
}

// streamInto copies reader into the run's stdout or stderr in chunks, as
// they arrive, rather than waiting for the process to exit.
func streamInto(activity *ActivityLog, run *Run, reader io.Reader, stderr bool) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			activity.AppendOutput(run, buf[:n], stderr)
		}
		if err != nil {
			return
		}
	}
}

// runAdd inserts one provider's rows into freehire's catalog via
// bulk-add-boards --apply, scoped to that provider. It no longer marks the
// CSV's `added` column — Postgres is the source of truth for that now
// (see db.go); the CSV keeps only the candidate list (provider/board/
// company) plus a frozen `added` value used solely as a stale fallback
// display when the database can't be reached.
func (r *Runner) runAdd(provider string) error {
	if err := r.exec("add-boards", provider, r.bin.BulkAddBoards,
		"-in", r.bin.CSVPath, "--apply", "-provider="+provider); err != nil {
		return fmt.Errorf("add-boards %s: %w", provider, err)
	}
	return nil
}

// runIngest crawls one provider, queued behind the ingest concurrency
// limit if it's currently saturated. refetchAll is freehire's
// INGEST_REFETCH_ALL: every listed posting is treated as new and re-written,
// not just liveness-refreshed — the repair path after an adapter fix, at one
// detail request per stored posting.
func (r *Runner) runIngest(provider string, refetchAll bool) error {
	label, env := "", []string(nil)
	if refetchAll {
		label, env = "full re-crawl", []string{"INGEST_REFETCH_ALL=1"}
	}
	if err := r.execIngestQueued(provider, label, env, r.bin.Ingest, provider); err != nil {
		return fmt.Errorf("ingest %s: %w", provider, err)
	}
	return nil
}

// runReindexNow actually runs the reindex subprocess — call queueReindex
// instead, which is what coalesces concurrent requests into this.
func (r *Runner) runReindexNow() error {
	r.heavyMu.Lock()
	defer r.heavyMu.Unlock()
	if err := r.exec("reindex", "", r.bin.Reindex); err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	return nil
}

// queueReindex triggers a reindex, coalesced with any already in flight:
// if one is already running, this request is satisfied by marking that one
// to run ONE more time right after it finishes — never two running at
// once, and never more than one queued regardless of how many requests
// arrive while it's busy (a full-catalog rebuild the second call would
// have started is redundant with the one the first call is about to run
// again anyway). Always returns immediately — the actual reindex(es) run
// in their own goroutine.
func (r *Runner) queueReindex() {
	r.reindexMu.Lock()
	if r.reindexRunning {
		r.reindexAgain = true
		r.reindexMu.Unlock()
		return
	}
	r.reindexRunning = true
	r.reindexMu.Unlock()

	go r.drainReindex()
}

func (r *Runner) drainReindex() {
	for {
		_ = r.runReindexNow()

		r.reindexMu.Lock()
		if r.reindexAgain {
			r.reindexAgain = false
			r.reindexMu.Unlock()
			continue
		}
		r.reindexRunning = false
		r.reindexMu.Unlock()
		return
	}
}

// Crawling reports whether a crawl (add-boards and/or ingest) of provider
// is in flight, from any caller.
func (r *Runner) Crawling(provider string) bool {
	r.crawlMu.Lock()
	defer r.crawlMu.Unlock()
	return r.crawling[provider]
}

// claimCrawl marks provider as crawling, reporting false if it already was.
// One provider is never crawled twice at once, whoever asks — a click, a
// schedule, a bulk run: a second copy would re-fetch the same boards,
// double the load on the source, and race the first over the same rows.
func (r *Runner) claimCrawl(provider string) bool {
	r.crawlMu.Lock()
	defer r.crawlMu.Unlock()
	if r.crawling[provider] {
		return false
	}
	r.crawling[provider] = true
	return true
}

func (r *Runner) releaseCrawl(provider string) {
	r.crawlMu.Lock()
	defer r.crawlMu.Unlock()
	delete(r.crawling, provider)
}

// crawlOne adds provider's boards when not every one is added yet, then
// ingests it. The caller holds the provider's claim.
func (r *Runner) crawlOne(provider string, refetchAll bool) error {
	if !r.FullyAdded(context.Background(), provider) {
		if err := r.runAdd(provider); err != nil {
			return err
		}
	}
	return r.runIngest(provider, refetchAll)
}

// StartCrawl is the one entry point for crawling a single provider — the
// Crawl / Add + Crawl actions and every schedule. It claims the provider
// before returning, then crawls in the background, queues a reindex after
// when asked, and hands the result to done (which may be nil). It reports
// false, starting nothing, when that provider is already being crawled.
// refetchAll makes it a full re-crawl (see runIngest).
func (r *Runner) StartCrawl(provider string, reindexAfter, refetchAll bool, done func(error)) bool {
	if !r.claimCrawl(provider) {
		return false
	}
	go func() {
		err := r.crawlOne(provider, refetchAll)
		r.releaseCrawl(provider)
		if reindexAfter {
			r.queueReindex()
		}
		if done != nil {
			done(err)
		}
	}()
	return true
}

// crawlOutcome is the Outcome status of provider's crawl that just
// finished — the newest add-boards or ingest run for it, which is this
// crawl's own, since a provider is never crawled twice at once. It falls
// back to the exit error only when the activity log has no such run.
func (r *Runner) crawlOutcome(provider string, err error) string {
	for _, run := range r.activity.List() { // newest first
		if run.Provider == provider && (run.Action == "ingest" || run.Action == "add-boards") {
			return run.Outcome().Status
		}
	}
	if err != nil {
		return OutcomeFailed
	}
	return OutcomeSuccess
}

// RunBatch is "Add + Crawl Selected": each provider in order, then ONE
// reindex at the end when reindexAfter is set. A provider already being
// crawled elsewhere is skipped rather than crawled a second time alongside.
func (r *Runner) RunBatch(providers []string, reindexAfter bool) error {
	var firstErr error
	for _, p := range providers {
		if !r.claimCrawl(p) {
			log.Printf("batch: %s is already being crawled — skipped", p)
			continue
		}
		err := r.crawlOne(p, false)
		r.releaseCrawl(p)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if reindexAfter {
		r.queueReindex()
	}
	return firstErr
}

// StartCleanup runs freehire's close-chronic-boards in the background:
// apply=false is Preview (report only, changes nothing); apply=true closes
// the jobs of boards unreachable for 60 days AND of boards whose feed has
// been empty for 30 — freehire arms those two passes with separate flags,
// so both are passed — then queues a reindex through the coalesced path.
// A real run is recorded in cleanup.json, which is what the daily slot is
// measured against; a Preview is not.
//
// It reports false, starting nothing, when a cleanup is already running:
// the command is idempotent, so a second copy could only repeat the first.
func (r *Runner) StartCleanup(apply bool) bool {
	if !r.cleanupMu.TryLock() {
		return false
	}
	go func() {
		defer r.cleanupMu.Unlock()
		startedAt := time.Now()
		var err error
		if apply {
			run := r.activity.Start("close-chronic-boards", "")
			err = r.runSubprocess(run, nil, r.bin.CloseChronicBoards, "--apply", "--apply-empty-feed")
		} else {
			run := r.activity.StartLabeled("close-chronic-boards", "", "dry run")
			err = r.runSubprocess(run, nil, r.bin.CloseChronicBoards)
		}
		if !apply {
			return
		}
		status := StatusDone
		if err != nil {
			status = StatusFailed
		}
		if recErr := r.cleanup.Record(startedAt, status); recErr != nil {
			log.Printf("cleanup: record run: %v", recErr)
		}
		r.queueReindex()
		// Closed jobs change company job counts: refresh them too. Skipped
		// (not queued) when a manual refresh is already running — that one
		// reads the same, already-closed rows.
		if r.companyMu.TryLock() {
			_ = r.companyRefresh()
			r.companyMu.Unlock()
		}
	}()
	return true
}

// StartCompanyRefresh runs the company refresh (see companyRefresh) in the
// background. It reports false, starting nothing, when one is already
// running — both steps are idempotent, so a second copy could only repeat it.
func (r *Runner) StartCompanyRefresh() bool {
	if !r.companyMu.TryLock() {
		return false
	}
	go func() {
		defer r.companyMu.Unlock()
		_ = r.companyRefresh()
	}()
	return true
}

// companyRefresh recomputes each company's open-job count and facets in
// Postgres (recount-companies), then rebuilds company search from them
// (reindex-companies) — the second step is what makes the new counts
// visible, since company search only lists companies with open jobs. The
// rebuild waits on heavyMu for any jobs reindex in flight rather than
// letting freehire's advisory lock skip it. The caller holds companyMu.
func (r *Runner) companyRefresh() error {
	if err := r.exec("recount-companies", "", r.bin.RecountCompanies); err != nil {
		return fmt.Errorf("recount-companies: %w", err)
	}
	r.heavyMu.Lock()
	defer r.heavyMu.Unlock()
	if err := r.exec("reindex-companies", "", r.bin.ReindexCompanies); err != nil {
		return fmt.Errorf("reindex-companies: %w", err)
	}
	return nil
}
