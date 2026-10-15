package backend

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DiskUsage describes the storage used by one game server.
type DiskUsage struct {
	Used     int64     `json:"used"`               // bytes allocated by the server directory (incl. linked serverfiles/backups)
	Backups  int64     `json:"backups"`            // part of Used that belongs to backup directories
	Files    int64     `json:"files"`              // number of entries walked
	FSTotal  int64     `json:"fs_total,omitempty"` // size of the filesystem holding the server directory
	FSFree   int64     `json:"fs_free,omitempty"`  // free space on that filesystem
	FSMount  string    `json:"fs_path,omitempty"`  // directory the filesystem figures refer to
	Shared   bool      `json:"shared,omitempty"`   // directory is shared with other instances
	Updated  time.Time `json:"updated,omitempty"`  // zero until the first measurement finished
	Scanning bool      `json:"scanning,omitempty"` // a measurement is running right now
	Error    string    `json:"error,omitempty"`    // last measurement failed or was incomplete
}

const (
	diskScanInterval = 10 * time.Minute
	diskScanTimeout  = 20 * time.Minute
	diskScanWorkers  = 2
)

var errDiskScanTimeout = errors.New("measurement took too long and was stopped")

// startDiskLoop measures the disk usage of all servers in the background and refreshes it
// periodically. New server directories are additionally measured as soon as they are discovered
// (see measureMissingDisks).
func (im *InstanceManager) startDiskLoop() {
	go func() {
		defer logPanic("disk usage loop")
		time.Sleep(2 * time.Second)
		for {
			im.scanDiskUsage(false)
			time.Sleep(diskScanInterval)
		}
	}()
}

// logPanic keeps a bug in a background task from taking down the whole dashboard.
func logPanic(task string) {
	if r := recover(); r != nil {
		fmt.Printf("[ERROR] %s crashed: %v\n", task, r)
	}
}

type diskJob struct {
	dir    string
	srv    GameServerInstance
	shared bool
}

func (im *InstanceManager) diskJobs(onlyMissing bool) []diskJob {
	im.mu.Lock()
	servers := make([]GameServerInstance, 0, len(im.instances))
	for _, s := range im.instances {
		servers = append(servers, *s)
	}
	im.mu.Unlock()
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })

	byDir := map[string][]GameServerInstance{}
	var order []string
	for _, s := range servers {
		if _, ok := byDir[s.Dir]; !ok {
			order = append(order, s.Dir)
		}
		byDir[s.Dir] = append(byDir[s.Dir], s)
	}

	im.diskMu.Lock()
	defer im.diskMu.Unlock()
	if im.diskCache == nil {
		im.diskCache = make(map[string]DiskUsage)
	}
	if im.diskInflight == nil {
		im.diskInflight = make(map[string]bool)
	}
	var jobs []diskJob
	for _, dir := range order {
		if im.diskInflight[dir] {
			continue
		}
		prev, known := im.diskCache[dir]
		if onlyMissing && known {
			continue
		}
		im.diskInflight[dir] = true
		prev.Scanning = true
		im.diskCache[dir] = prev
		jobs = append(jobs, diskJob{dir: dir, srv: byDir[dir][0], shared: len(byDir[dir]) > 1})
	}
	return jobs
}

// scanDiskUsage measures server directories with a small worker pool so one huge installation
// does not delay all others.
func (im *InstanceManager) scanDiskUsage(onlyMissing bool) {
	jobs := im.diskJobs(onlyMissing)
	if len(jobs) == 0 {
		return
	}
	queue := make(chan diskJob)
	var wg sync.WaitGroup
	for w := 0; w < diskScanWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				im.runDiskJob(job)
			}
		}()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()
}

func (im *InstanceManager) runDiskJob(job diskJob) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[ERROR] disk usage of %s crashed: %v\n", job.dir, r)
			im.storeDisk(job.dir, DiskUsage{Error: "measurement failed", Updated: time.Now().UTC()})
		}
	}()
	start := time.Now()
	usage := MeasureServerDisk(job.srv, im.getBackupDirsForServer(job.srv), time.Now().Add(diskScanTimeout))
	usage.Shared = job.shared
	if usage.Error != "" {
		fmt.Printf("[DISK] %s: %s after %s (%d entries, %s counted so far)\n", job.dir, usage.Error, time.Since(start).Round(time.Second), usage.Files, formatBytesLog(usage.Used))
	} else {
		fmt.Printf("[DISK] %s: %s in %d entries, measured in %s\n", job.dir, formatBytesLog(usage.Used), usage.Files, time.Since(start).Round(time.Millisecond))
	}
	im.storeDisk(job.dir, usage)
}

