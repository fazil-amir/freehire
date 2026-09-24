package main

import (
	"testing"
	"time"
)

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
