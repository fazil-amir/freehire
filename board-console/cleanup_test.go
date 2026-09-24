package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupDue(t *testing.T) {
	at := func(day, hour, min int) time.Time { return time.Date(2026, 9, day, hour, min, 0, 0, time.UTC) }
	cases := []struct {
		name    string
		lastRun time.Time
		now     time.Time
		want    bool
	}{
		{"before today's slot, ran yesterday", at(22, 3, 0), at(23, 2, 59), false},
		{"today's slot arrived", at(22, 3, 0), at(23, 3, 0), true},
		{"already ran today, restarted later", at(23, 3, 0), at(23, 15, 0), false},
		{"manual run after the slot counts for the day", at(23, 14, 0), at(23, 20, 0), false},
		{"down for three days: due once", at(19, 3, 0), at(23, 9, 0), true},
		{"down over the slot, back the next morning", at(22, 3, 0), at(23, 8, 0), true},
	}
	for _, c := range cases {
		if got := cleanupDue(c.lastRun, c.now); got != c.want {
			t.Errorf("%s: cleanupDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCleanupNext(t *testing.T) {
	last := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)
	want := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)
	if got := cleanupNext(last, now); !got.Equal(want) {
		t.Errorf("cleanupNext = %v, want %v", got, want)
	}
}

func TestCleanupStore_EmptyFileIsAFreshStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewCleanupStore(path)
	if err != nil {
		t.Fatalf("an empty cleanup.json must not stop the console: %v", err)
	}
	if s.State().LastRun.IsZero() {
		t.Error("want a fresh clock, as for a missing file")
	}
}
