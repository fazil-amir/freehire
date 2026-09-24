package main

import (
	"testing"
	"time"
)

func TestScheduleRow_LastCrawl(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 11, 15, 0, 0, time.UTC) // the schedule's own run
	later := t0.Add(13 * time.Minute)
	sch := func(status string, at time.Time) Schedule {
		return Schedule{Provider: "greenhouse", LastStatus: status, LastRun: at}
	}
	partial := &Run{Action: "ingest", Provider: "greenhouse", Status: StatusFailed, StartedAt: later,
		Stderr: "ingest done: provider=greenhouse providers=1 ingested=225573 failed=1 skipped=0 rejected=0 unreadable=0"}

	cases := []struct {
		name       string
		row        scheduleRow
		wantStatus string
		wantAt     time.Time
	}{
		{"your case: scheduled run interrupted by a restart, a manual crawl running now",
			scheduleRow{Schedule: sch("interrupted", t0), LatestRun: &Run{Status: StatusRunning, StartedAt: later}, Crawling: true},
			"running", later},
		{"that manual crawl finished: its outcome, not the stale interrupted",
			scheduleRow{Schedule: sch("interrupted", t0), LatestRun: partial},
			OutcomePartial, later},
		{"the schedule's own run is newer than anything in the log",
			scheduleRow{Schedule: sch("success", later), LatestRun: &Run{Status: StatusDone, StartedAt: t0}},
			OutcomeSuccess, later},
		{"an interrupted run with nothing newer stays interrupted",
			scheduleRow{Schedule: sch("interrupted", t0)},
			"interrupted", t0},
		{"old files' ok reads as success",
			scheduleRow{Schedule: sch("ok", t0)},
			OutcomeSuccess, t0},
		{"never run",
			scheduleRow{Schedule: sch("", time.Time{})},
			"", time.Time{}},
	}
	for _, c := range cases {
		got := c.row.LastCrawl()
		if got.Status != c.wantStatus || !got.At.Equal(c.wantAt) {
			t.Errorf("%s: got %s at %v, want %s at %v", c.name, got.Status, got.At, c.wantStatus, c.wantAt)
		}
	}
	if b := (scheduleRow{Schedule: sch("interrupted", t0)}).LastCrawl().Badge; b != OutcomeFailed {
		t.Errorf("interrupted must use the failed badge colour, got %q", b)
	}
}