func (im *InstanceManager) storeDisk(dir string, usage DiskUsage) {
	im.diskMu.Lock()
	defer im.diskMu.Unlock()
	if im.diskCache == nil {
		im.diskCache = make(map[string]DiskUsage)
	}
	usage.Scanning = false
	im.diskCache[dir] = usage
	delete(im.diskInflight, dir)
}

// measureMissingDisks starts measuring directories that have never been measured, e.g. servers
// discovered after startup. It returns immediately.
func (im *InstanceManager) measureMissingDisks(dirs []string) {
	if im.static {
		return
	}
	im.diskMu.Lock()
	missing := false
	for _, d := range dirs {
		if _, ok := im.diskCache[d]; !ok && !im.diskInflight[d] {
			missing = true
			break
		}
	}
	im.diskMu.Unlock()
	if missing {
		go func() {
			defer logPanic("disk usage")
			im.scanDiskUsage(true)
		}()
	}
}

func formatBytesLog(b int64) string {
	const gb = 1 << 30
	const mb = 1 << 20
	if b >= gb {
		return fmt.Sprintf("%.1f GiB", float64(b)/gb)
	}
	return fmt.Sprintf("%.1f MiB", float64(b)/mb)
}

func (im *InstanceManager) cachedDisk(dir string) *DiskUsage {
	im.diskMu.Lock()
	defer im.diskMu.Unlock()
	if u, ok := im.diskCache[dir]; ok {
		return &u
	}
	return nil
}

// MeasureServerDisk sums the allocated size of the server directory. Symlinks are not followed,
// except for serverfiles and backup directories that LinuxGSM setups often move to another disk;
// those are included when they belong to the server user (see ServerSubRoot / isAllowedBackupDir).
func MeasureServerDisk(srv GameServerInstance, backupDirs []string, deadline time.Time) DiskUsage {
	seen := map[fileID]bool{}
	visitedRoots := map[string]bool{}
	usage := DiskUsage{FSMount: srv.Dir}

	walk := func(root string) int64 {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return 0
		}
		for v := range visitedRoots {
			if IsWithin(v, resolved) {
				return 0 // already counted as part of another root
			}
		}
		visitedRoots[resolved] = true
		size, files, err := walkAllocated(resolved, seen, deadline)
		usage.Files += files
		if err != nil && usage.Error == "" {
			usage.Error = err.Error()
		}
		return size
	}

	usage.Used = walk(srv.Dir)

	// serverfiles moved to another disk via symlink
	if root, err := ServerSubRoot(srv.Dir, "serverfiles", srv.UID); err == nil {
		usage.Used += walk(root)
		usage.FSMount = root
	}
	for _, d := range backupDirs {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			size := walkBackupDir(d)
			usage.Backups += size
			if resolved, err := filepath.EvalSymlinks(d); err == nil && !isUnderAny(visitedRoots, resolved) {
				usage.Used += walk(d)
			}
		}
	}

	usage.FSTotal, usage.FSFree = filesystemStats(usage.FSMount)
	usage.Updated = time.Now().UTC()
	return usage
}

func isUnderAny(roots map[string]bool, p string) bool {
	for r := range roots {
		if IsWithin(r, p) {
			return true
		}
	}
	return false
}

// walkBackupDir sums the backup archives in a directory (counted for the "backups" figure only).
func walkBackupDir(dir string) int64 {
	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += allocatedSize(info)
		}
	}
	return total
}

// walkAllocated sums the allocated size of all entries below root without following symlinks.
// Hard links are counted once. The walk stops at the deadline.
func walkAllocated(root string, seen map[fileID]bool, deadline time.Time) (int64, int64, error) {
	var total, files int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		files++
		if files%4096 == 0 && !deadline.IsZero() && time.Now().After(deadline) {
			return errDiskScanTimeout
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if id, ok := hardlinkID(info); ok {
			if seen[id] {
				return nil
			}
			seen[id] = true
		}
		total += allocatedSize(info)
		return nil
	})
	return total, files, err
}
