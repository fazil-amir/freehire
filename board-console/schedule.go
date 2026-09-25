package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Schedule is one provider's recurring crawls, persisted to schedule.json:
// the provider is crawled once at each of Times, every day — times the
// operator picked one by one. A provider has at most one schedule.
type Schedule struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// Times are the day's runs, in minutes after 00:00 UTC: on the 15-minute
	// grid, unique, sorted.
	Times   []int `json:"times"`
	Enabled bool  `json:"enabled"`
	// The older shapes a file may still carry — "N a day from a first run",
	// and before that "every N" — converted to Times on load and not used
	// afterwards.
	PerDay       int   `json:"per_day,omitempty"`
	OffsetMin    int   `json:"offset_minutes,omitempty"`
	IntervalSecs int64 `json:"interval_seconds,omitempty"`

	// LastSlot is the planned run the schedule last served (ran, or found
	// already covered). A run is due once it is later than this.
	LastSlot time.Time `json:"last_slot,omitzero"`
	LastRun  time.Time `json:"last_run,omitzero"` // when its own last run started
	// "running", then the finished crawl's Outcome status — "success" /
	// "partial" / "failed" — or "interrupted". Files written before Outcome
	// existed hold "ok", which reads as "success".
	LastStatus string `json:"last_status,omitempty"`
	// LastFinished is when the schedule's OWN last run ended, whatever its
	// outcome.
	LastFinished time.Time `json:"last_finished,omitzero"`
	// LastCrawlEnd is when the provider's last crawl that got something done
	// (success or partial) ended, whoever started it — a Crawl click, a bulk
	// run or this schedule. A run is skipped when that was recent (see Due).
	LastCrawlEnd time.Time `json:"last_crawl_end,omitzero"`
}

// skipWindow: a planned run is skipped when a crawl of the provider that
// got something done ended less than this before it — a manual crawl at
// 13:50 covers a 14:00 run.
const skipWindow = slotMinutes * time.Minute

// prevSlot is the latest planned run at or before now — yesterday's last
// one before today's first. Zero when the schedule has no times.
func (s Schedule) prevSlot(now time.Time) time.Time {
	if len(s.Times) == 0 {
		return time.Time{}
	}
	day := slotStart(now, 0)
	for i := len(s.Times) - 1; i >= 0; i-- {
		if t := day.Add(time.Duration(s.Times[i]) * time.Minute); !t.After(now) {
			return t
		}
	}
	return day.Add(time.Duration(s.Times[len(s.Times)-1])*time.Minute - 24*time.Hour)
}

// nextSlot is the first planned run after now — tomorrow's first after
// today's last.
func (s Schedule) nextSlot(now time.Time) time.Time {
	if len(s.Times) == 0 {
		return time.Time{}
	}
	day := slotStart(now, 0)
	for _, m := range s.Times {
		if t := day.Add(time.Duration(m) * time.Minute); t.After(now) {
			return t
		}
	}
	return day.Add(time.Duration(s.Times[0])*time.Minute + 24*time.Hour)
}

// Due reports whether the latest planned run still needs a crawl: it is
// later than the last one served, and no crawl of the provider that got
// something done ended from skipWindow before it onwards. Only the LATEST
// run is ever considered, so after downtime missed runs are made up once,
// never several times.
func (s Schedule) Due(now time.Time) bool {
	if !s.Enabled || len(s.Times) == 0 {
		return false
	}
	prev := s.prevSlot(now)
	if !prev.After(s.LastSlot) {
		return false
	}
	return s.LastCrawlEnd.Before(prev.Add(-skipWindow))
}

// NextRun is when the schedule next crawls — the zero time when it is due
// right now (shown as "due now" or "queued").
func (s Schedule) NextRun() time.Time {
	now := time.Now().Round(0)
	if s.Due(now) {
		return time.Time{}
	}
	return s.nextSlot(now)
}

