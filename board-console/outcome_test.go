package main

import "testing"

func TestOutcome(t *testing.T) {
	cases := []struct {
		name string
		run  Run
		want RunOutcome
	}{
		{"greenhouse: 225k jobs, one board lost → partial", Run{Action: "ingest", Status: StatusFailed, Err: "exit status 1", Stderr: `2026/09/24 10:20:00 ingest: progress 4000/4957 boards crawled
2026/09/24 10:38:33 ingest done: provider=greenhouse providers=1 ingested=225573 failed=1 skipped=0 rejected=0 unreadable=0`},
			RunOutcome{OutcomePartial, "225,573 jobs · 1 of 4,957 boards failed"}},
		{"keka: no progress line → no total", Run{Action: "ingest", Status: StatusFailed, Err: "exit status 1", Stderr: `2026/09/24 08:47:21 ingest done: provider=keka providers=1 ingested=4278 failed=2 skipped=0 rejected=0 unreadable=0`},
			RunOutcome{OutcomePartial, "4,278 jobs · 2 boards failed"}},
		{"bayt: every board refused → failed", Run{Action: "ingest", Status: StatusFailed, Err: "exit status 1", Stderr: `ingest done: provider=bayt providers=1 ingested=0 failed=8 skipped=0 rejected=0 unreadable=0`},
			RunOutcome{OutcomeFailed, "0 jobs · 8 boards failed"}},
		{"clean crawl", Run{Action: "ingest", Status: StatusDone, Stderr: `ingest done: provider=jobdanmark providers=1 ingested=14180 failed=0 skipped=0 rejected=244 unreadable=0`},
			RunOutcome{OutcomeSuccess, "14,180 jobs"}},
		{"crawl with nothing new", Run{Action: "ingest", Status: StatusDone, Stderr: `ingest done: provider=2gis providers=1 ingested=0 failed=0 skipped=0 rejected=0 unreadable=0`},
			RunOutcome{OutcomeSuccess, "no new jobs"}},
		{"crawl cut off by a restart", Run{Action: "ingest", Status: StatusFailed, Err: "interrupted (board-console restarted)", Stderr: `ingest: progress 0/1 boards crawled`},
			RunOutcome{OutcomeFailed, "interrupted (board-console restarted)"}},
		{"reindex refused on disk: the reason, not 'exit status 1'", Run{Action: "reindex", Status: StatusFailed, Err: "exit status 1", Stderr: `2026/09/24 08:47:21 reindex: refusing rebuild — free 19GiB is below the 20GiB floor`},
			RunOutcome{OutcomeFailed, "reindex: refusing rebuild — free 19GiB is below the 20GiB floor"}},
		{"reindex done", Run{Action: "reindex", Status: StatusDone, Stderr: `reindex done: target=facet scope=full indexed=1812 skipped=0`},
			RunOutcome{OutcomeSuccess, "1,812 indexed"}},
		{"add-boards", Run{Action: "add-boards", Status: StatusDone, Stderr: `bulk-add-boards: done. added=3101 duplicate=0 failed=0 elapsed=0s`},
			RunOutcome{OutcomeSuccess, "3,101 added · 0 already there"}},
		{"cleanup preview", Run{Action: "close-chronic-boards", Status: StatusDone, Stderr: `close-chronic-boards: 2 chronic board(s) (0 skipped as region-ambiguous), would close 5 job(s) total
close-chronic-boards: 1 empty-feed board(s) (0 skipped as region-ambiguous), would close 3 job(s) total`},
			RunOutcome{OutcomeSuccess, "would close 8 jobs"}},
		{"recount", Run{Action: "recount-companies", Status: StatusDone, Stderr: `recount-companies done: companies updated=1`},
			RunOutcome{OutcomeSuccess, "1 company updated"}},
		{"still running", Run{Action: "ingest", Status: StatusRunning}, RunOutcome{Status: "running"}},
	}
	for _, c := range cases {
		if got := c.run.Outcome(); got != c.want {
			t.Errorf("%s:\n got  %+v\n want %+v", c.name, got, c.want)
		}
	}
}
