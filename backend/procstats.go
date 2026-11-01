package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// procStatsMaxAge is how long a measurement is reused. CPU usage is the difference between two
	// measurements, which says little over a shorter time.
	procStatsMaxAge = 2 * time.Second
	// procStatsMaxWait is how long a listing waits for a measurement before it shows the last one.
	procStatsMaxWait = time.Second
	// procStatsParallel limits the measurements running at the same time.
	procStatsParallel = 8
	// userHZ is the unit of utime and stime in /proc/<pid>/stat. Linux fixes it at 100.
	userHZ = 100
)

// procSample is one line of /proc/<pid>/stat.
type procSample struct {
	pid   int
	ppid  int
	ticks uint64 // utime + stime
	rssKB uint64
}

// parseProcStat reads a line of /proc/<pid>/stat. The process name in parentheses can hold spaces
// and parentheses, so the other fields are counted from the last closing parenthesis.
func parseProcStat(line string) (procSample, bool) {
	open, end := strings.IndexByte(line, '('), strings.LastIndexByte(line, ')')
	if open <= 0 || end < open {
		return procSample{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:open]))
	if err != nil {
		return procSample{}, false
	}
	// fields[0] is field 3 of proc(5): state.
	fields := strings.Fields(line[end+1:])
	if len(fields) < 22 {
		return procSample{}, false
	}
	ppid, err1 := strconv.Atoi(fields[1])
	utime, err2 := strconv.ParseUint(fields[11], 10, 64)
	stime, err3 := strconv.ParseUint(fields[12], 10, 64)
	rssPages, err4 := strconv.ParseInt(fields[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || rssPages < 0 {
		return procSample{}, false
	}
	return procSample{
		pid:   pid,
		ppid:  ppid,
		ticks: utime + stime,
		rssKB: uint64(rssPages) * uint64(os.Getpagesize()) / 1024,
	}, true
}

// parseProcStats reads several concatenated /proc/<pid>/stat files.
func parseProcStats(out string) []procSample {
	var samples []procSample
	for _, line := range strings.Split(out, "\n") {
		if s, ok := parseProcStat(line); ok {
			samples = append(samples, s)
		}
	}
	return samples
}

// readHostProcesses reads every process of the machine from /proc.
func readHostProcesses() []procSample {
	paths, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return nil
	}
	samples := make([]procSample, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p) // the process may be gone by now
		if err != nil {
			continue
		}
		if s, ok := parseProcStat(strings.TrimSpace(string(data))); ok {
			samples = append(samples, s)
		}
	}
	return samples
}

// processTree returns the processes in roots and all their descendants.
func processTree(all []procSample, roots []int) []procSample {
	children := make(map[int][]procSample, len(all))
	byPID := make(map[int]procSample, len(all))
	for _, s := range all {
		children[s.ppid] = append(children[s.ppid], s)
		byPID[s.pid] = s
	}
	var tree []procSample
	seen := make(map[int]bool, len(roots))
	queue := make([]procSample, 0, len(roots))
	for _, pid := range roots {
		if s, ok := byPID[pid]; ok && !seen[pid] {
			seen[pid] = true
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		tree = append(tree, s)
		for _, c := range children[s.pid] {
			if !seen[c.pid] {
				seen[c.pid] = true
				queue = append(queue, c)
			}
		}
	}
	return tree
}

// tmuxPanePIDs returns the process IDs started in the LinuxGSM tmux session of a server. known is
// false when tmux did not answer in time.
func tmuxPanePIDs(rt Runtime, srv GameServerInstance) (pids []int, known bool) {
	known = true
	for _, socket := range linuxGSMTmuxSocketNames(srv) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var out bytes.Buffer
		err := rt.RunAsUser(ctx, srv, ExecIO{Stdout: &out}, false, "tmux", "-L", socket, "list-panes", "-s", "-t", "="+srv.Script, "-F", "#{pane_pid}")
		timedOut := ctx.Err() != nil
		cancel()
		if err != nil {
			known = known && !timedOut
			continue
		}
		pids = nil
		for _, f := range strings.Fields(out.String()) {
			if pid, err := strconv.Atoi(f); err == nil && pid > 0 {
				pids = append(pids, pid)
			}
		}
		if len(pids) > 0 {
			return pids, true
		}
	}
	return nil, known
}

