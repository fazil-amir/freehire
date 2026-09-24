package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cleanupHour is when the built-in dead-board cleanup runs each day, in
// the container's local time.
const cleanupHour = 3

// cleanupState is cleanup.json: when the dead-board cleanup last ran for
// real (--apply, scheduled or "Run now" — a Preview is a dry run and never
// counts) and how that ended. It is its own file, not a schedule.json
// entry, because the job is built in: nobody edits or deletes it.
type cleanupState struct {
	LastRun    time.Time `json:"last_run,omitzero"`
	LastStatus RunStatus `json:"last_status,omitempty"`
}

// CleanupStore persists cleanupState atomically, the same temp-file-then-
// rename pattern the schedule store uses.
type CleanupStore struct {
	path string

	mu    sync.Mutex
	state cleanupState
}

// NewCleanupStore loads cleanup.json. With no file yet it starts the clock
// NOW rather than at the zero time: a zero LastRun would read as "missed
// every slot" and fire an --apply run on the first tick after deploy,
// before anyone has seen a Preview. The first real run is the next 03:00.
func NewCleanupStore(path string) (*CleanupStore, error) {
	s := &CleanupStore{path: path}
	data, err := os.ReadFile(path)
	switch {
	// An empty file (emptied by hand to reset it) is the same fresh start
	// as a missing one, not a reason to refuse to boot.
	case os.IsNotExist(err) || (err == nil && len(bytes.TrimSpace(data)) == 0):
		s.state.LastRun = time.Now()
		return s, s.save()
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

func (s *CleanupStore) State() cleanupState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Record stamps a finished --apply run. ranAt is when it STARTED, so a run
// that began at 03:00 and ended at 03:40 still satisfies the 03:00 slot.
func (s *CleanupStore) Record(ranAt time.Time, status RunStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = cleanupState{LastRun: ranAt, LastStatus: status}
	return s.save()
}

func (s *CleanupStore) save() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "cleanup-*.tmp")
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

// cleanupSlot is the most recent 03:00 at or before now.
func cleanupSlot(now time.Time) time.Time {
	slot := time.Date(now.Year(), now.Month(), now.Day(), cleanupHour, 0, 0, 0, now.Location())
	if now.Before(slot) {
		slot = slot.AddDate(0, 0, -1)
	}
	return slot
}

// cleanupDue reports whether the latest slot has gone unserved. Comparing
// against the latest slot only — never counting how many were missed — is
// what makes a process that was down for three days run once, not three
// times, and a restart after today's run not run again.
func cleanupDue(lastRun, now time.Time) bool {
	return lastRun.Before(cleanupSlot(now))
}

// cleanupNext is when the next scheduled run fires: now, if a slot is
// unserved, otherwise the next 03:00.
func cleanupNext(lastRun, now time.Time) time.Time {
	if cleanupDue(lastRun, now) {
		return now
	}
	return cleanupSlot(now).AddDate(0, 0, 1)
}
