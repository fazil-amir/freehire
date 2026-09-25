package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func day(h, m int) time.Time { return time.Date(2026, 9, 24, h, m, 0, 0, time.UTC) }

// A schedule set to run at 02:00 and 14:00 (UTC).
func twoADay() Schedule {
	return Schedule{Provider: "greenhouse", Times: []int{120, 14 * 60}, Enabled: true}
}

func TestSchedule_SlotRules(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Schedule)
		now  time.Time
		want bool
	}{
		{"before the first run of the day: yesterday's 14:00 already served",
			func(s *Schedule) { s.LastSlot = day(0, 0).Add(-10 * time.Hour) }, day(1, 59), false},
		{"at its time → due", func(s *Schedule) { s.LastSlot = day(0, 0).Add(-10 * time.Hour) }, day(2, 0), true},
		{"run already served", func(s *Schedule) { s.LastSlot = day(2, 0) }, day(2, 30), false},
		{"a manual crawl ended 10 min before the run → skipped",
			func(s *Schedule) { s.LastSlot = day(0, 0).Add(-10 * time.Hour); s.LastCrawlEnd = day(1, 50) }, day(2, 5), false},
		{"a manual crawl 20 min before is too long ago to cover it",
			func(s *Schedule) { s.LastSlot = day(0, 0).Add(-10 * time.Hour); s.LastCrawlEnd = day(1, 40) }, day(2, 5), true},
		{"down since yesterday: only the LATEST run is due, once",
			func(s *Schedule) { s.LastSlot = day(0, 0).Add(-2 * 24 * time.Hour) }, day(15, 0), true},
		{"disabled", func(s *Schedule) { s.Enabled = false }, day(2, 0), false},
		{"no times", func(s *Schedule) { s.Times = nil }, day(2, 0), false},
	}
	for _, c := range cases {
		s := twoADay()
		c.edit(&s)
		if got := s.Due(c.now); got != c.want {
			t.Errorf("%s: Due = %v, want %v (prev run %v)", c.name, got, c.want, s.prevSlot(c.now))
		}
	}
	s := twoADay()
	if got := s.prevSlot(day(15, 0)); !got.Equal(day(14, 0)) {
		t.Errorf("latest run at 15:00 = %v, want 14:00", got)
	}
	if got := s.prevSlot(day(1, 0)); !got.Equal(day(14, 0).Add(-24 * time.Hour)) {
		t.Errorf("latest run at 01:00 = %v, want yesterday 14:00", got)
	}
	if got := s.nextSlot(day(15, 0)); !got.Equal(day(2, 0).Add(24 * time.Hour)) {
		t.Errorf("next run after 15:00 = %v, want tomorrow 02:00", got)
	}
	if got := s.nextSlot(day(2, 0)); !got.Equal(day(14, 0)) {
		t.Errorf("next run after 02:00 = %v, want 14:00", got)
	}
}

