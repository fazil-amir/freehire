package main

import (
	"strings"
	"testing"
	"time"
)

// Every page must parse and render. The handlers' own tests never load the
// templates, so without this a broken template only surfaces as a server
// that exits on start.
func TestTemplates_EveryPageRenders(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	run := &Run{ID: 1, Action: "ingest", Provider: "acme", Status: StatusDone, StartedAt: time.Now(), FinishedAt: time.Now()}
	pages := map[string]any{
		"login":     map[string]any{"Error": ""},
		"catalog":   catalogPageData{Active: "catalog", Providers: []ProviderSummary{{Provider: "acme", CompanyCount: 1}}},
		"schedules": schedulesPageData{Active: "schedules", Schedules: []scheduleRow{{Schedule: Schedule{ID: "s", Provider: "acme"}, LatestRun: run}}, Explanations: map[int]string{1: "Why: because."}},
		"activity":  activityPageData{Active: "activity", Runs: []*Run{run}, Explanations: map[int]string{}},
	}
	for name, data := range pages {
		var b strings.Builder
		if err := tmpl.tmpl.ExecuteTemplate(&b, name, data); err != nil {
			t.Errorf("render %s: %v", name, err)
		}
	}
}
