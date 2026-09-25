package main

import (
	"strings"
	"testing"
	"time"
)

// An added provider with no schedule gets a Not scheduled row, carrying
// only its crawls of the last 24 hours.
func TestUnscheduledRow_LastDayOfCrawls(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	job := func(id int, at time.Time) *Job {
		return &Job{Kind: "Crawl", StartedAt: at, Status: OutcomeSuccess,
			Steps: []*JobStep{{Run: &Run{ID: id, Action: "ingest", StartedAt: at}}}}
	}
	row := newUnscheduledRow("adp", nil, false, []*Job{job(1, now.Add(-2*time.Hour)), job(2, now.Add(-30*time.Hour))}, now)
	if !strings.Contains(row.Runs, `"run":1`) || strings.Contains(row.Runs, `"run":2`) {
		t.Errorf("want only the crawl of the last 24 hours, got %s", row.Runs)
	}
	if !strings.Contains(row.Runs, `"m":600`) { // started 10:00 UTC
		t.Errorf("a crawl sits at the minute it started, got %s", row.Runs)
	}
	if !row.Last.At.IsZero() {
		t.Errorf("no crawl in the activity log: never crawled, got %+v", row.Last)
	}
}