// TodayTimes are the schedule's runs on today's (UTC) date, in order.
func (s Schedule) TodayTimes() []time.Time {
	day := slotStart(time.Now(), 0)
	out := make([]time.Time, 0, len(s.Times))
	for _, m := range s.Times {
		out = append(out, day.Add(time.Duration(m)*time.Minute))
	}
	return out
}

// validTime reports whether m is a time of day on the 15-minute grid.
func validTime(m int) bool { return m >= 0 && m < 24*60 && m%slotMinutes == 0 }

// normalizeTimes validates a schedule's run times and returns them sorted,
// each once.
func normalizeTimes(in []int) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, m := range in {
		if !validTime(m) {
			return nil, fmt.Errorf("every run must be a time on the 15-minute grid")
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("add at least one run time")
	}
	sort.Ints(out)
	return out, nil
}

// errHasSchedule: a provider has one schedule, holding all its times.
var errHasSchedule = errors.New("already has a schedule — edit it to change its times")

// ScheduleStore holds schedule.json in memory and persists every change
// atomically, the same temp-file-then-rename pattern the CSV store uses.
type ScheduleStore struct {
	path string

	mu        sync.Mutex
	schedules []Schedule

	// saveMu serialises save: each write snapshots under mu and is renamed
	// into place in the order it was taken, so an older snapshot never
	// lands on top of a newer one.
	saveMu sync.Mutex
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
	if len(bytes.TrimSpace(data)) == 0 { // emptied by hand to reset it
		s.schedules = nil
		return nil
	}
	var schedules []Schedule
	if err := json.Unmarshal(data, &schedules); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	now := time.Now().Round(0)
	converted := false
	for i := range schedules {
		// "running" on disk means board-console stopped mid-run: the
		// subprocess died with it, so the run did not finish.
		if schedules[i].LastStatus == "running" {
			schedules[i].LastStatus = "interrupted"
		}
		if len(schedules[i].Times) == 0 {
			convertLegacy(&schedules[i], now)
			converted = true
		}
	}
	merged := mergeByProvider(schedules)
	s.schedules = merged
	if converted || len(merged) != len(schedules) {
		if err := s.save(); err != nil {
			log.Printf("schedule store: persist converted schedules: %v", err)
		}
	}
	return nil
}

// convertLegacy turns an older schedule into its explicit times. "Every N"
// first becomes the nearest crawls-per-day, starting at its last run's time
// of day (on the 15-minute grid) or 00:00 UTC, and waits for its next time;
// "N a day from a first run" then becomes the N times that implies.
func convertLegacy(s *Schedule, now time.Time) {
	fromInterval := s.PerDay == 0
	if fromInterval {
		s.PerDay = perDayFromInterval(s.IntervalSecs)
		s.OffsetMin = 0
		if lr := s.LastRun.UTC(); !lr.IsZero() {
			s.OffsetMin = (lr.Hour()*60 + lr.Minute()) / slotMinutes * slotMinutes
		}
	}
	gap := 24 * 60 / s.PerDay
	var times []int
	for i := 0; i < s.PerDay; i++ {
		times = append(times, (s.OffsetMin+i*gap)%(24*60))
	}
	s.Times, _ = normalizeTimes(times)
	s.PerDay, s.OffsetMin, s.IntervalSecs = 0, 0, 0
	if fromInterval {
		s.LastSlot = s.prevSlot(now)
	}
}

// mergeByProvider folds several schedules of one provider — allowed before
// a schedule could hold any number of times — into the first of them, with
// every time any of them had.
func mergeByProvider(in []Schedule) []Schedule {
	var out []Schedule
	at := map[string]int{}
	for _, sch := range in {
		i, ok := at[sch.Provider]
		if !ok {
			at[sch.Provider] = len(out)
			out = append(out, sch)
			continue
		}
		m := &out[i]
		m.Times, _ = normalizeTimes(append(append([]int{}, m.Times...), sch.Times...))
		m.Enabled = m.Enabled || sch.Enabled
		if sch.LastSlot.After(m.LastSlot) {
			m.LastSlot = sch.LastSlot
		}
		if sch.LastCrawlEnd.After(m.LastCrawlEnd) {
			m.LastCrawlEnd = sch.LastCrawlEnd
		}
		if sch.LastRun.After(m.LastRun) {
			m.LastRun, m.LastStatus, m.LastFinished = sch.LastRun, sch.LastStatus, sch.LastFinished
		}
	}
	return out
}

