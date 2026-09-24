package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RunStatus string

const (
	// StatusQueued is an ingest run's state between the click that
	// triggered it and the moment a concurrency slot actually frees up for
	// it — the row exists and is visible the instant it's requested, never
	// silently dropped while it waits.
	StatusQueued  RunStatus = "queued"
	StatusRunning RunStatus = "running"
	StatusDone    RunStatus = "done"
	StatusFailed  RunStatus = "failed"
)

// activityFileMaxLines and activityFileMaxBytes bound data/activity.jsonl
// from two directions: a busy install accumulating many short runs hits
// the line cap first, while a few runs with unusually large captured
// output hit the byte cap first. Enforced both at startup and, cheaply, on
// every persist while running — not just at startup — so a long-lived
// process can't grow the file unbounded between restarts.
const activityFileMaxLines = 10_000
const activityFileMaxBytes = 50 * 1024 * 1024 // 50MB, hard ceiling

// runOutputMaxBytes caps how much of a single run's stdout/stderr is kept,
// in memory and on disk alike — the front is dropped, keeping the tail,
// since recent output (progress near the end, or the error that ended it)
// is what a huge dump is almost always read for. Without this, one
// pathological run (a runaway process, or a provider whose crawl logs
// every row) could by itself blow the whole file's byte budget.
const runOutputMaxBytes = 2 * 1024 * 1024 // 2MB per stream, per run

// activityPersistThrottle bounds how often a still-running Run's growing
// stdout/stderr gets written to disk. It exists only to recover partial
// output if board-console itself crashes mid-run — the authoritative,
// complete record is always the one written at Finish — so it's
// deliberately coarse rather than persisting on every output chunk.
const activityPersistThrottle = 10 * time.Second

// Run is one recorded subprocess invocation shown on the Activity page.
type Run struct {
	ID       int
	Action   string // "add-boards" / "ingest" / "reindex" / "close-chronic-boards"
	Provider string // empty for reindex and close-chronic-boards
	Label    string // shown beside the action, e.g. "dry run"; usually empty
	// Jobs are the job(s) this run is a step of — one crawl, cleanup, ... as
	// the operator asked for it (see jobs.go). Usually one; a reindex that
	// served several crawls at once belongs to each. Empty for runs recorded
	// before jobs existed, which show as single-step jobs.
	Jobs       []int `json:",omitempty"`
	Status     RunStatus
	Stdout     string
	Stderr     string
	Err        string
	StartedAt  time.Time
	FinishedAt time.Time

	lastPersist time.Time // unexported: throttling state, never persisted
}

func (r *Run) Duration() time.Duration {
	if r.FinishedAt.IsZero() {
		return time.Since(r.StartedAt).Round(time.Second)
	}
	return r.FinishedAt.Sub(r.StartedAt).Round(time.Second)
}

// ActivityLog is an in-memory ring buffer of the last maxRuns runs, backed
// by data/activity.jsonl so history survives a restart — a working log,
// still not an audit trail (the file is trimmed, never guaranteed complete).
type ActivityLog struct {
	mu        sync.Mutex
	runs      []*Run
	nextID    int
	nextJob   int
	maxRuns   int
	file      *os.File
	path      string
	lineCount int // raw lines currently in the file — cheap running tally
}

// NewActivityLog loads existing history from <dataDir>/activity.jsonl (if
// any), trims that file if it's grown past activityFileMaxLines or
// activityFileMaxBytes, and opens it for append so every future
// Start/Finish/AppendOutput is durable.
func NewActivityLog(dataDir string, maxRuns int) (*ActivityLog, error) {
	path := filepath.Join(dataDir, "activity.jsonl")

	trimActivityFile(path, activityFileMaxLines, activityFileMaxBytes)
	runs, maxID, lineCount := loadActivityRuns(path, maxRuns)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}

	a := &ActivityLog{runs: runs, nextID: maxID, maxRuns: maxRuns, file: f, path: path, lineCount: lineCount}
	for _, r := range runs {
		for _, j := range r.Jobs {
			a.nextJob = max(a.nextJob, j)
		}
	}

	// A run still "running" OR "queued" in the file means the process was
	// killed or crashed mid-run (or mid-wait — the concurrency queue only
	// ever lived in memory) — the subprocess is gone either way, so mark
	// it failed rather than leaving it stuck forever, which would also
	// wedge the Activity page's "keep polling while anything is running"
	// logic.
	a.mu.Lock()
	for _, r := range a.runs {
		if r.Status == StatusRunning || r.Status == StatusQueued {
			r.Status = StatusFailed
			r.Err = "interrupted (board-console restarted)"
			r.FinishedAt = time.Now()
			a.persistLocked(r)
		}
	}
	a.mu.Unlock()

	return a, nil
}

