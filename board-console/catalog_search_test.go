package main

import "testing"

func TestBuildCatalog_MultiWordSearchMatchesReorderedTerms(t *testing.T) {
	rows := []BoardRow{
		{Provider: "greenhouse", Board: "1", Company: "Recruit at Google"},
		{Provider: "lever", Board: "1", Company: "Acme Corp"},
	}

	for _, query := range []string{"green house", "google recruit", "greenhouse", "GOOGLE"} {
		out := buildCatalog(rows, "", query, nil)
		if len(out) != 1 || out[0].Provider != "greenhouse" {
			t.Errorf("query %q: want only greenhouse, got %+v", query, out)
		}
	}
}

func TestBuildCatalog_SearchRequiresAllTerms(t *testing.T) {
	rows := []BoardRow{
		{Provider: "greenhouse", Board: "1", Company: "Acme Corp"},
	}
	out := buildCatalog(rows, "", "acme nonexistent", nil)
	if len(out) != 0 {
		t.Errorf("want no match when one term is absent, got %+v", out)
	}
}

func TestBuildCatalog_AddedCountFromDBCappedAtCandidates(t *testing.T) {
	rows := []BoardRow{
		{Provider: "greenhouse", Board: "1", Company: "Acme Corp"},
	}
	// The live DB can show more added boards than this provider has
	// candidate rows in board-console's own CSV — the display must cap at
	// the candidate count, never show X > Y.
	out := buildCatalog(rows, "", "", map[string]int{"greenhouse": 5})
	if len(out) != 1 || out[0].AddedCount != 1 || out[0].CompanyCount != 1 {
		t.Fatalf("want AddedCount capped at CompanyCount (1), got %+v", out)
	}
}

func TestBuildCatalog_CountsBoardsByFreehireIdentityNotRows(t *testing.T) {
	// freehire's boards are UNIQUE on (provider, lower(board)): these three
	// rows are two boards, and a boardless aggregator listed twice is one.
	// Counting rows left such a provider "partially added" forever.
	rows := []BoardRow{
		{Provider: "ashby", Board: "AeroVect", Company: "Aerovect"},
		{Provider: "ashby", Board: "aerovect", Company: "AeroVect"},
		{Provider: "ashby", Board: "arch", Company: "Arch"},
		{Provider: "jobdanmark", Board: "", Company: "JobiDanmark"},
		{Provider: "jobdanmark", Board: "", Company: "Jobdanmark"},
	}
	out := buildCatalog(rows, "", "", map[string]int{"ashby": 2, "jobdanmark": 1})
	for _, p := range out {
		if p.AddedCount != p.CompanyCount { // fully added
			t.Errorf("%s: want fully added, got %d/%d", p.Provider, p.AddedCount, p.CompanyCount)
		}
	}
}
