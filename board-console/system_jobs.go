package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// System jobs are Board Console's own daily chores, not a provider's
// crawls: the dead-board cleanup and the company recount. Each runs once a
// day at a time the operator can change, can be paused, and records its
// last real run. They live in system.json, apart from schedule.json,
// because they are built in: they can be re-timed and paused, never added
// or deleted.
const (
	sysCleanup = "cleanup"
	sysRecount = "recount"
)

// systemJobInfo is what a UI says about a system job; not stored.
type systemJobInfo struct {
	Key, Name, Description string
	DefaultMin             int    // its time until the operator changes it, minutes after 00:00 UTC
	ActivityURL            string // where its runs are listed
}

// systemJobInfos, in the order the Schedules page lists them.
var systemJobInfos = []systemJobInfo{
	{
		Key: sysCleanup, Name: "Dead-board cleanup",
		Description: "Closes the jobs of boards that have been unreachable for 60 days or empty for 30 (close-chronic-boards), then reindexes search and recounts companies.",
		DefaultMin:  3 * 60,
		ActivityURL: "/activity?view=cleanup",
	},
	{
		Key: sysRecount, Name: "Recount companies",
		Description: "Recomputes every company's open-job count and facets (recount-companies), then rebuilds company search (reindex-companies) so the new counts show.",
		DefaultMin:  5 * 60,
		ActivityURL: "/activity?action=recount-companies",
	},
}

func systemJobInfoOf(key string) (systemJobInfo, bool) {
	for _, i := range systemJobInfos {
		if i.Key == key {
			return i, true
		}
	}
	return systemJobInfo{}, false
}

// SystemJob is one system job's settings and last run, as stored.
type SystemJob struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
	Minute  int    `json:"minute"` // its daily time, minutes after 00:00 UTC
	// Served is the latest daily slot already taken care of — by a run, or
	// by being re-timed or resumed (which must not fire it on the spot).
	// A slot is due once it is later than this.
	Served time.Time `json:"served,omitzero"`
	// LastRun is when its last real run STARTED (the cleanup's Preview is a
	// dry run and never counts), LastStatus how it ended.
	LastRun    time.Time `json:"last_run,omitzero"`
	LastStatus RunStatus `json:"last_status,omitempty"`
}

// slotAt is the most recent occurrence of minute (after 00:00 UTC) at or
// before t.
func slotAt(t time.Time, minute int) time.Time {
	s := slotStart(t, 0).Add(time.Duration(minute) * time.Minute)
	if s.After(t) {
		s = s.Add(-24 * time.Hour)
	}
	return s
}

// Due reports whether the latest slot has gone unserved. Comparing against
// the latest slot only — never counting how many were missed — is what
// makes a process that was down for three days run once, not three times,
// and a restart after today's run not run again.
func (j SystemJob) Due(now time.Time) bool {
	return j.Enabled && slotAt(now, j.Minute).After(j.Served)
}

// NextRun is when it next fires: now while a slot is unserved, zero while
// paused, otherwise its next daily time.
func (j SystemJob) NextRun(now time.Time) time.Time {
	switch {
	case !j.Enabled:
		return time.Time{}
	case j.Due(now):
		return now
	}
	return slotAt(now, j.Minute).Add(24 * time.Hour)
}

// SystemStore persists the system jobs to system.json, atomically, the same
// temp-file-then-rename pattern the schedule store uses.
type SystemStore struct {
	path string

	mu   sync.Mutex
	jobs map[string]*SystemJob

	// saveMu serialises save, so an older snapshot never lands on top of a
	// newer one.
	saveMu sync.Mutex
}

// NewSystemStore loads system.json. A job missing from it — every job on a
// fresh install — starts with its default time and its current slot
// already served: an unserved slot would fire an --apply cleanup on the
// first tick after deploy, before anyone has seen a Preview. Its first run
// is its next daily time.
//
// legacyCleanup is the cleanup.json system.json replaced: its last run is
// carried over once, when the cleanup is first added to system.json.
func NewSystemStore(path, legacyCleanup string) (*SystemStore, error) {
	s := &SystemStore{path: path, jobs: map[string]*SystemJob{}}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	case len(bytes.TrimSpace(data)) > 0: // an emptied file is a fresh start
		var list []SystemJob
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for i := range list {
			s.jobs[list[i].Key] = &list[i]
		}
	}

	now := time.Now().Round(0)
	added := false
	for _, info := range systemJobInfos {
		if _, ok := s.jobs[info.Key]; ok {
			continue
		}
		j := &SystemJob{Key: info.Key, Enabled: true, Minute: info.DefaultMin, Served: slotAt(now, info.DefaultMin)}
		if info.Key == sysCleanup {
			carryLegacyCleanup(j, legacyCleanup)
		}
		s.jobs[info.Key] = j
		added = true
	}
	if added {
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// carryLegacyCleanup copies cleanup.json's last run into the cleanup job.
// A last run without a status was only the clock a fresh install started,
// not a run.
func carryLegacyCleanup(j *SystemJob, path string) {
	data, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return
	}
	var old struct {
		LastRun    time.Time `json:"last_run"`
		LastStatus RunStatus `json:"last_status"`
	}
	if json.Unmarshal(data, &old) != nil || old.LastStatus == "" {
		return
	}
	j.LastRun, j.LastStatus = old.LastRun, old.LastStatus
	if slot := slotAt(old.LastRun, j.Minute); slot.After(j.Served) {
		j.Served = slot
	}
}

// Get returns a copy of one system job.
func (s *SystemStore) Get(key string) SystemJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[key]; ok {
		return *j
	}
	return SystemJob{Key: key}
}

// List returns every system job, in systemJobInfos order.
func (s *SystemStore) List() []SystemJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SystemJob, 0, len(systemJobInfos))
	for _, info := range systemJobInfos {
		if j, ok := s.jobs[info.Key]; ok {
			out = append(out, *j)
		}
	}
	return out
}

// Record stamps a finished real run. ranAt is when it STARTED, so a run
// that began at 03:00 and ended at 03:40 still serves the 03:00 slot.
func (s *SystemStore) Record(key string, ranAt time.Time, status RunStatus) error {
	return s.update(key, func(j *SystemJob) {
		j.LastRun, j.LastStatus = ranAt, status
		if slot := slotAt(ranAt, j.Minute); slot.After(j.Served) {
			j.Served = slot
		}
	})
}

// SetTime re-times a job. Its current slot counts as served, so a time
// moved to earlier today waits for tomorrow instead of firing at once.
func (s *SystemStore) SetTime(key string, minute int) error {
	if !validTime(minute) {
		return fmt.Errorf("the time must be on the 15-minute grid")
	}
	now := time.Now().Round(0)
	return s.update(key, func(j *SystemJob) {
		j.Minute = minute
		j.Served = slotAt(now, minute)
	})
}

// Toggle pauses or resumes a job. Resuming serves the current slot, so a
// job paused over its time does not fire the moment it is switched back on.
func (s *SystemStore) Toggle(key string) error {
	now := time.Now().Round(0)
	return s.update(key, func(j *SystemJob) {
		j.Enabled = !j.Enabled
		if j.Enabled {
			j.Served = slotAt(now, j.Minute)
		}
	})
}

func (s *SystemStore) update(key string, fn func(*SystemJob)) error {
	s.mu.Lock()
	j, ok := s.jobs[key]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown system job %q", key)
	}
	fn(j)
	s.mu.Unlock()
	return s.save()
}

// save writes system.json. Callers must NOT hold s.mu.
func (s *SystemStore) save() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	data, err := json.MarshalIndent(s.List(), "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "system-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}
