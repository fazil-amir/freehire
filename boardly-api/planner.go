package main

import (
	"math"
	"os"
	"strconv"
	"time"
)

// The planned day: 96 slots of 15 minutes, counted from 00:00 UTC (the
// container's clock). A schedule's runs are times on that grid the OPERATOR
// picks — nothing here moves them. The day's load (how many crawls start in
// each slot) is only measured: a slot at SCHEDULE_CAPACITY is booked, and a
// run placed there anyway waits in the scheduler's queue. Every run is one
// slot: nothing guesses how long a crawl takes.
const (
	slotMinutes = 15
	slotsPerDay = 24 * 60 / slotMinutes
)

// perDayOptions are the frequencies an older "every N" schedule converts
// to on load (see convertLegacy). Each divides the day into whole slots.
var perDayOptions = []int{2, 3, 4, 6, 8, 12, 24}

// scheduleCapacity is how many scheduled crawls may run at once: a slot
// holding this many runs is booked, and a scheduled run due while this many
// crawls are in flight waits for one to end. SCHEDULE_CAPACITY sets it;
// unset or unreadable means 2.
func scheduleCapacity() int {
	if n, err := strconv.Atoi(os.Getenv("SCHEDULE_CAPACITY")); err == nil && n > 0 {
		return n
	}
	return 2
}

// loadProfile is how many planned crawls start in each slot of the day,
// over every enabled schedule.
func loadProfile(schedules []Schedule) [slotsPerDay]int {
	var load [slotsPerDay]int
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		for _, m := range s.Times {
			load[m/slotMinutes%slotsPerDay]++
		}
	}
	return load
}

// perDayFromInterval converts a pre-planner "every N" schedule to the
// nearest crawls-per-day option: every 30m → 24×, every 6h → 4×, anything
// rarer than 12h → 2×.
func perDayFromInterval(secs int64) int {
	if secs <= 0 {
		return 2
	}
	want := 86400 / float64(secs)
	best := perDayOptions[0]
	for _, o := range perDayOptions {
		if math.Abs(float64(o)-want) < math.Abs(float64(best)-want) {
			best = o
		}
	}
	return best
}

// slotStart is the time of slot index s (0..95) on the UTC day of t.
func slotStart(t time.Time, s int) time.Time {
	u := t.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return day.Add(time.Duration(s*slotMinutes) * time.Minute)
}
