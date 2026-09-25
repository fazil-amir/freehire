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
	system   *SystemStore
	bin      Binaries

	crawlMu  sync.Mutex
	crawling map[string]bool // providers with a crawl in flight — see claimCrawl

	// OnCrawlFinished, when set, hears about every finished crawl of a
	// provider — click, schedule or bulk run — with its outcome, so the
	// schedules can count it (ScheduleStore.RecordProviderCrawl).
	OnCrawlFinished func(provider string, finishedAt time.Time, outcome string)

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
	// reindexPending are the jobs waiting for a reindex: the next reindex run
	// serves ALL of them, and becomes a step of each (see jobs.go).
	reindexPending []int
}

// Binaries holds the paths to the freehire worker binaries, overridable for
// local (non-container) testing.
type Binaries struct {
	BulkAddBoards      string
	AddBoard           string
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
		AddBoard:           "/app/add-board",
		RecountCompanies:   "/app/recount-companies",
		ReindexCompanies:   "/app/reindex-companies",
		CSVPath:            "/app/data/combined_boards.csv",
	}
}

func NewRunner(csv *CSVStore, activity *ActivityLog, db *DBStore, system *SystemStore, bin Binaries) *Runner {
	return &Runner{
		csv:       csv,
		activity:  activity,
		db:        db,
		system:    system,
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
// Run and finishes the Run with its result — see execInto.
func (r *Runner) runSubprocess(run *Run, extraEnv []string, name string, args ...string) error {
	err := r.execInto(run, extraEnv, name, args...)
	r.activity.Finish(run, err)
	return err
}

// execInto runs one subprocess against an already-created (running) Run,
// streaming its stdout/stderr as they're written (not buffering until
// exit) so the Activity page can show a live-updating log while the
// command is still going. It does NOT finish the Run, so several commands
// can write into one (see StartRemoveProvider).
//
// extraEnv (KEY=value) is added on top of board-console's own environment,
// which every subprocess inherits — CATALOGUE_TECH_ONLY included.
func (r *Runner) execInto(run *Run, extraEnv []string, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
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

	return cmd.Wait()
}

// exec runs one subprocess immediately, with its own Activity row starting
// straight in "running" — used for add-boards and reindex, neither of
// which is concurrency-limited the way ingest is.
func (r *Runner) exec(action, provider string, jobs []int, name string, args ...string) error {
	run := r.activity.Start(action, provider, jobs...)
	return r.runSubprocess(run, nil, name, args...)
}

// execIngestQueued creates the Activity row up front as "queued" — visible
// the instant it's requested — and only flips it to "running" once a
// concurrency slot is actually free, so a click that arrives while
// maxConcurrentIngest ingests are already busy still shows up immediately
// instead of looking silently dropped.
func (r *Runner) execIngestQueued(provider, label string, job int, extraEnv []string, name string, args ...string) error {
	run := r.activity.StartQueued("ingest", provider, label, job)
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
func (r *Runner) runAdd(provider string, job int) error {
	if err := r.exec("add-boards", provider, []int{job}, r.bin.BulkAddBoards,
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
func (r *Runner) runIngest(provider string, refetchAll bool, job int) error {
	label, env := "", []string(nil)
	if refetchAll {
		label, env = "full re-crawl", []string{"INGEST_REFETCH_ALL=1"}
	}
	if err := r.execIngestQueued(provider, label, job, env, r.bin.Ingest, provider); err != nil {
		return fmt.Errorf("ingest %s: %w", provider, err)
	}
	return nil
}

// runReindexNow actually runs the reindex subprocess, as a step of every job
// in jobs — call queueReindex instead, which is what coalesces concurrent
// requests into this.
func (r *Runner) runReindexNow(jobs []int) error {
	r.heavyMu.Lock()
	defer r.heavyMu.Unlock()
	if err := r.exec("reindex", "", jobs, r.bin.Reindex); err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	return nil
}

// queueReindex triggers a reindex for jobs, coalesced with any already in
// flight: never two running at once, and every request that arrives while
// one runs is served by ONE more run right after it — a full-catalog
// rebuild the second call would have started is redundant with the one
// that follows anyway. That next run is a step of every job waiting on it,
// which is how one reindex shows under two crawls ("shared with …").
// Always returns immediately — the actual reindex(es) run in their own
// goroutine.
func (r *Runner) queueReindex(jobs ...int) {
	if len(jobs) == 0 { // a reindex nobody else asked for is its own job
		jobs = []int{r.activity.NewJob()}
	}
	r.reindexMu.Lock()
	r.reindexPending = append(r.reindexPending, jobs...)
	if r.reindexRunning {
		r.reindexMu.Unlock()
		return
	}
	r.reindexRunning = true
	r.reindexMu.Unlock()

	go r.drainReindex()
}

func (r *Runner) drainReindex() {
	for {
		r.reindexMu.Lock()
		if len(r.reindexPending) == 0 {
			r.reindexRunning = false
			r.reindexMu.Unlock()
			return
		}
		jobs := r.reindexPending
		r.reindexPending = nil
		r.reindexMu.Unlock()

		_ = r.runReindexNow(jobs)
	}
}

// Crawling reports whether a crawl (add-boards and/or ingest) of provider
// is in flight, from any caller.
func (r *Runner) Crawling(provider string) bool {
	r.crawlMu.Lock()
	defer r.crawlMu.Unlock()
	return r.crawling[provider]
}

// CrawlCount is how many providers are being crawled right now, from any
// caller — what the scheduler holds to SCHEDULE_CAPACITY.
func (r *Runner) CrawlCount() int {
	r.crawlMu.Lock()
	defer r.crawlMu.Unlock()
	return len(r.crawling)
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
func (r *Runner) crawlOne(provider string, refetchAll bool, job int) error {
	if !r.FullyAdded(context.Background(), provider) {
		if err := r.runAdd(provider, job); err != nil {
			return err
		}
	}
	return r.runIngest(provider, refetchAll, job)
}

// StartCrawl is the one entry point for crawling a single provider — the
// Crawl / Add + Crawl actions and every schedule. It claims the provider
// before returning, then crawls in the background, queues a reindex after
// when asked, and hands the result to done (which may be nil). It reports
// false, starting nothing, when that provider is already being crawled.
// refetchAll makes it a full re-crawl (see runIngest).
func (r *Runner) StartCrawl(provider string, reindexAfter, refetchAll bool, done func(error)) bool {
	_, ok := r.StartCrawlJob(provider, reindexAfter, refetchAll, done)
	return ok
}

// StartCrawlJob is StartCrawl that also returns the crawl's job ID — the
// scheduler keeps it to include the crawl in the hourly batched reindex.
func (r *Runner) StartCrawlJob(provider string, reindexAfter, refetchAll bool, done func(error)) (int, bool) {
	if !r.claimCrawl(provider) {
		return 0, false
	}
	job := r.activity.NewJob()
	go func() {
		err := r.crawlOne(provider, refetchAll, job)
		r.crawlFinished(provider, err)
		r.releaseCrawl(provider)
		if reindexAfter {
			r.queueReindex(job)
		}
		if done != nil {
			done(err)
		}
	}()
	return job, true
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

// StartRemoveProvider is "Remove provider": it retires every live board of
// provider through freehire's own add-board --retire (status 'retired',
// row kept, health cleared — nothing deleted, jobs untouched), one call
// per board, all streamed into ONE Activity run.
//
// It holds the provider's crawl lock throughout, so no crawl or schedule
// can re-add a board mid-removal, and reports false, starting nothing,
// when the provider is already being crawled. deleteSchedules runs once
// the lock is held and returns how many of the provider's schedules it
// removed — without that, the next scheduled crawl would re-add every
// board (a crawl adds what is missing), silently undoing the removal.
//
// purge runs once every board is retired, still under the lock: it forgets
// what Board Console holds about the provider — its activity, this run
// included. A removal that could not retire every board purges nothing, so
// its log stays to show what failed.
func (r *Runner) StartRemoveProvider(provider string, boards []liveBoard, deleteSchedules func() int, purge func()) bool {
	if !r.claimCrawl(provider) {
		return false
	}
	schedules := deleteSchedules()
	run := r.activity.Start("remove-boards", provider, r.activity.NewJob())
	go func() {
		defer r.releaseCrawl(provider)
		retired, failed := 0, 0
		for _, b := range boards {
			args := []string{"--retire", "--provider=" + provider, "--board=" + b.Board, "--apply"}
			if b.Region != "" {
				args = append(args, "--region="+b.Region)
			}
			if err := r.execInto(run, nil, r.bin.AddBoard, args...); err != nil {
				failed++ // e.g. already retired by someone else — keep going
				continue
			}
			retired++
		}
		// The summary line Outcome reads (see removeDoneRe).
		r.activity.AppendOutput(run, []byte(fmt.Sprintf(
			"remove-boards: done. retired=%d failed=%d schedules_deleted=%d\n", retired, failed, schedules)), true)
		var err error
		if failed > 0 {
			err = fmt.Errorf("%d of %d boards could not be retired", failed, len(boards))
		}
		r.activity.Finish(run, err)
		if err == nil && purge != nil {
			purge()
		}
	}()
	return true
}

// crawlFinished reports a finished crawl to OnCrawlFinished. Called while
// the provider's claim is still held, so a schedule cannot see the provider
// free before its clock has been reset.
func (r *Runner) crawlFinished(provider string, err error) {
	if r.OnCrawlFinished != nil {
		r.OnCrawlFinished(provider, time.Now().Round(0), r.crawlOutcome(provider, err))
	}
}

// RunBatch is "Add + Crawl Selected": each provider in order, then ONE
// reindex at the end when reindexAfter is set. A provider already being
// crawled elsewhere is skipped rather than crawled a second time alongside.
func (r *Runner) RunBatch(providers []string, reindexAfter bool) error {
	var firstErr error
	var jobs []int
	for _, p := range providers {
		if !r.claimCrawl(p) {
			log.Printf("batch: %s is already being crawled — skipped", p)
			continue
		}
		job := r.activity.NewJob()
		jobs = append(jobs, job)
		err := r.crawlOne(p, false, job)
		r.crawlFinished(p, err)
		r.releaseCrawl(p)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if reindexAfter && len(jobs) > 0 {
		r.queueReindex(jobs...)
	}
	return firstErr
}

// StartCleanup runs freehire's close-chronic-boards in the background:
// apply=false is Preview (report only, changes nothing); apply=true closes
// the jobs of boards unreachable for 60 days AND of boards whose feed has
// been empty for 30 — freehire arms those two passes with separate flags,
// so both are passed — then queues a reindex through the coalesced path.
// A real run is recorded in system.json, which is what its daily slot is
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
		job := r.activity.NewJob()
		var err error
		if apply {
			run := r.activity.Start("close-chronic-boards", "", job)
			err = r.runSubprocess(run, nil, r.bin.CloseChronicBoards, "--apply", "--apply-empty-feed")
		} else {
			run := r.activity.StartLabeled("close-chronic-boards", "", "dry run", job)
			err = r.runSubprocess(run, nil, r.bin.CloseChronicBoards)
		}
		if !apply {
			return
		}
		status := StatusDone
		if err != nil {
			status = StatusFailed
		}
		if recErr := r.system.Record(sysCleanup, startedAt, status); recErr != nil {
			log.Printf("cleanup: record run: %v", recErr)
		}
		r.queueReindex(job)
		// Closed jobs change company job counts: refresh them too. Skipped
		// (not queued) when a manual refresh is already running — that one
		// reads the same, already-closed rows.
		if r.companyMu.TryLock() {
			_ = r.companyRefresh(job)
			r.companyMu.Unlock()
		}
	}()
	return true
}

// StartCompanyRefresh runs the company refresh (see companyRefresh) in the
// background — the "Recount companies" system job, whether its daily time
// came or it was started by hand; either way the run is recorded as the
// job's last. It reports false, starting nothing, when one is already
// running — both steps are idempotent, so a second copy could only repeat it.
func (r *Runner) StartCompanyRefresh() bool {
	if !r.companyMu.TryLock() {
		return false
	}
	go func() {
		defer r.companyMu.Unlock()
		startedAt := time.Now().Round(0)
		status := StatusDone
		if err := r.companyRefresh(r.activity.NewJob()); err != nil {
			status = StatusFailed
		}
		if err := r.system.Record(sysRecount, startedAt, status); err != nil {
			log.Printf("recount companies: record run: %v", err)
		}
	}()
	return true
}

// companyRefresh recomputes each company's open-job count and facets in
// Postgres (recount-companies), then rebuilds company search from them
// (reindex-companies) — the second step is what makes the new counts
// visible, since company search only lists companies with open jobs. The
// rebuild waits on heavyMu for any jobs reindex in flight rather than
// letting freehire's advisory lock skip it. The caller holds companyMu.
func (r *Runner) companyRefresh(job int) error {
	if err := r.exec("recount-companies", "", []int{job}, r.bin.RecountCompanies); err != nil {
		return fmt.Errorf("recount-companies: %w", err)
	}
	r.heavyMu.Lock()
	defer r.heavyMu.Unlock()
	if err := r.exec("reindex-companies", "", []int{job}, r.bin.ReindexCompanies); err != nil {
		return fmt.Errorf("reindex-companies: %w", err)
	}
	return nil
}
