package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestScheduler wires a real Scheduler over temp stores and a fake
// ingest that sleeps, for one already-added provider "acme".
func newTestScheduler(t *testing.T) (*Scheduler, *ScheduleStore, *ActivityLog) {
	return newTestSchedulerFor(t, "acme")
}

// newTestSchedulerFor is newTestScheduler with one due schedule per provider.
func newTestSchedulerFor(t *testing.T, providers ...string) (*Scheduler, *ScheduleStore, *ActivityLog) {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "combined_boards.csv")
	rows := "id,provider,board,company,added\n"
	for _, p := range providers {
		rows += p + "," + p + ",," + p + ",true\n"
	}
	if err := os.WriteFile(csvPath, []byte(rows), 0o644); err != nil {
		t.Fatal(err)
	}
	ingest := filepath.Join(dir, "ingest.sh")
	if err := os.WriteFile(ingest, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	csv, err := NewCSVStore(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	activity, err := NewActivityLog(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	system, err := NewSystemStore(filepath.Join(dir, "system.json"), "") // fresh: not due
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewScheduleStore(filepath.Join(dir, "schedule.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range providers {
		if err := store.Add(p, []int{0}); err != nil {
			t.Fatal(err)
		}
	}
	forceDue(store) // a new schedule first runs at its NEXT slot; tests want it now
	runner := NewRunner(csv, activity, nil, system, Binaries{Ingest: ingest, CSVPath: csvPath})
	return NewScheduler(store, runner), store, activity
}

func countIngests(a *ActivityLog) int {
	n := 0
	for _, r := range a.List() {
		if r.Action == "ingest" {
			n++
		}
	}
	return n
}

// The bug this guards: a manual Crawl was running when the provider's
// schedule came due, and the scheduler started a second crawl of the same
// provider alongside it.
func TestScheduler_WaitsForAManualCrawlOfTheSameProvider(t *testing.T) {
	sched, store, activity := newTestScheduler(t)

	manualDone := make(chan struct{})
	if !sched.runner.StartCrawl("acme", false, false, func(error) { close(manualDone) }) {
		t.Fatal("manual crawl did not start")
	}
	if sched.runner.StartCrawl("acme", false, false, nil) {
		t.Fatal("a second manual crawl of the same provider must be refused")
	}

	sched.runDue() // schedule is due (never run), but acme is crawling
	if got := store.List()[0].LastStatus; got == "running" {
		t.Fatal("schedule started while a manual crawl of its provider was in flight")
	}

	<-manualDone
	sched.runDue() // now free: the schedule runs
	deadline := time.Now().Add(5 * time.Second)
	for store.List()[0].LastStatus != "success" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := countIngests(activity); got != 2 {
		t.Fatalf("want 2 ingests one after the other (manual, then scheduled), got %d", got)
	}
}

func TestScheduler_NeverOverlapsItselfAndStampsTheStart(t *testing.T) {
	sched, store, activity := newTestScheduler(t)

	sched.runDue() // due: never run
	started := store.List()[0]
	if started.LastStatus != "running" || started.LastRun.IsZero() {
		t.Fatalf("want the start stamped as running, got %+v", started)
	}
	// A monotonic reading shows as "m=" in String(); the stamp must have
	// none, or a sleep would delay the next run (see runDue).
	if strings.Contains(started.LastRun.String(), "m=") {
		t.Fatalf("LastRun carries a monotonic reading: %v", started.LastRun)
	}

	// Force it due again while the first run is still in flight.
	forceDue(store)
	sched.runDue()

	deadline := time.Now().Add(5 * time.Second)
	for store.List()[0].LastStatus == "running" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := store.List()[0].LastStatus; got != "success" {
		t.Fatalf("want success after the run, got %q", got)
	}
	if ingests := countIngests(activity); ingests != 1 {
		t.Fatalf("want exactly 1 ingest while the first was in flight, got %d", ingests)
	}
}

func TestScheduleStore_RunningOnDiskLoadsAsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte(`[{"id":"a","provider":"p","interval_seconds":900,"enabled":true,"last_status":"running"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewScheduleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.List()[0].LastStatus; got != "interrupted" {
		t.Fatalf("want interrupted, got %q", got)
	}
}

// forceDue makes every schedule's current slot unserved, as if it had
// never run.
func forceDue(store *ScheduleStore) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for i := range store.schedules {
		store.schedules[i].LastSlot = time.Time{}
		store.schedules[i].LastCrawlEnd = time.Time{}
	}
}

// Three schedules due at once with SCHEDULE_CAPACITY=2: two start, the
// third stays due (queued) and starts on the first tick after one ends.
func TestScheduler_QueuesRunsBeyondCapacity(t *testing.T) {
	t.Setenv("SCHEDULE_CAPACITY", "2")
	sched, store, _ := newTestSchedulerFor(t, "a", "b", "c")

	sched.runDue()
	running := 0
	var queued Schedule
	for _, s := range store.List() {
		if s.LastStatus == "running" {
			running++
		} else {
			queued = s
		}
	}
	if running != 2 || sched.runner.CrawlCount() != 2 {
		t.Fatalf("want 2 crawls started, got %d (in flight %d)", running, sched.runner.CrawlCount())
	}
	if !queued.Due(time.Now()) {
		t.Fatal("the third schedule must stay due while it waits")
	}

	sched.runDue() // still full: nothing more starts
	if s, _ := store.ByProvider(queued.Provider); s.LastStatus == "running" || sched.runner.Crawling(queued.Provider) {
		t.Fatal("capacity reached: the queued schedule must not start")
	}

	waitFor(t, "a crawl to end", func() bool { return sched.runner.CrawlCount() < 2 })
	sched.runDue()
	waitFor(t, "the queued schedule to start", func() bool {
		s, _ := store.ByProvider(queued.Provider)
		return s.LastStatus == "running" || s.LastStatus == "success"
	})
	waitFor(t, "every crawl to end", func() bool { return sched.runner.CrawlCount() == 0 })
}

// The company recount is a system job: when its daily slot is unserved the
// scheduler starts it, and the run is recorded as its last.
func TestScheduler_RunsTheDailyRecount(t *testing.T) {
	sched, store, activity := newTestScheduler(t)
	r := sched.runner
	r.bin.RecountCompanies = writeScript(t, "true")
	r.bin.ReindexCompanies = writeScript(t, "true")
	store.mu.Lock()
	store.schedules = nil // only the system jobs here
	store.mu.Unlock()
	r.system.mu.Lock()
	r.system.jobs[sysRecount].Served = time.Time{} // its slot is unserved
	r.system.mu.Unlock()

	sched.runDue()
	waitFor(t, "the recount to be recorded", func() bool { return r.system.Get(sysRecount).LastStatus == StatusDone })
	if len(jobsOf(activity, "recount-companies")) != 1 {
		t.Fatal("want one recount-companies run")
	}
	if r.system.Get(sysRecount).Due(time.Now()) {
		t.Error("a recorded run must serve the slot")
	}
	if r.system.Get(sysCleanup).LastRun.IsZero() == false {
		t.Error("the cleanup was not due and must not have run")
	}
}