// save writes the schedules to disk. Callers must NOT hold s.mu.
func (s *ScheduleStore) save() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	data, err := json.MarshalIndent(s.schedules, "", "  ")
	s.mu.Unlock()
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

// markServedLocked records the current planned run as served, if it is
// later than the one already recorded — so a new or re-timed schedule first
// runs at its NEXT time rather than the moment it is saved. Callers hold
// s.mu.
func (s *ScheduleStore) markServedLocked(i int, now time.Time) {
	if prev := s.schedules[i].prevSlot(now); prev.After(s.schedules[i].LastSlot) {
		s.schedules[i].LastSlot = prev
	}
}

// indexOfProviderLocked is the index of provider's schedule, or -1.
func (s *ScheduleStore) indexOfProviderLocked(provider string) int {
	for i, sch := range s.schedules {
		if sch.Provider == provider {
			return i
		}
	}
	return -1
}

// Add creates provider's schedule with the given run times. It refuses a
// provider that already has one (errHasSchedule): that one is edited.
func (s *ScheduleStore) Add(provider string, times []int) error {
	times, err := normalizeTimes(times)
	if err != nil {
		return err
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.indexOfProviderLocked(provider) >= 0 {
		s.mu.Unlock()
		return fmt.Errorf("%s %w", provider, errHasSchedule)
	}
	s.schedules = append(s.schedules, Schedule{ID: id, Provider: provider, Times: times, Enabled: true})
	s.markServedLocked(len(s.schedules)-1, time.Now().Round(0))
	s.mu.Unlock()
	return s.save()
}

// ByProvider returns provider's schedule, if it has one.
func (s *ScheduleStore) ByProvider(provider string) (Schedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.indexOfProviderLocked(provider); i >= 0 {
		return s.schedules[i], true
	}
	return Schedule{}, false
}

// Update changes a schedule's provider and run times in place (same ID).
func (s *ScheduleStore) Update(id, provider string, times []int) error {
	times, err := normalizeTimes(times)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if i := s.indexOfProviderLocked(provider); i >= 0 && s.schedules[i].ID != id {
		s.mu.Unlock()
		return fmt.Errorf("%s %w", provider, errHasSchedule)
	}
	found := false
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].Provider = provider
			s.schedules[i].Times = times
			s.markServedLocked(i, time.Now().Round(0))
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
// "running", and marks the planned run it serves.
func (s *ScheduleStore) markStarted(id string, startedAt time.Time) {
	s.mu.Lock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].LastRun = startedAt
			s.schedules[i].LastStatus = "running"
			s.markServedLocked(i, startedAt)
			break
		}
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Printf("schedule store: persist run start: %v", err)
	}
}

// restoreRun puts back a schedule's previous run stamps, undoing a
// markStarted whose crawl never began.
func (s *ScheduleStore) restoreRun(id string, lastRun, lastSlot time.Time, status string) {
	s.mu.Lock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].LastRun = lastRun
			s.schedules[i].LastSlot = lastSlot
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