// Start records a new running entry and returns it so the caller can stream
// output into it and call Finish.
func (a *ActivityLog) Start(action, provider string, jobs ...int) *Run {
	return a.start(action, provider, "", StatusRunning, jobs)
}

// NewJob hands out the next job ID — one per thing the operator (or a
// schedule) asked for; its steps are the runs started with it.
func (a *ActivityLog) NewJob() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextJob++
	return a.nextJob
}

// StartLabeled is Start with a label shown beside the action on the
// Activity page (a Preview's "dry run").
func (a *ActivityLog) StartLabeled(action, provider, label string, jobs ...int) *Run {
	return a.start(action, provider, label, StatusRunning, jobs)
}

// StartQueued records a new entry as "queued" rather than "running" —
// visible the instant an action is requested, even if it then has to wait
// for a concurrency slot (see Runner's ingest semaphore). Call MarkRunning
// once that slot is actually acquired.
func (a *ActivityLog) StartQueued(action, provider, label string, jobs ...int) *Run {
	return a.start(action, provider, label, StatusQueued, jobs)
}

func (a *ActivityLog) start(action, provider, label string, status RunStatus, jobs []int) *Run {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	r := &Run{
		ID:          a.nextID,
		Action:      action,
		Provider:    provider,
		Label:       label,
		Jobs:        jobs,
		Status:      status,
		StartedAt:   time.Now(),
		lastPersist: time.Now(),
	}
	a.runs = append(a.runs, r)
	if len(a.runs) > a.maxRuns {
		a.runs = a.runs[len(a.runs)-a.maxRuns:]
	}
	a.persistLocked(r)
	return r
}

// MarkRunning transitions a queued run to running once its concurrency
// slot is acquired. StartedAt deliberately stays at the original queue
// time — Duration() is meant to reflect the whole wait-plus-work latency,
// not just the subprocess's own runtime.
func (a *ActivityLog) MarkRunning(r *Run) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r.Status = StatusRunning
	a.persistLocked(r)
}

// AppendOutput appends a chunk of freshly-read subprocess output to the
// run's stdout or stderr, immediately visible to anything reading the Run —
// the live-log view depends on this being cheap and frequent, unlike disk
// persistence, which is throttled separately.
func (a *ActivityLog) AppendOutput(r *Run, chunk []byte, stderr bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if stderr {
		r.Stderr = capOutput(r.Stderr + string(chunk))
	} else {
		r.Stdout = capOutput(r.Stdout + string(chunk))
	}
	if time.Since(r.lastPersist) >= activityPersistThrottle {
		r.lastPersist = time.Now()
		a.persistLocked(r)
	}
}

// capOutput keeps only the last runOutputMaxBytes of a run's captured
// output, prefixed with a marker once anything's been dropped — the tail
// is what an operator reads a huge dump for (recent progress, or the
// error that ended it), not the beginning.
func capOutput(s string) string {
	if len(s) <= runOutputMaxBytes {
		return s
	}
	return "[...earlier output truncated...]\n" + s[len(s)-runOutputMaxBytes:]
}

// Finish records the outcome of a run started with Start and always
// persists the final, authoritative snapshot regardless of the throttle.
func (a *ActivityLog) Finish(r *Run, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r.FinishedAt = time.Now()
	if err != nil {
		r.Status = StatusFailed
		r.Err = err.Error()
	} else {
		r.Status = StatusDone
	}
	a.persistLocked(r)
}

// persistLocked writes one JSON-Lines snapshot of r, then checks (cheaply —
// an fstat, no read) whether the file needs trimming. Callers must hold
// a.mu.
func (a *ActivityLog) persistLocked(r *Run) {
	if a.file == nil {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		log.Printf("activity log: marshal run %d: %v", r.ID, err)
		return
	}
	data = append(data, '\n')
	if _, err := a.file.Write(data); err != nil {
		log.Printf("activity log: persist run %d: %v", r.ID, err)
		return
	}
	a.lineCount++
	a.maybeTrimLocked()
}

