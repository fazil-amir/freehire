package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newAPITestServer is the API over a test scheduler (one added provider,
// "acme", with a schedule), served the way main mounts it.
func newAPITestServer(t *testing.T) (*httptest.Server, *App) {
	t.Helper()
	sched, store, activity := newTestScheduler(t)
	app := &App{
		csv: sched.runner.csv, activity: activity, schedules: store,
		runner: sched.runner, system: sched.runner.system, explainer: NewExplainerFromEnv(),
		docker: &DockerProxy{}, cpu: &CPUSampler{}, dataDir: t.TempDir(),
	}
	mux := http.NewServeMux()
	registerAPI(mux, app)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, app
}

func apiDo(t *testing.T, method, url, body string, header map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestAPI_KeyAndCORS(t *testing.T) {
	t.Setenv("BOARDLY_API_KEY", "s3cret")
	srv, _ := newAPITestServer(t)

	if resp, _ := apiDo(t, "GET", srv.URL+"/api/v1/meta", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: status %d, want 401", resp.StatusCode)
	}
	if resp, _ := apiDo(t, "GET", srv.URL+"/api/v1/meta", "", map[string]string{"Authorization": "Bearer wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong key: status %d, want 401", resp.StatusCode)
	}
	resp, meta := apiDo(t, "GET", srv.URL+"/api/v1/meta", "", map[string]string{"Authorization": "Bearer s3cret", "Origin": "http://localhost:5173"})
	if resp.StatusCode != 200 || meta["scheduleCapacity"] == nil {
		t.Fatalf("right key: status %d, body %v", resp.StatusCode, meta)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allowed origin not echoed: %q", got)
	}

	// A preflight carries no key and must still be answered, with the headers.
	resp, _ = apiDo(t, "OPTIONS", srv.URL+"/api/v1/schedules", "", map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "PUT"})
	if resp.StatusCode != http.StatusNoContent || !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "PUT") {
		t.Errorf("preflight: status %d, methods %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Methods"))
	}
	// An origin not on the list gets no CORS headers.
	resp, _ = apiDo(t, "GET", srv.URL+"/api/v1/meta", "", map[string]string{"Authorization": "Bearer s3cret", "Origin": "https://evil.example"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("a foreign origin must not be allowed")
	}
}

// Several origins — a laptop's dev server and the deployed app — share one API;
// spaces and a trailing slash in the list are forgiven.
func TestAPI_CORSMultipleOrigins(t *testing.T) {
	t.Setenv("BOARDLY_CORS_ORIGINS", "http://localhost:5173, http://1.2.3.4:5173/")
	srv, _ := newAPITestServer(t)

	for _, origin := range []string{"http://localhost:5173", "http://1.2.3.4:5173"} {
		resp, _ := apiDo(t, "GET", srv.URL+"/api/v1/meta", "", map[string]string{"Origin": origin})
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("origin %s: allowed %q, want it echoed", origin, got)
		}
	}
	resp, _ := apiDo(t, "GET", srv.URL+"/api/v1/meta", "", map[string]string{"Origin": "http://1.2.3.4:8080"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin allowed: %q", got)
	}
}

func TestAPI_CatalogAndActions(t *testing.T) {
	t.Setenv("BOARDLY_API_KEY", "")
	srv, app := newAPITestServer(t)

	resp, cat := apiDo(t, "GET", srv.URL+"/api/v1/catalog?show=all", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("catalog: %d", resp.StatusCode)
	}
	providers, _ := cat["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("want acme in the catalog, got %v", cat)
	}
	acme := providers[0].(map[string]any)
	if acme["provider"] != "acme" || acme["schedule"] == nil || acme["addedCount"].(float64) != 1 {
		t.Errorf("acme = %v", acme)
	}

	// New provider: an unknown one is refused with the page's own message.
	resp, body := apiDo(t, "POST", srv.URL+"/api/v1/providers", `{"provider":"nope","company":"X"}`, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body["error"].(string), "known") {
		t.Errorf("new provider: %d %v", resp.StatusCode, body)
	}

	// Schedule upsert by provider: saving without an id updates acme's one.
	resp, body = apiDo(t, "PUT", srv.URL+"/api/v1/schedules", `{"provider":"acme","times":[60,600]}`, nil)
	if resp.StatusCode != 200 || len(app.schedules.List()) != 1 {
		t.Fatalf("save: %d %v (schedules %d)", resp.StatusCode, body, len(app.schedules.List()))
	}
	if got := app.schedules.List()[0].Times; !equalInts(got, []int{60, 600}) {
		t.Errorf("times = %v", got)
	}
	resp, _ = apiDo(t, "PUT", srv.URL+"/api/v1/schedules", `{"provider":"acme","times":[7]}`, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a time off the grid must be a 422, got %d", resp.StatusCode)
	}

	// Crawl: accepted, then a second one while it runs is a conflict.
	if resp, _ = apiDo(t, "POST", srv.URL+"/api/v1/providers/acme/crawl", "", nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("crawl: %d", resp.StatusCode)
	}
	if resp, _ = apiDo(t, "POST", srv.URL+"/api/v1/providers/acme/crawl", `{"refetchAll":true}`, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("a second crawl while one runs must be 409, got %d", resp.StatusCode)
	}
	waitFor(t, "the crawl to finish", func() bool { return !app.runner.Crawling("acme") })

	// Delete the schedule.
	id := app.schedules.List()[0].ID
	if resp, _ = apiDo(t, "DELETE", srv.URL+"/api/v1/schedules/"+id, "", nil); resp.StatusCode != http.StatusNoContent || len(app.schedules.List()) != 0 {
		t.Errorf("delete: %d, left %d", resp.StatusCode, len(app.schedules.List()))
	}
}

