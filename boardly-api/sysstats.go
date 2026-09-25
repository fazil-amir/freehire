package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The sidebar's Server card: disk, memory and CPU of the machine
// boardly-api runs on. Inside Docker (no lxcfs) /proc/meminfo and
// /proc/stat describe the HOST, not the container, which is what an
// operator watching a VPS wants; the disk is measured on a path that lives
// on the host's root filesystem. Every figure is best-effort: one that
// cannot be read is left out, never guessed.

// memInfo is total and used memory in bytes; used counts what the kernel
// cannot hand out (MemTotal - MemAvailable), not page cache.
type memInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// parseMeminfo reads /proc/meminfo's MemTotal and MemAvailable (kB).
func parseMeminfo(r io.Reader) (memInfo, bool) {
	var total, avail uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total, haveTotal = n*1024, true
		case "MemAvailable:":
			avail, haveAvail = n*1024, true
		}
	}
	if !haveTotal || !haveAvail || avail > total {
		return memInfo{}, false
	}
	return memInfo{Total: total, Used: total - avail}, true
}

// cpuTimes is the aggregate "cpu" line of /proc/stat: busy and total
// jiffies since boot, and how many cores the machine has.
type cpuTimes struct {
	Busy, Total uint64
	Cores       int
}

func parseProcStat(r io.Reader) (cpuTimes, bool) {
	var t cpuTimes
	found := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu":
			// user nice system idle iowait irq softirq steal ...
			for i, s := range f[1:] {
				n, err := strconv.ParseUint(s, 10, 64)
				if err != nil {
					return cpuTimes{}, false
				}
				if i >= 8 { // guest and guest_nice are already inside user/nice
					break
				}
				t.Total += n
				if i != 3 && i != 4 { // idle and iowait are not busy
					t.Busy += n
				}
			}
			found = true
		case strings.HasPrefix(f[0], "cpu"):
			t.Cores++
		}
	}
	return t, found
}

// cpuPercent is the busy share between two samples, 0-100.
func cpuPercent(prev, cur cpuTimes) float64 {
	if cur.Total <= prev.Total || cur.Busy < prev.Busy {
		return 0
	}
	return 100 * float64(cur.Busy-prev.Busy) / float64(cur.Total-prev.Total)
}

// CPUSampler keeps the latest CPU usage, measured over its tick — a
// percentage needs two readings, and sampling on a clock keeps the value
// independent of how often (or by how many tabs) the card is polled.
type CPUSampler struct {
	mu    sync.Mutex
	prev  cpuTimes
	pct   float64
	cores int
	ok    bool
}

func (c *CPUSampler) sample() {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	cur, ok := parseProcStat(f)
	f.Close()
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prev.Total > 0 {
		c.pct, c.ok = cpuPercent(c.prev, cur), true
	}
	c.prev, c.cores = cur, cur.Cores
}

// Run samples every tick until stop closes — call it in its own goroutine.
func (c *CPUSampler) Run(tick time.Duration, stop <-chan struct{}) {
	c.sample()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.sample()
		}
	}
}

// Latest is the last measured usage; ok is false until two samples exist
// (or when /proc/stat cannot be read at all, e.g. on a developer's Mac).
func (c *CPUSampler) Latest() (pct float64, cores int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pct, c.cores, c.ok
}

// diskInfo is the filesystem's size, and what is used and free for an
// unprivileged writer, in bytes.
type diskInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

// diskUsage measures the filesystem holding path. The uint64 conversions
// keep it compiling where Statfs's field types differ (macOS, for tests).
func diskUsage(path string) (diskInfo, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return diskInfo{}, false
	}
	bs := uint64(st.Bsize)
	total, free := uint64(st.Blocks)*bs, uint64(st.Bavail)*bs
	used := total - uint64(st.Bfree)*bs
	return diskInfo{Total: total, Used: used, Free: free}, true
}

// statsDiskPath is where the Disk meter measures: the Meilisearch volume
// the reindex disk guard also checks (MEILI_DATA_DIR), else the data dir —
// both on the host's root filesystem.
func statsDiskPath(dataDir string) string {
	if p := os.Getenv("MEILI_DATA_DIR"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return dataDir
}

// reindexFloorGB is REINDEX_MIN_FREE_GB as boardly-api's reindex runs
// will see it: the free space below which a rebuild refuses. 0 when unset.
func reindexFloorGB() int {
	n, err := strconv.Atoi(os.Getenv("REINDEX_MIN_FREE_GB"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// humanBytes renders a size the way the sidebar does: "14.8 GB", "512 MB".
func humanBytes(n uint64) string {
	const gb, mb = 1 << 30, 1 << 20
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.0f MB", float64(n)/mb)
	default:
		return fmt.Sprintf("%d KB", n/1024)
	}
}
