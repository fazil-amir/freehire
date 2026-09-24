package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveProvider_RetiresEveryBoardInOneRunAndDropsSchedules(t *testing.T) {
	sched, store, activity := newTestScheduler(t) // has one schedule for "acme"
	r := sched.runner
	calls := filepath.Join(t.TempDir(), "calls")
	// Fake add-board: records its arguments; "gone" is already retired.
	r.bin.AddBoard = writeScript(t, `echo "$*" >> `+calls+`
case "$*" in *--board=gone*) echo "no live board" >&2; exit 1;; esac
echo "add-board: retired $*" >&2`)

	boards := []liveBoard{{"acme", "one", ""}, {"acme", "two", "eu"}, {"acme", "gone", ""}}
	var deleted int
	if !r.StartRemoveProvider("acme", boards, func() int { deleted, _ = store.DeleteByProvider("acme"); return deleted }) {
		t.Fatal("removal did not start")
	}
	waitFor(t, "the removal to finish", func() bool { return !r.Crawling("acme") })

	got, _ := os.ReadFile(calls)
	for _, want := range []string{
		"--retire --provider=acme --board=one --apply",
		"--retire --provider=acme --board=two --apply --region=eu",
		"--retire --provider=acme --board=gone --apply",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("add-board was not called with %q; calls:\n%s", want, got)
		}
	}
	if deleted != 1 || len(store.List()) != 0 {
		t.Errorf("want the provider's schedule deleted, deleted=%d left=%d", deleted, len(store.List()))
	}

	var runs []*Run
	for _, run := range activity.List() {
		if run.Action == "remove-boards" {
			runs = append(runs, run)
		}
	}
	if len(runs) != 1 {
		t.Fatalf("want ONE remove-boards run for all boards, got %d", len(runs))
	}
	o := runs[0].Outcome()
	if o.Status != OutcomePartial || o.Summary != "2 boards retired · 1 schedule deleted · 1 failed" {
		t.Errorf("outcome = %+v", o)
	}
}

func TestRemoveProvider_RefusedWhileTheProviderIsCrawling(t *testing.T) {
	sched, store, _ := newTestScheduler(t)
	r := sched.runner
	done := make(chan struct{})
	if !r.StartCrawl("acme", false, false, func(error) { close(done) }) {
		t.Fatal("crawl did not start")
	}
	if r.StartRemoveProvider("acme", []liveBoard{{"acme", "one", ""}}, func() int { n, _ := store.DeleteByProvider("acme"); return n }) {
		t.Fatal("a removal must be refused while the provider is crawling")
	}
	if len(store.List()) != 1 {
		t.Fatal("a refused removal must not delete schedules")
	}
	<-done
}