func TestAPI_SchedulesActivityAndRuns(t *testing.T) {
	t.Setenv("BOARDLY_API_KEY", "")
	srv, app := newAPITestServer(t)

	// Schedules page: acme is scheduled; both system jobs are listed.
	resp, page := apiDo(t, "GET", srv.URL+"/api/v1/schedules", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("schedules: %d", resp.StatusCode)
	}
	scheduled := page["scheduled"].([]any)
	system := page["system"].([]any)
	if len(scheduled) != 1 || len(system) != 2 || page["load"] == nil {
		t.Fatalf("page = %v", page)
	}
	row := scheduled[0].(map[string]any)
	if _, ok := row["slots"].([]any); !ok || row["provider"] != "acme" {
		t.Errorf("schedule row = %v", row)
	}

	// Toggle a schedule, re-time and toggle a system job.
	id := app.schedules.List()[0].ID
	if resp, _ = apiDo(t, "POST", srv.URL+"/api/v1/schedules/"+id+"/toggle", "", nil); resp.StatusCode != 204 || app.schedules.List()[0].Enabled {
		t.Errorf("toggle schedule: %d", resp.StatusCode)
	}
	if resp, _ = apiDo(t, "PUT", srv.URL+"/api/v1/system-jobs/recount/time", `{"minute":120}`, nil); resp.StatusCode != 204 || app.system.Get(sysRecount).Minute != 120 {
		t.Errorf("re-time: %d", resp.StatusCode)
	}
	if resp, _ = apiDo(t, "PUT", srv.URL+"/api/v1/system-jobs/recount/time", `{"minute":7}`, nil); resp.StatusCode != 422 {
		t.Errorf("an off-grid time must be 422, got %d", resp.StatusCode)
	}
	if resp, _ = apiDo(t, "POST", srv.URL+"/api/v1/system-jobs/cleanup/toggle", "", nil); resp.StatusCode != 204 || app.system.Get(sysCleanup).Enabled {
		t.Errorf("toggle system job: %d", resp.StatusCode)
	}
	if resp, _ = apiDo(t, "POST", srv.URL+"/api/v1/system-jobs/nope/toggle", "", nil); resp.StatusCode != 404 {
		t.Errorf("an unknown system job must be 404, got %d", resp.StatusCode)
	}

	// A crawl shows up in Activity as a job with its step; its run's log reads.
	run := app.activity.Start("ingest", "acme", app.activity.NewJob())
	app.activity.AppendOutput(run, []byte("ingest done: ingested=4 failed=0\n"), true)
	app.activity.Finish(run, nil)
	resp, act := apiDo(t, "GET", srv.URL+"/api/v1/activity", "", nil)
	jobs := act["jobs"].([]any)
	if resp.StatusCode != 200 || len(jobs) != 1 || act["cleanup"] == nil {
		t.Fatalf("activity = %v", act)
	}
	job := jobs[0].(map[string]any)
	if job["kind"] != "Crawl" || job["status"] != "success" || len(job["steps"].([]any)) != 1 {
		t.Errorf("job = %v", job)
	}
	resp, detail := apiDo(t, "GET", srv.URL+"/api/v1/runs/"+strconv.Itoa(run.ID), "", nil)
	if resp.StatusCode != 200 || !strings.Contains(detail["stderr"].(string), "ingested=4") || detail["summary"] != "4 jobs" {
		t.Errorf("run = %d %v", resp.StatusCode, detail)
	}
	if resp, _ = apiDo(t, "GET", srv.URL+"/api/v1/runs/999999", "", nil); resp.StatusCode != 404 {
		t.Errorf("an unknown run must be 404, got %d", resp.StatusCode)
	}
	// Explain is refused cleanly without a model configured.
	if resp, _ = apiDo(t, "POST", srv.URL+"/api/v1/runs/"+strconv.Itoa(run.ID)+"/explain", "", nil); resp.StatusCode != 503 {
		t.Errorf("explain without OPENAI_API_KEY must be 503, got %d", resp.StatusCode)
	}
}
