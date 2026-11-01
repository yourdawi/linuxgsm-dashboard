package backend

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListingAnswersWhileDiscoveryHangs(t *testing.T) {
	rt := &slowDiscoverRuntime{blocked: make(chan struct{}, 8), release: make(chan struct{})}
	im := NewInstanceManager(t.TempDir(), rt)
	im.scanStateMu.Lock()
	im.lastScan = time.Time{}
	im.scanStateMu.Unlock()

	for i := 0; i < 2; i++ {
		start := time.Now()
		if len(im.GetInstances()) != 1 {
			t.Fatal("last result missing")
		}
		if d := time.Since(start); d > 2*scanMaxWait+procStatsMaxWait {
			t.Errorf("listing %d took %v", i, d)
		}
	}
	if n := rt.calls.Load(); n != 2 {
		t.Errorf("discoveries = %d, want one besides the first", n)
	}
	close(rt.release)

	// After the hang the next listing starts a discovery again.
	deadline := time.Now().Add(5 * time.Second)
	for {
		im.scanStateMu.Lock()
		running := im.scanDone != nil
		im.scanStateMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("discovery did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcStatsAnswersWhileMeasurementHangs(t *testing.T) {
	c := &procStatsCache{}
	c.update("a", []procSample{{pid: 1, rssKB: 1024 * 1024}}, time.Now().Add(-time.Minute))

	release := make(chan struct{})
	var collects atomic.Int32
	collect := func() []procSample {
		collects.Add(1)
		<-release
		return []procSample{{pid: 1, rssKB: 2 * 1024 * 1024}}
	}
	for i := 0; i < 2; i++ {
		start := time.Now()
		_, ram, pids := c.measure("a", collect)
		if ram != 1 || len(pids) != 1 {
			t.Fatalf("last values missing: %v %v", ram, pids)
		}
		if d := time.Since(start); d > procStatsMaxWait+time.Second {
			t.Errorf("measure took %v", d)
		}
	}
	if n := collects.Load(); n != 1 {
		t.Errorf("measurements = %d, want one", n)
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ram, _, ok := c.recent("a", time.Now()); ok && ram == 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the finished measurement was not stored")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processGone also accepts a zombie: the test container may have no init that reaps orphans.
func processGone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	s := string(data)
	return strings.HasPrefix(strings.TrimSpace(s[strings.LastIndexByte(s, ')')+1:]), "Z")
}

func TestTimeoutEndsTheChildrenOfACommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if err := runHostCmd(killTreeOnCancel(exec.CommandContext(ctx, "sh", "-c", "sleep 300 & echo $!; wait")), ExecIO{Stdout: &out}); err == nil {
		t.Error("timeout not reported")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d still runs", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The login path also runs commands that must survive a closed browser.
func TestTimeoutKeepsChildrenWithoutTreeKill(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	_ = runHostCmd(exec.CommandContext(ctx, "sh", "-c", "sleep 3 & echo $!; wait"), ExecIO{Stdout: &out})
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if processGone(pid) {
		t.Error("child was killed")
	}
}