func TestSchedule_OlderFilesConvertToTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte(`[
		{"id":"a","provider":"greenhouse","interval_seconds":21600,"enabled":true,"last_run":"2026-09-24T10:37:00Z"},
		{"id":"b","provider":"keka","interval_seconds":43200,"enabled":true},
		{"id":"c","provider":"adp","per_day":2,"offset_minutes":30,"enabled":true},
		{"id":"d","provider":"adp","per_day":4,"offset_minutes":105,"enabled":false}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewScheduleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]int{
		"greenhouse": {270, 630, 990, 1350}, // 4× from 10:30
		"keka":       {0, 720},
		"adp":        {30, 105, 465, 750, 825, 1185}, // both adp schedules, merged
	}
	list := store.List()
	if len(list) != 3 {
		t.Fatalf("the two adp schedules must merge into one, got %d schedules", len(list))
	}
	for _, s := range list {
		if !equalInts(s.Times, want[s.Provider]) || s.PerDay != 0 || s.OffsetMin != 0 || s.IntervalSecs != 0 {
			t.Errorf("%s: times %v, want %v", s.Provider, s.Times, want[s.Provider])
		}
		if s.Provider == "adp" && (s.ID != "c" || !s.Enabled) {
			t.Errorf("adp: merged into the first (id c) and enabled, got %+v", s)
		}
		if s.Provider != "adp" && s.Due(time.Now()) {
			t.Errorf("%s: a converted interval schedule must wait for its next time", s.Provider)
		}
	}
	// The conversion is written back: the file now holds times.
	var onDisk []Schedule
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &onDisk); err != nil || len(onDisk) != 3 || len(onDisk[0].Times) == 0 {
		t.Fatalf("converted schedules not saved: %s", data)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestScheduleStore_TimesAreSortedUniqueAndOnePerProvider(t *testing.T) {
	store, err := NewScheduleStore(filepath.Join(t.TempDir(), "schedule.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 17:45, 01:30, 09:00 and 01:30 again → 01:30, 09:00, 17:45.
	if err := store.Add("adp", []int{17*60 + 45, 90, 540, 90}); err != nil {
		t.Fatal(err)
	}
	s := store.List()[0]
	if !equalInts(s.Times, []int{90, 540, 17*60 + 45}) {
		t.Errorf("want sorted unique times, got %v", s.Times)
	}
	if got := s.TodayTimes(); got[0].Format("15:04") != "01:30" || got[2].Format("15:04") != "17:45" {
		t.Errorf("TodayTimes = %v", got)
	}
	if s.Due(time.Now()) {
		t.Error("a new schedule must first run at its NEXT time, not immediately")
	}
	if err := store.Add("adp", []int{600}); err == nil {
		t.Error("a second schedule for the same provider must be refused")
	}
	if err := store.Add("keka", []int{7}); err == nil {
		t.Error("a time off the 15-minute grid must be refused")
	}
	if err := store.Add("keka", nil); err == nil {
		t.Error("a schedule needs at least one time")
	}
	if err := store.Add("keka", []int{600}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(s.ID, "keka", []int{0}); err == nil {
		t.Error("moving adp's schedule onto keka, which has one, must be refused")
	}
	if err := store.Update(s.ID, "adp", []int{0, 60}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ByProvider("adp"); !equalInts(got.Times, []int{0, 60}) {
		t.Errorf("updated times = %v", got.Times)
	}
}

func TestScheduleStore_EmptyFileIsNoSchedules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewScheduleStore(path)
	if err != nil || len(store.List()) != 0 {
		t.Fatalf("an empty schedule.json must load as no schedules, got %v, %v", store, err)
	}
}

// Each slot shows the crawl that served its latest occurrence: one started
// from 15 minutes before it until the next slot, whoever started it.
func TestSlotRuns_MatchTheCrawlThatServedEachTime(t *testing.T) {
	s := Schedule{Provider: "acme", Times: []int{120, 600, 840}, Enabled: true} // 02:00, 10:00, 14:00 UTC
	now := day(12, 0)
	// A job as buildJobs makes it: its main step is its ingest run.
	job := func(id int, at time.Time, status string) *Job {
		return &Job{Kind: "Crawl", StartedAt: at, Status: status,
			Steps: []*JobStep{{Run: &Run{ID: id, Action: "ingest", StartedAt: at}}}}
	}
	crawls := []*Job{
		job(1, day(1, 50), OutcomeSuccess),             // a manual crawl just before 02:00 covers it
		job(2, day(10, 1), OutcomeFailed),              // the 10:00 run
		job(3, day(10, 30), OutcomeSuccess),            // a later one in the same window: not the slot's
		job(4, day(0, 0).Add(-9*time.Hour), "running"), // yesterday 15:00: inside yesterday 14:00's window
	}
	got := slotRuns(s, crawls, now)
	want := []string{OutcomeSuccess, OutcomeFailed, "running"} // 14:00 today is ahead, so yesterday's 14:00 counts
	wantRun := []int{1, 2, 4}                                  // the run a click on each square opens
	for i, w := range want {
		if got[i].Status != w || got[i].Run != wantRun[i] {
			t.Errorf("slot %d (%d): %q run %d, want %q run %d", i, got[i].Min, got[i].Status, got[i].Run, w, wantRun[i])
		}
	}

	// Inside its window with nothing started yet: pending; past it: none.
	got = slotRuns(Schedule{Times: []int{690}, Enabled: true}, nil, now) // 11:30, window runs to 11:30 tomorrow
	if got[0].Status != "pending" {
		t.Errorf("an open window with no crawl = %q, want pending", got[0].Status)
	}
	got = slotRuns(Schedule{Times: []int{600, 660}, Enabled: true}, nil, now) // 10:00 window closed at 11:00
	if got[0].Status != "none" || got[1].Status != "pending" {
		t.Errorf("got %q/%q, want none/pending", got[0].Status, got[1].Status)
	}
}
