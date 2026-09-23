package main

import (
	"testing"
	"time"
)

func TestScheduleDue_NeverRun(t *testing.T) {
	s := Schedule{Enabled: true, IntervalSecs: 900}
	if !s.Due(time.Now()) {
		t.Fatal("a never-run, enabled schedule must be due immediately")
	}
}

func TestScheduleDue_Disabled(t *testing.T) {
	s := Schedule{Enabled: false, IntervalSecs: 900}
	if s.Due(time.Now()) {
		t.Fatal("a disabled schedule must never be due")
	}
}

func TestScheduleDue_RecentlyRun(t *testing.T) {
	s := Schedule{Enabled: true, IntervalSecs: 900, LastRun: time.Now()}
	if s.Due(time.Now()) {
		t.Fatal("a schedule run seconds ago with a 15m interval must not be due yet")
	}
}

func TestScheduleDue_IntervalElapsed(t *testing.T) {
	s := Schedule{Enabled: true, IntervalSecs: 900, LastRun: time.Now().Add(-16 * time.Minute)}
	if !s.Due(time.Now()) {
		t.Fatal("a schedule last run 16m ago with a 15m interval must be due")
	}
}
