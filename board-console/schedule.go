package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// minScheduleInterval is a floor against typos, not against overlap: the
// Scheduler never starts a schedule whose previous run is still in flight,
// so a crawl that outlasts its own interval simply runs again on the next
// tick after it finishes rather than stacking a second copy.
const minScheduleInterval = 2 * time.Minute

// Schedule is one recurring crawl job, persisted to schedule.json.
type Schedule struct {
	ID           string    `json:"id"`
	Provider     string    `json:"provider"`
	IntervalSecs int64     `json:"interval_seconds"`
	ReindexAfter bool      `json:"reindex_after"`
	Enabled      bool      `json:"enabled"`
	LastRun      time.Time `json:"last_run,omitzero"`
	// "running", then the finished crawl's Outcome status — "success" /
	// "partial" / "failed" — or "interrupted". Files written before Outcome
	// existed hold "ok", which reads as "success".
	LastStatus string `json:"last_status,omitempty"`

	// LastFinished is when the schedule's OWN last run ended, whatever its
	// outcome — so a failing provider waits a full interval, not a minute.
	LastFinished time.Time `json:"last_finished,omitzero"`
	// LastCrawlEnd is when the provider's last crawl that got something done
	// (success or partial) ended, whoever started it — a Crawl click, a bulk
	// run or this schedule. It is what stops a schedule re-crawling a
	// provider that was crawled by hand a minute ago.
	LastCrawlEnd time.Time `json:"last_crawl_end,omitzero"`
}

// clockStart is when the schedule's interval is counted from: the latest of
// its own run's start, its own run's end, and the provider's last good
// crawl's end. Files written before the two end stamps existed only have
// LastRun, which the max simply falls back to.
func (s Schedule) clockStart() time.Time {
	t := s.LastRun
	for _, c := range []time.Time{s.LastFinished, s.LastCrawlEnd} {
		if c.After(t) {
			t = c
		}
	}
	return t
}

func (s Schedule) Interval() time.Duration {
	return time.Duration(s.IntervalSecs) * time.Second
}

// NextRun reports when a schedule that has already run once will run
// again. It deliberately does NOT resolve "never run yet" to time.Now():
// that would make it a moving target — every call returns a slightly later
// instant than the last — and comparing a fixed "now" against a target
// that keeps re-evaluating to "later than now" means Due() could never
// return true. Callers must check LastRun.IsZero() themselves (Due does,
// below); the zero time.Time this returns in that case is a sentinel, not
// a real schedule, and the template renders it as "due now" rather than
// formatting it.
func (s Schedule) NextRun() time.Time {
	start := s.clockStart()
	if start.IsZero() {
		return time.Time{}
	}
	return start.Add(s.Interval())
}

func (s Schedule) Due(now time.Time) bool {
	if !s.Enabled {
		return false
	}
	if s.clockStart().IsZero() {
		return true // never run — due immediately
	}
	return !now.Before(s.NextRun())
}

// ScheduleStore holds schedule.json in memory and persists every change
// atomically, the same temp-file-then-rename pattern the CSV store uses.
type ScheduleStore struct {
	path string

	mu        sync.Mutex
	schedules []Schedule
}

func NewScheduleStore(path string) (*ScheduleStore, error) {
	s := &ScheduleStore{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ScheduleStore) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.schedules = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var schedules []Schedule
	if err := json.Unmarshal(data, &schedules); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	// "running" on disk means board-console stopped mid-run: the subprocess
	// died with it, so the run did not finish.
	for i := range schedules {
		if schedules[i].LastStatus == "running" {
			schedules[i].LastStatus = "interrupted"
		}
	}
	s.schedules = schedules
	return nil
}

func (s *ScheduleStore) save() error {
	data, err := json.MarshalIndent(s.schedules, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "schedule-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func (s *ScheduleStore) List() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Schedule, len(s.schedules))
	copy(out, s.schedules)
	return out
}

// Add validates and appends a new schedule. Returns an error the form can
// show inline if the interval is below minScheduleInterval.
func (s *ScheduleStore) Add(provider string, interval time.Duration, reindexAfter bool) error {
	if interval < minScheduleInterval {
		return fmt.Errorf("interval must be at least %s", minScheduleInterval)
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.schedules = append(s.schedules, Schedule{
		ID:           id,
		Provider:     provider,
		IntervalSecs: int64(interval.Seconds()),
		ReindexAfter: reindexAfter,
		Enabled:      true,
	})
	s.mu.Unlock()
	return s.save()
}

// Get returns one schedule by ID, for pre-filling the edit modal.
func (s *ScheduleStore) Get(id string) (Schedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sch := range s.schedules {
		if sch.ID == id {
			return sch, true
		}
	}
	return Schedule{}, false
}

// Update overwrites an existing schedule's provider/interval/reindex-after
// in place (same ID), the same validation Add applies. LastRun/LastStatus
// are left untouched — editing a schedule's settings isn't a fresh run of
// it.
func (s *ScheduleStore) Update(id, provider string, interval time.Duration, reindexAfter bool) error {
	if interval < minScheduleInterval {
		return fmt.Errorf("interval must be at least %s", minScheduleInterval)
	}
	s.mu.Lock()
	found := false
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].Provider = provider
			s.schedules[i].IntervalSecs = int64(interval.Seconds())
			s.schedules[i].ReindexAfter = reindexAfter
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("schedule %s not found", id)
	}
	return s.save()
}

