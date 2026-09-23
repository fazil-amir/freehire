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
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "combined_boards.csv")
	if err := os.WriteFile(csvPath, []byte("id,provider,board,company,added\nx,acme,,Acme,true\n"), 0o644); err != nil {
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
	cleanup, err := NewCleanupStore(filepath.Join(dir, "cleanup.json")) // fresh: not due
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewScheduleStore(filepath.Join(dir, "schedule.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add("acme", 2*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(csv, activity, nil, cleanup, Binaries{Ingest: ingest, CSVPath: csvPath})
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
	if !sched.runner.StartCrawl("acme", false, func(error) { close(manualDone) }) {
		t.Fatal("manual crawl did not start")
	}
	if sched.runner.StartCrawl("acme", false, nil) {
		t.Fatal("a second manual crawl of the same provider must be refused")
	}

	sched.runDue() // schedule is due (never run), but acme is crawling
	if got := store.List()[0].LastStatus; got == "running" {
		t.Fatal("schedule started while a manual crawl of its provider was in flight")
	}

	<-manualDone
	sched.runDue() // now free: the schedule runs
	deadline := time.Now().Add(5 * time.Second)
	for store.List()[0].LastStatus != "ok" && time.Now().Before(deadline) {
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
	store.mu.Lock()
	store.schedules[0].LastRun = time.Now().Add(-time.Hour)
	store.mu.Unlock()
	sched.runDue()

	deadline := time.Now().Add(5 * time.Second)
	for store.List()[0].LastStatus == "running" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := store.List()[0].LastStatus; got != "ok" {
		t.Fatalf("want ok after the run, got %q", got)
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
