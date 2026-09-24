package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMeminfo_UsedIsTotalMinusAvailable(t *testing.T) {
	m, ok := parseMeminfo(strings.NewReader("MemTotal:        8000000 kB\nMemFree:          500000 kB\nMemAvailable:    6000000 kB\n"))
	if !ok || m.Total != 8000000*1024 || m.Used != 2000000*1024 {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
	if _, ok := parseMeminfo(strings.NewReader("MemTotal: 8000000 kB\n")); ok {
		t.Error("without MemAvailable there is no reading")
	}
}

func TestCPUPercent_FromTwoSamples(t *testing.T) {
	const stat = "cpu  %d 0 %d %d 0 0 0 0 0 0\ncpu0 1 1 1 1\ncpu1 1 1 1 1\nintr 1\n"
	parse := func(user, system, idle int) cpuTimes {
		c, ok := parseProcStat(strings.NewReader(fmt.Sprintf(stat, user, system, idle)))
		if !ok {
			t.Fatal("parse failed")
		}
		return c
	}
	a, b := parse(100, 100, 800), parse(250, 150, 1000) // +200 busy, +200 idle
	if b.Cores != 2 {
		t.Errorf("cores = %d, want 2", b.Cores)
	}
	if got := cpuPercent(a, b); got != 50 {
		t.Errorf("cpu = %v%%, want 50", got)
	}
	if got := cpuPercent(b, a); got != 0 {
		t.Errorf("a counter going backwards must read 0, got %v", got)
	}
}

func TestParseBuildCache_SplitsInUseFromClearable(t *testing.T) {
	c, err := parseBuildCache([]byte(`{"BuildCache":[{"Size":1000,"InUse":false},{"Size":500,"InUse":true},{"Size":-1,"InUse":false}]}`))
	if err != nil || c.Total != 1500 || c.Reclaimable != 1000 {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[uint64]string{15891378176: "14.8 GB", 512 << 20: "512 MB", 2048: "2 KB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// The prune goes through the proxy, is recorded as an Activity job with a
// readable outcome, and answers with the bytes freed.
func TestBuildCachePrune_RecordsAnActivityJob(t *testing.T) {
	var pruned bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/build/prune" && r.URL.Query().Get("all") == "1":
			pruned = true
			_, _ = w.Write([]byte(`{"CachesDeleted":["a"],"SpaceReclaimed":15891378176}`))
		case r.URL.Path == "/system/df":
			_, _ = w.Write([]byte(`{"BuildCache":[]}`))
		default:
			http.Error(w, "blocked by proxy", http.StatusForbidden)
		}
	}))
	defer proxy.Close()

	_, _, activity := newTestScheduler(t)
	app := &App{activity: activity, docker: &DockerProxy{baseURL: proxy.URL, client: proxy.Client()}, cpu: &CPUSampler{}, dataDir: t.TempDir()}

	rec := httptest.NewRecorder()
	handleBuildCachePrune(app)(rec, httptest.NewRequest(http.MethodPost, "/system/build-cache/prune", nil))
	if rec.Code != http.StatusOK || !pruned {
		t.Fatalf("status %d pruned=%v body=%s", rec.Code, pruned, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"reclaimedText":"14.8 GB"`) {
		t.Errorf("body = %s", rec.Body)
	}
	jobs := buildJobs(activity.List())
	if len(jobs) != 1 || jobs[0].Kind != "Clear build cache" || jobs[0].Summary != "14.8 GB reclaimed" || jobs[0].Status != OutcomeSuccess {
		t.Fatalf("want one successful Clear build cache job, got %+v", jobs[0])
	}

	// Stats with the proxy: the build cache is read through it.
	rec = httptest.NewRecorder()
	handleSystemStats(app)(rec, httptest.NewRequest(http.MethodGet, "/system/stats", nil))
	var s systemStats
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || !s.Docker || s.BuildCache == nil {
		t.Fatalf("stats = %s (%v)", rec.Body, err)
	}
}

func TestSystemStats_WithoutDockerStillAnswers(t *testing.T) {
	app := &App{docker: &DockerProxy{}, cpu: &CPUSampler{}, dataDir: t.TempDir()}
	rec := httptest.NewRecorder()
	handleSystemStats(app)(rec, httptest.NewRequest(http.MethodGet, "/system/stats", nil))
	var s systemStats
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Docker || s.BuildCache != nil || s.Disk == nil || s.Disk.Total == 0 {
		t.Errorf("want disk measured and Docker off, got %s", rec.Body)
	}
}