// procStatsCache turns process samples into CPU usage: the CPU time used since the previous
// measurement of the same server, divided by the time passed. 100 percent is one full core.
type procStatsCache struct {
	mu       sync.Mutex
	entries  map[string]*procStatsEntry
	inflight map[string]chan struct{}
}

type procStatsEntry struct {
	at    time.Time
	ticks map[int]uint64
	cpu   float64
	ram   float64
	pids  []int
}

var (
	procStats      = &procStatsCache{}
	procStatsSlots = make(chan struct{}, procStatsParallel)
)

// measure returns the usage of a server, reusing a recent measurement. collect runs in the
// background and can take seconds when a tmux hangs: after procStatsMaxWait the last values are
// returned, and the running measurement is used by a later call. collect returns false when it
// could not find out: the last values then stay.
func (c *procStatsCache) measure(serverID string, collect func() ([]procSample, bool)) (float64, float64, []int) {
	if cpu, ram, pids, ok := c.recent(serverID, time.Now()); ok {
		return cpu, ram, pids
	}
	c.mu.Lock()
	done := c.inflight[serverID]
	if done == nil {
		done = make(chan struct{})
		if c.inflight == nil {
			c.inflight = make(map[string]chan struct{})
		}
		c.inflight[serverID] = done
		go func() {
			procStatsSlots <- struct{}{}
			samples, known := collect()
			<-procStatsSlots
			if known {
				c.update(serverID, samples, time.Now())
			}
			c.mu.Lock()
			delete(c.inflight, serverID)
			c.mu.Unlock()
			close(done)
		}()
	}
	c.mu.Unlock()

	timer := time.NewTimer(procStatsMaxWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[serverID]; ok {
		return e.cpu, e.ram, e.pids
	}
	return 0, 0, nil
}

func (c *procStatsCache) recent(serverID string, now time.Time) (float64, float64, []int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[serverID]; ok && now.Sub(e.at) < procStatsMaxAge {
		return e.cpu, e.ram, e.pids, true
	}
	return 0, 0, nil, false
}

func (c *procStatsCache) update(serverID string, samples []procSample, now time.Time) (float64, float64, []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, hasPrev := c.entries[serverID]
	if hasPrev && now.Sub(prev.at) < procStatsMaxAge {
		return prev.cpu, prev.ram, prev.pids // measured by a parallel request in the meantime
	}
	if len(samples) == 0 {
		delete(c.entries, serverID)
		return 0, 0, nil
	}

	e := &procStatsEntry{at: now, ticks: make(map[int]uint64, len(samples)), pids: make([]int, 0, len(samples))}
	var used, rssKB uint64
	for _, s := range samples {
		e.ticks[s.pid] = s.ticks
		e.pids = append(e.pids, s.pid)
		rssKB += s.rssKB
		if !hasPrev {
			continue
		}
		// A process not seen before, or a reused PID, started after the previous measurement:
		// all of its CPU time belongs to this one.
		if old, ok := prev.ticks[s.pid]; ok && s.ticks >= old {
			used += s.ticks - old
		} else {
			used += s.ticks
		}
	}
	if hasPrev {
		e.cpu = float64(used) / userHZ / now.Sub(prev.at).Seconds() * 100
	}
	e.ram = float64(rssKB) / 1024 / 1024
	if c.entries == nil {
		c.entries = make(map[string]*procStatsEntry)
	}
	c.entries[serverID] = e
	return e.cpu, e.ram, e.pids
}
