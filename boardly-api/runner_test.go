package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartCrawl_FullReCrawlPassesRefetchAll(t *testing.T) {
	sched, _, activity := newTestScheduler(t)
	r := sched.runner
	r.bin.Ingest = writeScript(t, `echo "refetch=[$INGEST_REFETCH_ALL]" >&2`)

	done := make(chan struct{})
	r.StartCrawl("acme", false, true, func(error) { close(done) })
	<-done
	r.StartCrawl("acme", false, false, nil) // an ordinary crawl after it
	waitFor(t, "the ordinary crawl", func() bool { return countIngests(activity) == 2 })
	waitFor(t, "both crawls to finish", func() bool { return !r.Crawling("acme") })

	runs := activity.List() // newest first
	full, plain := runs[1], runs[0]
	if full.Label != "full re-crawl" || !strings.Contains(full.Stderr, "refetch=[1]") {
		t.Errorf("full re-crawl: label %q, output %q", full.Label, full.Stderr)
	}
	if plain.Label != "" || !strings.Contains(plain.Stderr, "refetch=[]") {
		t.Errorf("ordinary crawl must not refetch: label %q, output %q", plain.Label, plain.Stderr)
	}
}

func TestCompanyRefresh_WaitsForARunningReindex(t *testing.T) {
	sched, _, activity := newTestScheduler(t)
	r := sched.runner
	r.bin.Reindex = writeScript(t, "sleep 1")
	r.bin.RecountCompanies = writeScript(t, "true")
	r.bin.ReindexCompanies = writeScript(t, "true")

	r.queueReindex()
	waitFor(t, "the reindex to start", func() bool {
		for _, run := range activity.List() {
			if run.Action == "reindex" {
				return true
			}
		}
		return false
	})
	if !r.StartCompanyRefresh() {
		t.Fatal("company refresh did not start")
	}
	if r.StartCompanyRefresh() {
		t.Fatal("a second company refresh must be refused while one runs")
	}

	byAction := map[string]*Run{}
	waitFor(t, "reindex-companies to finish", func() bool {
		for _, run := range activity.List() {
			byAction[run.Action] = run
		}
		rc := byAction["reindex-companies"]
		return rc != nil && rc.Status == StatusDone
	})
	reindex, companies := byAction["reindex"], byAction["reindex-companies"]
	if companies.StartedAt.Before(reindex.FinishedAt) {
		t.Fatalf("reindex-companies started %v, before the jobs reindex finished %v",
			companies.StartedAt, reindex.FinishedAt)
	}
}
