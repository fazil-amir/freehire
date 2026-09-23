package main

import (
	"context"
	"fmt"
	"io"
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
	bin      Binaries

	ingestSem chan struct{} // buffered to maxConcurrentIngest

	reindexMu      sync.Mutex // guards the two fields below, not subprocess execution itself
	reindexRunning bool
	reindexAgain   bool // a request arrived while one was already running — run once more after
}

// Binaries holds the paths to the freehire worker binaries, overridable for
// local (non-container) testing.
type Binaries struct {
	BulkAddBoards string
	Ingest        string
	Reindex       string
	CSVPath       string
}

func DefaultBinaries() Binaries {
	return Binaries{
		BulkAddBoards: "/app/bulk-add-boards",
		Ingest:        "/app/ingest",
		Reindex:       "/app/reindex",
		CSVPath:       "/app/data/combined_boards.csv",
	}
}

func NewRunner(csv *CSVStore, activity *ActivityLog, db *DBStore, bin Binaries) *Runner {
	return &Runner{
		csv:       csv,
		activity:  activity,
		db:        db,
		bin:       bin,
		ingestSem: make(chan struct{}, maxConcurrentIngest),
	}
}

// FullyAdded reports whether every one of a provider's CSV candidate rows
// is already added — read from Postgres (the live, correct answer) when
// reachable, falling back to the CSV's own frozen `added` column
// otherwise. Used to decide whether a Crawl action needs an add-boards
// step first; a false positive here just means an extra, idempotent
// add-boards call, never a skipped one.
func (r *Runner) FullyAdded(ctx context.Context, provider string) bool {
	total := 0
	for _, row := range r.csv.Rows() {
		if row.Provider == provider {
			total++
		}
	}
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
func (r *Runner) runSubprocess(run *Run, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)

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
	return r.runSubprocess(run, name, args...)
}

// execIngestQueued creates the Activity row up front as "queued" — visible
// the instant it's requested — and only flips it to "running" once a
// concurrency slot is actually free, so a click that arrives while
// maxConcurrentIngest ingests are already busy still shows up immediately
// instead of looking silently dropped.
func (r *Runner) execIngestQueued(provider, name string, args ...string) error {
	run := r.activity.StartQueued("ingest", provider)
	r.ingestSem <- struct{}{}
	defer func() { <-r.ingestSem }()
	r.activity.MarkRunning(run)
	return r.runSubprocess(run, name, args...)
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
// limit if it's currently saturated.
func (r *Runner) runIngest(provider string) error {
	if err := r.execIngestQueued(provider, r.bin.Ingest, provider); err != nil {
		return fmt.Errorf("ingest %s: %w", provider, err)
	}
	return nil
}

// runReindexNow actually runs the reindex subprocess — call queueReindex
// instead, which is what coalesces concurrent requests into this.
func (r *Runner) runReindexNow() error {
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

// RunSingleCrawl is the single-row "Crawl" action: ingest then queue a
// reindex, for a provider that's already fully added.
func (r *Runner) RunSingleCrawl(provider string) error {
	err := r.runIngest(provider)
	r.queueReindex()
	return err
}

// RunBatch is the shared batching rule used by a scheduled run (and, via
// RunAddAndCrawlOne, the single-row "Add + Crawl" action): add (if not
// already fully added) + ingest for each provider in order, then queue
// ONE reindex at the end — only if reindexAfter is set. Concurrency across
// SEPARATE calls to RunBatch/RunSingleCrawl (e.g. two different button
// clicks) is what the ingest semaphore provides; within one call the
// providers still run sequentially.
func (r *Runner) RunBatch(providers []string, reindexAfter bool) error {
	ctx := context.Background()

	var firstErr error
	for _, p := range providers {
		if !r.FullyAdded(ctx, p) {
			if err := r.runAdd(p); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue // still attempt to crawl what we can of the rest
			}
		}
		if err := r.runIngest(p); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if reindexAfter {
		r.queueReindex()
	}
	return firstErr
}

// RunAddAndCrawlOne is the single-row "Add + Crawl" action for a provider
// not yet fully added — its own one-provider batch, always reindexing
// after.
func (r *Runner) RunAddAndCrawlOne(provider string) error {
	return r.RunBatch([]string{provider}, true)
}
