package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeModel is an OpenAI-compatible /chat/completions that records what it
// was sent and answers with reply (or a JSON error when status != 200).
func fakeModel(t *testing.T, status int, reply string, calls *int32, got *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct{ Role, Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			*got = body.Messages[0].Content + "\n---\n" + body.Messages[1].Content
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
}

func testRun() *Run {
	return &Run{
		ID: 7, Action: "ingest", Provider: "keka", Status: StatusFailed, Err: "exit status 1",
		StartedAt: time.Now().Add(-25 * time.Second), FinishedAt: time.Now(),
		Stderr: `ingest: keka board "swaransoft" failed: invalid character 'i' looking for beginning of value
ingest done: provider=keka providers=1 ingested=4278 failed=2`,
	}
}

func TestExplain_SendsTheRunAndContextAndCachesTheAnswer(t *testing.T) {
	var calls int32
	var sent string
	srv := fakeModel(t, http.StatusOK, "What happened: fine.", &calls, &sent)
	defer srv.Close()
	e := &Explainer{apiKey: "test-key", model: "m", baseURL: srv.URL, client: srv.Client(), cache: map[int]string{}}

	history := []*Run{{Action: "ingest", Provider: "keka", Status: StatusDone, StartedAt: time.Now().Add(-time.Hour)}}
	got, err := e.Explain(context.Background(), testRun(), history)
	if err != nil || got != "What happened: fine." {
		t.Fatalf("Explain = %q, %v", got, err)
	}
	for _, want := range []string{"Boardly", "Provider: keka", "ingested=4278", "Exit error: exit status 1", "Earlier runs"} {
		if !strings.Contains(sent, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}

	// A finished run's answer is cached: asking again makes no call.
	if _, err := e.Explain(context.Background(), testRun(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("want 1 model call (then the cache), got %d", calls)
	}
	if e.Cached()[7] != "What happened: fine." {
		t.Fatal("answer not in the cache the page renders from")
	}
}

func TestExplain_ReportsTheModelsError(t *testing.T) {
	var calls int32
	var sent string
	srv := fakeModel(t, http.StatusUnauthorized, "", &calls, &sent)
	defer srv.Close()
	e := &Explainer{apiKey: "test-key", model: "m", baseURL: srv.URL, client: srv.Client(), cache: map[int]string{}}

	_, err := e.Explain(context.Background(), testRun(), nil)
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("want the model's own error message, got %v", err)
	}
	if len(e.Cached()) != 0 {
		t.Fatal("a failure must not be cached")
	}
}

func TestLogTail_KeepsTheEndWithinBudget(t *testing.T) {
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, strings.Repeat("x", 50))
	}
	lines = append(lines, "ingest done: the last line", strings.Repeat("y", 5000))
	out := logTail(strings.Join(lines, "\n"), 2000)
	if len(out) > 2100 {
		t.Fatalf("tail is %d bytes, over the budget", len(out))
	}
	if !strings.Contains(out, "ingest done: the last line") || !strings.HasPrefix(out, "[...earlier output omitted...]") {
		t.Fatalf("tail must keep the end and say it dropped the start:\n%s", out[:200])
	}
	if !strings.Contains(out, "[...]") {
		t.Fatal("an over-long line must be capped")
	}
}

func TestExplainSections(t *testing.T) {
	got := explainSections("What happened: 4278 jobs ingested.\n\nWhy: two boards returned HTML.\nSee the keka lines.\nWhat to do: Nothing — this is expected.")
	want := []explainSection{
		{"What happened", "4278 jobs ingested."},
		{"Why", "two boards returned HTML.\nSee the keka lines."},
		{"What to do", "Nothing — this is expected."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sections: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("section %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// An answer that ignored the format is kept whole, never dropped.
	if s := explainSections("Just a sentence."); len(s) != 1 || s[0].Label != "" || s[0].Body != "Just a sentence." {
		t.Fatalf("unformatted answer: %+v", s)
	}
}
