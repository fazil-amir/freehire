package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// systemStats is what the sidebar's Server card polls. A figure that could
// not be measured is null, and the card hides that meter rather than
// showing a zero that looks like a reading.
type systemStats struct {
	Disk *diskInfo `json:"disk"`
	Mem  *memInfo  `json:"mem"`
	CPU  *struct {
		Pct   float64 `json:"pct"`
		Cores int     `json:"cores"`
	} `json:"cpu"`
	ReindexFloorGB int         `json:"reindexFloorGB"`
	Running        int         `json:"running"`    // jobs in flight, for the sidebar's Activity pulse
	Docker         bool        `json:"docker"`     // the proxy is configured
	BuildCache     *buildCache `json:"buildCache"` // null when Docker could not be asked
	DockerError    string      `json:"dockerError,omitempty"`
}

func handleSystemStats(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := systemStats{ReindexFloorGB: reindexFloorGB(), Docker: app.docker.Enabled()}
		for _, j := range buildJobs(app.activity.List()) {
			if j.Running() {
				s.Running++
			}
		}
		if d, ok := diskUsage(statsDiskPath(app.dataDir)); ok {
			s.Disk = &d
		}
		if f, err := os.Open("/proc/meminfo"); err == nil {
			if m, ok := parseMeminfo(f); ok {
				s.Mem = &m
			}
			f.Close()
		}
		if pct, cores, ok := app.cpu.Latest(); ok {
			s.CPU = &struct {
				Pct   float64 `json:"pct"`
				Cores int     `json:"cores"`
			}{pct, cores}
		}
		if s.Docker {
			if c, err := app.docker.BuildCache(r.Context()); err == nil {
				s.BuildCache = &c
			} else {
				s.DockerError = err.Error()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(s)
	}
}

// handleBuildCachePrune is "Clear build cache": docker
// builder prune -af through the proxy, recorded as an Activity job like
// every other operation. It answers with the bytes freed. The prune runs
// on its own context: a caller that goes away mid-prune must not
// abort it half-way.
func handleBuildCachePrune(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.docker.Enabled() {
			actionError(w, http.StatusServiceUnavailable, "Docker is not connected (DOCKER_PROXY_URL is unset).")
			return
		}
		run := app.activity.Start("prune-build-cache", "", app.activity.NewJob())
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		freed, err := app.docker.PruneBuildCache(ctx)
		if errors.Is(err, errPruneRunning) {
			app.activity.Finish(run, err)
			actionError(w, http.StatusConflict, "The build cache is already being cleared.")
			return
		}
		if err == nil {
			app.activity.AppendOutput(run, []byte(fmt.Sprintf("prune-build-cache: reclaimed=%d (%s)\n", freed, humanBytes(freed))), true)
		}
		app.activity.Finish(run, err)
		if err != nil {
			log.Printf("prune build cache: %v", err)
			actionError(w, http.StatusBadGateway, "Could not clear the build cache: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"reclaimed": freed, "reclaimedText": humanBytes(freed)})
	}
}