// Scheduler ticks once a minute. It starts the built-in dead-board cleanup
// and company recount when their daily slot is unserved (see
// system_jobs.go), starts every schedule whose planned run has come (see
// Schedule.Due) while fewer than
// SCHEDULE_CAPACITY crawls are running — the rest queue — and once an hour
// runs ONE reindex for every scheduled crawl that finished in that hour.
//
// Each schedule runs in the background, so one slow crawl never holds up
// another. A schedule never overlaps a crawl of its provider — its own
// previous run or a manual one: while one is in flight the slot waits, and
// it runs on the first tick after that crawl finishes, unless that crawl
// already did the job (see Schedule.Due).
type Scheduler struct {
	store  *ScheduleStore
	runner *Runner
	tick   time.Duration

	// Scheduled crawls don't reindex one by one — a full rebuild per crawl
	// is the heaviest thing this tool does. Their jobs wait here, and the
	// first tick of each hour reindexes once for all of them (so in Activity
	// the reindex shows as a shared step of each).
	mu            sync.Mutex
	reindexJobs   []int
	lastBatchHour time.Time
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
	// monotonic clock, which stops while the host sleeps — a stamp taken
	// in-process before a 2-hour sleep would then look minutes old on wake,
	// and the schedule would run late by the length of the sleep.
	now := time.Now().Round(0)

	// The system jobs (system_jobs.go), each at its daily time. Started in
	// the background; if one is already running (started by hand), the
	// start declines and a later tick tries again — until that run is
	// recorded and serves the slot.
	if s.runner.system.Get(sysCleanup).Due(now) && s.runner.StartCleanup(true) {
		log.Printf("scheduler: daily dead-board cleanup started")
	}
	if s.runner.system.Get(sysRecount).Due(now) && s.runner.StartCompanyRefresh() {
		log.Printf("scheduler: daily company recount started")
	}

	s.flushHourlyReindex(now)

	// Due runs start oldest planned time first, and only while fewer than
	// SCHEDULE_CAPACITY crawls (of any origin) are in flight. The rest stay
	// unserved — queued — and a later tick starts them once a crawl ends.
	var due []Schedule
	for _, sch := range s.store.List() {
		if sch.Due(now) && !s.runner.Crawling(sch.Provider) {
			due = append(due, sch)
		}
	}
	sort.SliceStable(due, func(a, b int) bool { return due[a].prevSlot(now).Before(due[b].prevSlot(now)) })
	capacity := scheduleCapacity()

	for i, sch := range due {
		if s.runner.CrawlCount() >= capacity {
			log.Printf("scheduler: %d scheduled crawl(s) queued — %d already running", len(due)-i, capacity)
			return
		}
		s.store.markStarted(sch.ID, now)
		id, provider := sch.ID, sch.Provider
		job, started := s.runner.StartCrawlJob(sch.Provider, false, false, func(err error) {
			if err != nil {
				log.Printf("scheduler: %s: %v", id, err)
			}
			s.store.recordRun(id, s.runner.crawlOutcome(provider, err), time.Now().Round(0))
		})
		if !started {
			// A crawl of this provider began between the check and the claim:
			// put the stamps back, and a later tick tries again.
			s.store.restoreRun(sch.ID, sch.LastRun, sch.LastSlot, sch.LastStatus)
			continue
		}
		s.mu.Lock()
		s.reindexJobs = append(s.reindexJobs, job)
		s.mu.Unlock()
		log.Printf("scheduler: running %s (provider=%s)", sch.ID, sch.Provider)
	}
}

// flushHourlyReindex queues one reindex for every scheduled crawl collected
// since the last one, on the first tick of each new hour. A crawl still
// running then is simply served by the next hour's reindex — the queue
// makes a reindex wait for nothing, and the crawl is already in the list.
func (s *Scheduler) flushHourlyReindex(now time.Time) {
	hour := now.Truncate(time.Hour)
	s.mu.Lock()
	if !hour.After(s.lastBatchHour) {
		s.mu.Unlock()
		return
	}
	first := s.lastBatchHour.IsZero()
	s.lastBatchHour = hour
	jobs := s.reindexJobs
	if !first {
		s.reindexJobs = nil
	}
	s.mu.Unlock()
	if !first && len(jobs) > 0 {
		s.runner.queueReindex(jobs...)
	}
}
