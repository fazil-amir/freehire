package main

import (
	"testing"
	"time"
)

func TestScheduleDue_CountsEveryGoodCrawlFromItsFinish(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 32, 0, 0, time.UTC)
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	base := Schedule{Enabled: true, IntervalSecs: 1800} // every 30m

	cases := []struct {
		name string
		s    Schedule
		want bool
	}{
		{"own run 32m ago, nothing since → due", withRun(base, ago(32), time.Time{}, time.Time{}), true},
		{"own run 32m ago, a manual crawl finished 1m ago → NOT due", withRun(base, ago(32), time.Time{}, ago(1)), false},
		{"clock counts from the FINISH: started 40m ago, finished 20m ago → not due", withRun(base, ago(40), ago(20), time.Time{}), false},
		{"own run failed 31m ago (finish stamped) → due again, not every minute before", withRun(base, ago(35), ago(31), time.Time{}), true},
		{"old file: only last_run → still works", withRun(base, ago(31), time.Time{}, time.Time{}), true},
		{"never run → due", base, true},
	}
	for _, c := range cases {
		if got := c.s.Due(now); got != c.want {
			t.Errorf("%s: Due = %v, want %v (next %v)", c.name, got, c.want, c.s.NextRun())
		}
	}
}

func withRun(s Schedule, started, finished, crawlEnd time.Time) Schedule {
	s.LastRun, s.LastFinished, s.LastCrawlEnd = started, finished, crawlEnd
	return s
}

func TestRecordProviderCrawl_OnlyGoodCrawlsResetTheClock(t *testing.T) {
	_, store, _ := newTestScheduler(t) // one schedule for "acme"
	at := time.Now().Round(0)

	store.RecordProviderCrawl("acme", at, OutcomeFailed)
	if !store.List()[0].LastCrawlEnd.IsZero() {
		t.Fatal("a failed crawl must not reset the clock")
	}
	store.RecordProviderCrawl("other", at, OutcomeSuccess)
	if !store.List()[0].LastCrawlEnd.IsZero() {
		t.Fatal("another provider's crawl must not touch this schedule")
	}
	store.RecordProviderCrawl("acme", at, OutcomePartial)
	if !store.List()[0].LastCrawlEnd.Equal(at) {
		t.Fatal("a partial crawl got jobs in: it must reset the clock")
	}
}

// Your scenario: the schedule comes due while you are crawling the same
// provider by hand. It must wait for your crawl — and then NOT crawl again
// a minute later, since your crawl already did the job.
func TestScheduler_ManualCrawlSatisfiesTheSchedule(t *testing.T) {
	sched, store, activity := newTestScheduler(t) // "acme" every 2m, never run → due
	sched.runner.OnCrawlFinished = store.RecordProviderCrawl

	done := make(chan struct{})
	if !sched.runner.StartCrawl("acme", false, false, func(error) { close(done) }) {
		t.Fatal("manual crawl did not start")
	}
	sched.runDue() // due, but acme is crawling → waits
	<-done

	sched.runDue() // manual crawl just finished → the schedule's clock was reset
	time.Sleep(200 * time.Millisecond)
	if got := countIngests(activity); got != 1 {
		t.Fatalf("want only the manual crawl (1 ingest), got %d — the schedule re-crawled straight after it", got)
	}
	if s := store.List()[0]; s.LastCrawlEnd.IsZero() || s.Due(time.Now()) {
		t.Fatalf("schedule should count the manual crawl and not be due: %+v", s)
	}
}

func TestAgoText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second: "just now", time.Minute: "1 minute ago", 4 * time.Minute: "4 minutes ago",
		70 * time.Minute: "1 hour ago", 150 * time.Minute: "2 hours ago",
	} {
		if got := agoText(d); got != want {
			t.Errorf("agoText(%v) = %q, want %q", d, got, want)
		}
	}
}