// Delete removes a schedule entirely.
func (s *ScheduleStore) Delete(id string) error {
	s.mu.Lock()
	idx := -1
	for i, sch := range s.schedules {
		if sch.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.mu.Unlock()
		return fmt.Errorf("schedule %s not found", id)
	}
	s.schedules = append(s.schedules[:idx], s.schedules[idx+1:]...)
	s.mu.Unlock()
	return s.save()
}

// DeleteByProvider removes every schedule of provider and reports how many.
func (s *ScheduleStore) DeleteByProvider(provider string) (int, error) {
	s.mu.Lock()
	kept := s.schedules[:0:0]
	removed := 0
	for _, sch := range s.schedules {
		if sch.Provider == provider {
			removed++
			continue
		}
		kept = append(kept, sch)
	}
	s.schedules = kept
	s.mu.Unlock()
	if removed == 0 {
		return 0, nil
	}
	return removed, s.save()
}

func (s *ScheduleStore) Toggle(id string) error {
	s.mu.Lock()
	found := false
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].Enabled = !s.schedules[i].Enabled
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("schedule %s not found", id)
	}
	return s.save()
}

// markStarted stamps a run's START as the schedule's last run, with status
// "running". The interval is measured from here — an every-15m schedule
// fires 15 minutes after its previous run began, not after it ended.
func (s *ScheduleStore) markStarted(id string, startedAt time.Time) {
	s.mu.Lock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].LastRun = startedAt
			s.schedules[i].LastStatus = "running"
			break
		}
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Printf("schedule store: persist run start: %v", err)
	}
}

// restoreRun puts back a schedule's previous last-run stamp, undoing a
// markStarted whose crawl never began.
func (s *ScheduleStore) restoreRun(id string, lastRun time.Time, status string) {
	s.mu.Lock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].LastRun = lastRun
			s.schedules[i].LastStatus = status
			break
		}
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Printf("schedule store: persist restore: %v", err)
	}
}

// recordRun stamps the outcome of a finished run and persists. LastRun is
// left at the start markStarted recorded.
func (s *ScheduleStore) recordRun(id, status string, finishedAt time.Time) {
	s.mu.Lock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].LastStatus = status
			s.schedules[i].LastFinished = finishedAt
			break
		}
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Printf("schedule store: persist after run: %v", err)
	}
}

// RecordProviderCrawl stamps LastCrawlEnd on every schedule of provider when
// a crawl of it — from any source — ended with jobs to show for it. A crawl
// that failed outright stamps nothing: it must not push the next real
// crawl a whole interval away.
func (s *ScheduleStore) RecordProviderCrawl(provider string, finishedAt time.Time, outcome string) {
	if outcome != OutcomeSuccess && outcome != OutcomePartial {
		return
	}
	s.mu.Lock()
	changed := false
	for i := range s.schedules {
		if s.schedules[i].Provider == provider && finishedAt.After(s.schedules[i].LastCrawlEnd) {
			s.schedules[i].LastCrawlEnd = finishedAt
			changed = true
		}
	}
	s.mu.Unlock()
	if !changed {
		return
	}
	if err := s.save(); err != nil {
		log.Printf("schedule store: persist crawl end: %v", err)
	}
}

func randomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Scheduler ticks once a minute: it starts the built-in daily dead-board
// cleanup when its 03:00 slot is unserved (see cleanup.go), and starts every
// due schedule through Runner.StartCrawl.
//
// Each schedule runs in the background, so one slow crawl never holds up
// another (the ingest semaphore bounds how many crawl at once). A schedule
// never overlaps a crawl of its provider — its own previous run or a manual
// one: while one is in flight it is skipped, and it starts on the first
// tick after that crawl finishes.
type Scheduler struct {
	store  *ScheduleStore
	runner *Runner
	tick   time.Duration
}

func NewScheduler(store *ScheduleStore, runner *Runner) *Scheduler {
	return &Scheduler{store: store, runner: runner, tick: time.Minute}
}

// Run blocks, ticking until ctx-less forever (the process lifetime) — call
// it in its own goroutine.
func (s *Scheduler) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.runDue()
		}
	}
}

func (s *Scheduler) runDue() {
	// Round(0) strips the monotonic reading, so every comparison below is on
	// the wall clock. Go compares two times that BOTH carry one by the
	// monotonic clock, which stops while the host sleeps — a LastRun stamped
	// in-process before a 2-hour sleep would then look minutes old on wake,
	// and the schedule would run late by the length of the sleep.
	now := time.Now().Round(0)

	// The built-in daily dead-board cleanup. Started in the background; if
	// one is already running (a "Run now"), StartCleanup declines and a
	// later tick tries again.
	if cleanupDue(s.runner.cleanup.State().LastRun, now) {
		if s.runner.StartCleanup(true) {
			log.Printf("scheduler: daily dead-board cleanup started")
		}
	}

	for _, sch := range s.store.List() {
		if !sch.Due(now) || s.runner.Crawling(sch.Provider) {
			continue
		}
		s.store.markStarted(sch.ID, now)
		id, provider := sch.ID, sch.Provider
		started := s.runner.StartCrawl(sch.Provider, sch.ReindexAfter, false, func(err error) {
			if err != nil {
				log.Printf("scheduler: %s: %v", id, err)
			}
			s.store.recordRun(id, s.runner.crawlOutcome(provider, err), time.Now().Round(0))
		})
		if !started {
			// A crawl of this provider began between the check and the claim:
			// put the stamp back, and a later tick tries again.
			s.store.restoreRun(sch.ID, sch.LastRun, sch.LastStatus)
			continue
		}
		log.Printf("scheduler: running %s (provider=%s)", sch.ID, sch.Provider)
	}
}
