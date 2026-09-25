package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSystemJob_Due(t *testing.T) {
	at := func(day, hour, min int) time.Time { return time.Date(2026, 9, day, hour, min, 0, 0, time.UTC) }
	job := func(served time.Time) SystemJob { return SystemJob{Enabled: true, Minute: 3 * 60, Served: served} }
	cases := []struct {
		name   string
		served time.Time
		now    time.Time
		want   bool
	}{
		{"before today's slot, served yesterday", at(22, 3, 0), at(23, 2, 59), false},
		{"today's slot arrived", at(22, 3, 0), at(23, 3, 0), true},
		{"already served today, restarted later", at(23, 3, 0), at(23, 15, 0), false},
		{"down for three days: due once", at(19, 3, 0), at(23, 9, 0), true},
	}
	for _, c := range cases {
		if got := job(c.served).Due(c.now); got != c.want {
			t.Errorf("%s: Due = %v, want %v", c.name, got, c.want)
		}
	}
	paused := job(at(19, 3, 0))
	paused.Enabled = false
	if paused.Due(at(23, 9, 0)) || !paused.NextRun(at(23, 9, 0)).IsZero() {
		t.Error("a paused job is never due and has no next run")
	}
	if got := job(at(23, 3, 0)).NextRun(at(23, 15, 0)); !got.Equal(at(24, 3, 0)) {
		t.Errorf("NextRun = %v, want tomorrow 03:00", got)
	}
}

func TestSystemStore_FreshInstallAndLegacyCleanup(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "cleanup.json")
	if err := os.WriteFile(legacy, []byte(`{"last_run":"2026-09-20T03:00:28Z","last_status":"done"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSystemStore(filepath.Join(dir, "system.json"), legacy)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, j := range s.List() {
		if !j.Enabled || j.Due(now) {
			t.Errorf("%s: a fresh job must be on and wait for its next time, got %+v", j.Key, j)
		}
	}
	if c := s.Get(sysCleanup); c.LastStatus != StatusDone || c.LastRun.Day() != 20 {
		t.Errorf("cleanup.json's last run must carry over, got %+v", c)
	}
	if r := s.Get(sysRecount); !r.LastRun.IsZero() {
		t.Errorf("the recount has no history yet, got %+v", r)
	}

	// Re-timing and resuming never fire a job on the spot.
	if err := s.SetTime(sysRecount, 0); err != nil || s.Get(sysRecount).Due(now) {
		t.Errorf("a re-timed job must wait for its next time (%v)", err)
	}
	if err := s.SetTime(sysRecount, 7); err == nil {
		t.Error("a time off the 15-minute grid must be refused")
	}
	_ = s.Toggle(sysCleanup)
	_ = s.Toggle(sysCleanup)
	if c := s.Get(sysCleanup); !c.Enabled || c.Due(now) {
		t.Errorf("a resumed job must wait for its next time, got %+v", c)
	}

	// Persisted: a reload reads the same settings.
	again, err := NewSystemStore(filepath.Join(dir, "system.json"), legacy)
	if err != nil || again.Get(sysRecount).Minute != 0 {
		t.Fatalf("reload: %+v %v", again.Get(sysRecount), err)
	}
	var onDisk []SystemJob
	data, _ := os.ReadFile(filepath.Join(dir, "system.json"))
	if json.Unmarshal(data, &onDisk) != nil || len(onDisk) != 2 {
		t.Errorf("system.json = %s", data)
	}
}

func TestSystemStore_EmptyFileIsAFreshStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSystemStore(path, "")
	if err != nil {
		t.Fatalf("an empty system.json must not stop the console: %v", err)
	}
	if len(s.List()) != 2 {
		t.Errorf("want both system jobs, got %d", len(s.List()))
	}
}