// maybeTrimLocked enforces activityFileMaxLines/activityFileMaxBytes at
// runtime, not just at startup — a process that runs for weeks without a
// restart would otherwise grow the file unbounded. The byte check is a
// cheap fstat, done only when the (already-tracked, no-syscall) line count
// hasn't already tripped the trim, so this costs nothing on the vast
// majority of persists. Callers must hold a.mu.
func (a *ActivityLog) maybeTrimLocked() {
	needsTrim := a.lineCount > activityFileMaxLines
	if !needsTrim {
		if info, err := a.file.Stat(); err == nil && info.Size() > activityFileMaxBytes {
			needsTrim = true
		}
	}
	if !needsTrim {
		return
	}

	if err := a.file.Close(); err != nil {
		log.Printf("activity log: close before trim: %v", err)
		return
	}
	trimActivityFile(a.path, activityFileMaxLines, activityFileMaxBytes)

	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("activity log: reopen after trim: %v", err)
		a.file = nil // persistLocked no-ops on a nil file rather than panicking
		return
	}
	a.file = f
	a.lineCount = countLines(a.path)
}

// List returns runs newest-first.
//
// Each entry is a snapshot COPY taken under the lock, not the live Run: a
// running Run keeps being written (output appended, status finished) by its
// subprocess goroutine, and a page rendering the live struct meanwhile would
// race it. The copy is cheap — Stdout/Stderr are strings, so only their
// headers are copied, never the bytes.
func (a *ActivityLog) List() []*Run {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*Run, len(a.runs))
	for i, r := range a.runs {
		snapshot := *r
		out[len(a.runs)-1-i] = &snapshot
	}
	return out
}

// Get returns a snapshot of one run by ID (see List on why a copy).
func (a *ActivityLog) Get(id int) (*Run, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.runs {
		if r.ID == id {
			snapshot := *r
			return &snapshot, true
		}
	}
	return nil, false
}

// AnyRunning reports whether at least one run is still in progress — drives
// whether the Activity page keeps polling.
func (a *ActivityLog) AnyRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.runs {
		if r.Status == StatusRunning || r.Status == StatusQueued {
			return true
		}
	}
	return false
}

// trimActivityFile keeps data/activity.jsonl within both bounds: first the
// line count (drop the oldest lines beyond maxLines), then — only if that
// alone wasn't enough, which only happens when the kept lines' own output
// is large despite runOutputMaxBytes — the byte budget, dropping further
// oldest lines until under it. A no-op, so cheap to call speculatively,
// when neither bound is exceeded.
func trimActivityFile(path string, maxLines int, maxBytes int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // no file yet, or unreadable — nothing to trim
	}
	lines := splitLines(data)
	original := len(lines)

	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	var total int64
	for _, l := range lines {
		total += int64(len(l)) + 1
	}
	for total > maxBytes && len(lines) > 1 {
		total -= int64(len(lines[0])) + 1
		lines = lines[1:]
	}

	if len(lines) == original {
		return // neither bound was exceeded
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "activity-*.tmp")
	if err != nil {
		log.Printf("activity log: trim: %v", err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed

	w := bufio.NewWriter(tmp)
	for _, line := range lines {
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		log.Printf("activity log: trim flush: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		log.Printf("activity log: trim close: %v", err)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		log.Printf("activity log: trim rename: %v", err)
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// countLines is a cheap post-trim recount — reads once, doesn't unmarshal
// anything, unlike loadActivityRuns.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n
}

// loadActivityRuns replays activity.jsonl: since it's an append-only log of
// snapshots keyed by run ID, the LATEST snapshot for a given ID is that
// run's current state. Order is preserved by first-seen ID, which equals
// chronological order since IDs only increase and the file is append-only.
// It also returns the raw line count (distinct from len(runs), since one
// run can have several snapshot lines), seeding the running tally
// maybeTrimLocked uses to avoid re-scanning the file on every persist.
func loadActivityRuns(path string, maxRuns int) (runs []*Run, maxID int, rawLines int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0
	}
	defer f.Close()

	byID := map[int]*Run{}
	var order []int

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // a run's captured output can be large
	for scanner.Scan() {
		rawLines++
		var r Run
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue // tolerate a corrupt/truncated trailing line
		}
		if _, exists := byID[r.ID]; !exists {
			order = append(order, r.ID)
		}
		rr := r
		byID[r.ID] = &rr
		if r.ID > maxID {
			maxID = r.ID
		}
	}

	runs = make([]*Run, 0, len(order))
	for _, id := range order {
		runs = append(runs, byID[id])
	}
	if len(runs) > maxRuns {
		runs = runs[len(runs)-maxRuns:]
	}
	return runs, maxID, rawLines
}
