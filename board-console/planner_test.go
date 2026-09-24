package main

import (
	"testing"
	"time"
)

func TestLoadProfile_CountsStartsPerSlot(t *testing.T) {
	// Three providers all set to run at 02:00 UTC (slot 8); one is paused.
	day := []Schedule{
		{Provider: "a", Times: []int{120, 840}, Enabled: true},
		{Provider: "b", Times: []int{120}, Enabled: true},
		{Provider: "c", Times: []int{0, 120, 840}, Enabled: true},
		{Provider: "d", Times: []int{120}, Enabled: false},
	}
	load := loadProfile(day)
	if load[8] != 3 {
		t.Errorf("slot 02:00: want 3 crawls starting (the paused one not counted), got %d", load[8])
	}
	if load[56] != 2 {
		t.Errorf("slot 14:00: want 2, got %d", load[56])
	}
	if load[4] != 0 {
		t.Errorf("slot 01:00: want 0, got %d", load[4])
	}
}

func TestPerDayFromInterval(t *testing.T) {
	for secs, want := range map[int64]int{1800: 24, 3600: 24, 7200: 12, 21600: 4, 43200: 2, 86400: 2, 0: 2} {
		if got := perDayFromInterval(secs); got != want {
			t.Errorf("every %ds → %d× a day, want %d×", secs, got, want)
		}
	}
}

func TestScheduleCapacity(t *testing.T) {
	t.Setenv("SCHEDULE_CAPACITY", "")
	if scheduleCapacity() != 2 {
		t.Error("unset must default to 2")
	}
	t.Setenv("SCHEDULE_CAPACITY", "3")
	if scheduleCapacity() != 3 {
		t.Error("SCHEDULE_CAPACITY=3 must be used")
	}
	t.Setenv("SCHEDULE_CAPACITY", "lots")
	if scheduleCapacity() != 2 {
		t.Error("an unreadable value must fall back to 2")
	}
}

func TestScheduler_HourlyReindexBatchesScheduledCrawls(t *testing.T) {
	sched, _, activity := newTestScheduler(t)
	r := sched.runner
	r.bin.Reindex = writeScript(t, "true")

	t0 := time.Date(2026, 9, 24, 10, 1, 0, 0, time.UTC)
	sched.flushHourlyReindex(t0) // first tick: just starts the clock
	sched.mu.Lock()
	sched.reindexJobs = []int{activity.NewJob(), activity.NewJob()} // two scheduled crawls finished this hour
	sched.mu.Unlock()

	sched.flushHourlyReindex(t0.Add(30 * time.Minute)) // same hour: nothing
	if n := len(jobsOf(activity, "reindex")); n != 0 {
		t.Fatalf("no reindex before the hour turns, got %d", n)
	}
	sched.flushHourlyReindex(t0.Add(time.Hour)) // 11:01
	waitFor(t, "the batched reindex", func() bool { return len(jobsOf(activity, "reindex")) == 1 })
	if jobs := jobsOf(activity, "reindex")[0]; len(jobs) != 2 {
		t.Fatalf("ONE reindex must serve both crawls, got jobs %v", jobs)
	}
	sched.flushHourlyReindex(t0.Add(2 * time.Hour)) // nothing new crawled → nothing
	time.Sleep(200 * time.Millisecond)
	if n := len(jobsOf(activity, "reindex")); n != 1 {
		t.Fatalf("an hour with no scheduled crawls must not reindex, got %d", n)
	}
}
