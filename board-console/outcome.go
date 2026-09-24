package main

import (
	"regexp"
	"strconv"
	"strings"
)

// RunOutcome is what a run achieved, as the operator should read it — not
// the subprocess's exit code. freehire's ingest exits non-zero when ANY
// board failed, so a crawl that stored 225,573 jobs and lost one board is
// "failed" by exit code; here it is Partial, with the numbers alongside.
//
// It is derived from the run's own summary lines every time it is shown and
// never stored, so it applies to runs recorded before it existed.
type RunOutcome struct {
	Status  string // "success", "partial", "failed", "running", "queued"
	Summary string // one line under the badge; empty when there is nothing to add
}

const (
	OutcomeSuccess = "success"
	OutcomePartial = "partial"
	OutcomeFailed  = "failed"
)

var (
	ingestDoneRe   = regexp.MustCompile(`ingest done: .*\bingested=(\d+) failed=(\d+)`)
	ingestTotalRe  = regexp.MustCompile(`ingest: progress \d+/(\d+) boards crawled`)
	addDoneRe      = regexp.MustCompile(`bulk-add-boards: done\. added=(\d+) duplicate=(\d+) failed=(\d+)`)
	reindexDoneRe  = regexp.MustCompile(`reindex(?:-companies)? done: .*\bindexed=(\d+)`)
	recountDoneRe  = regexp.MustCompile(`recount-companies done: companies updated=(\d+)`)
	removeDoneRe   = regexp.MustCompile(`remove-boards: done\. retired=(\d+) failed=(\d+) schedules_deleted=(\d+)`)
	cleanupJobsRe  = regexp.MustCompile(`close-chronic-boards: \d+ .*?(would close|closed) (\d+) job\(s\) total`)
	logTimestampRe = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
)

// Outcome reads the run's result from its output. The rules, per action:
//   - ingest: jobs came in and some boards failed → Partial; nothing came
//     in while boards failed, or the program died before its summary →
//     Failed. Failed means nothing got done, never "not everything did".
//   - everything else: its exit code, with the counts from its summary
//     line, or on failure the line that says why.
func (r *Run) Outcome() RunOutcome {
	switch r.Status {
	case StatusRunning:
		return RunOutcome{Status: "running"}
	case StatusQueued:
		return RunOutcome{Status: "queued"}
	}

	if r.Action == "ingest" {
		if m := lastMatch(ingestDoneRe, r.Stderr); m != nil {
			ingested, failed := atoi(m[1]), atoi(m[2])
			jobs := plural(ingested, "job", "jobs")
			if ingested == 0 && failed == 0 {
				jobs = "no new jobs"
			}
			if failed == 0 {
				return RunOutcome{Status: OutcomeSuccess, Summary: jobs}
			}
			boards := plural(failed, "board", "boards") + " failed"
			if t := lastMatch(ingestTotalRe, r.Stderr); t != nil && atoi(t[1]) > 1 {
				boards = strconv.Itoa(failed) + " of " + thousands(atoi(t[1])) + " boards failed"
			}
			if ingested > 0 {
				return RunOutcome{Status: OutcomePartial, Summary: jobs + " · " + boards}
			}
			return RunOutcome{Status: OutcomeFailed, Summary: "0 jobs · " + boards}
		}
	}

	if r.Action == "remove-boards" {
		if m := lastMatch(removeDoneRe, r.Stderr); m != nil {
			retired, failed, schedules := atoi(m[1]), atoi(m[2]), atoi(m[3])
			s := plural(retired, "board", "boards") + " retired"
			if schedules > 0 {
				s += " · " + plural(schedules, "schedule", "schedules") + " deleted"
			}
			switch {
			case failed == 0:
				return RunOutcome{Status: OutcomeSuccess, Summary: s}
			case retired > 0:
				return RunOutcome{Status: OutcomePartial, Summary: s + " · " + strconv.Itoa(failed) + " failed"}
			default:
				return RunOutcome{Status: OutcomeFailed, Summary: "0 boards retired · " + strconv.Itoa(failed) + " failed"}
			}
		}
	}

	if r.Status == StatusFailed {
		return RunOutcome{Status: OutcomeFailed, Summary: r.failureReason()}
	}
	return RunOutcome{Status: OutcomeSuccess, Summary: r.successSummary()}
}

// successSummary is the counts from a finished run's own summary line.
func (r *Run) successSummary() string {
	out := r.Stderr + "\n" + r.Stdout
	if m := lastMatch(addDoneRe, out); m != nil {
		s := thousands(atoi(m[1])) + " added · " + thousands(atoi(m[2])) + " already there"
		if f := atoi(m[3]); f > 0 {
			s += " · " + thousands(f) + " failed"
		}
		return s
	}
	if m := lastMatch(reindexDoneRe, out); m != nil {
		return thousands(atoi(m[1])) + " indexed"
	}
	if m := lastMatch(recountDoneRe, out); m != nil {
		return plural(atoi(m[1]), "company", "companies") + " updated"
	}
	if ms := cleanupJobsRe.FindAllStringSubmatch(out, -1); len(ms) > 0 {
		total := 0
		for _, m := range ms {
			total += atoi(m[2])
		}
		return ms[len(ms)-1][1] + " " + plural(total, "job", "jobs")
	}
	return ""
}

// failureReason is the most telling line about why a run failed: the
// recorded error when it is more than a bare exit status, otherwise the
// last line the program printed.
func (r *Run) failureReason() string {
	if r.Err != "" && !strings.HasPrefix(r.Err, "exit status") {
		return shorten(r.Err)
	}
	lines := strings.Split(strings.TrimSpace(r.Stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(logTimestampRe.ReplaceAllString(lines[i], ""))
		if line != "" {
			return shorten(line)
		}
	}
	return shorten(r.Err)
}

func lastMatch(re *regexp.Regexp, s string) []string {
	all := re.FindAllStringSubmatch(s, -1)
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
}

// thousands formats 225573 as "225,573".
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// shorten keeps a reason to one readable line; the full text is in the log.
func shorten(s string) string {
	const max = 110
	r := []rune(s) // by character: a byte cut could split "—" in half
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}
