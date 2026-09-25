package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DockerProxy reaches the host's Docker Engine through the compose file's
// docker-socket-proxy, which only lets through the two calls used here —
// reading build-cache usage and pruning it — so boardly-api never holds
// the raw socket (full control of the host). DOCKER_PROXY_URL unset leaves
// it disabled and the Server card says so.
type DockerProxy struct {
	baseURL string
	client  *http.Client

	mu       sync.Mutex
	cached   buildCache
	cachedAt time.Time
	pruning  sync.Mutex // one prune at a time
}

// buildCache is Docker's build cache: its total size and how much of it no
// build is using right now — what a prune frees.
type buildCache struct {
	Total       uint64 `json:"total"`
	Reclaimable uint64 `json:"reclaimable"`
}

// buildCacheTTL: `docker system df` walks the cache, so the card's 5s poll
// reads a cached answer. A prune refreshes it at once.
const buildCacheTTL = time.Minute

func NewDockerProxyFromEnv() *DockerProxy {
	return &DockerProxy{
		baseURL: strings.TrimRight(os.Getenv("DOCKER_PROXY_URL"), "/"),
		client:  &http.Client{Timeout: 10 * time.Minute}, // a big prune takes a while
	}
}

func (d *DockerProxy) Enabled() bool { return d.baseURL != "" }

// BuildCache returns the build cache's size, cached for buildCacheTTL.
func (d *DockerProxy) BuildCache(ctx context.Context) (buildCache, error) {
	d.mu.Lock()
	if !d.cachedAt.IsZero() && time.Since(d.cachedAt) < buildCacheTTL {
		c := d.cached
		d.mu.Unlock()
		return c, nil
	}
	d.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body, err := d.do(ctx, http.MethodGet, "/system/df?type=build-cache")
	if err != nil {
		return buildCache{}, err
	}
	c, err := parseBuildCache(body)
	if err != nil {
		return buildCache{}, err
	}
	d.mu.Lock()
	d.cached, d.cachedAt = c, time.Now()
	d.mu.Unlock()
	return c, nil
}

// parseBuildCache sums /system/df's BuildCache records: every record's
// size, and separately those not in use.
func parseBuildCache(body []byte) (buildCache, error) {
	var df struct {
		BuildCache []struct {
			Size  int64
			InUse bool
		}
	}
	if err := json.Unmarshal(body, &df); err != nil {
		return buildCache{}, fmt.Errorf("read docker disk usage: %w", err)
	}
	var c buildCache
	for _, r := range df.BuildCache {
		if r.Size <= 0 {
			continue
		}
		c.Total += uint64(r.Size)
		if !r.InUse {
			c.Reclaimable += uint64(r.Size)
		}
	}
	return c, nil
}

// errPruneRunning: a prune is already in progress.
var errPruneRunning = fmt.Errorf("the build cache is already being cleared")

// PruneBuildCache is `docker builder prune -af`: every build cache record
// not in use, and reports how many bytes it freed.
func (d *DockerProxy) PruneBuildCache(ctx context.Context) (uint64, error) {
	if !d.pruning.TryLock() {
		return 0, errPruneRunning
	}
	defer d.pruning.Unlock()
	body, err := d.do(ctx, http.MethodPost, "/build/prune?all=1")
	if err != nil {
		return 0, err
	}
	var res struct{ SpaceReclaimed int64 }
	if err := json.Unmarshal(body, &res); err != nil {
		return 0, fmt.Errorf("read prune result: %w", err)
	}
	d.mu.Lock()
	d.cachedAt = time.Time{} // re-measure on the next read
	d.mu.Unlock()
	return uint64(max(res.SpaceReclaimed, 0)), nil
}

func (d *DockerProxy) do(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker proxy unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("docker %s %s: %s %s", method, path, resp.Status, msg)
	}
	return body, nil
}
