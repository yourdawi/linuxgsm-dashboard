package backend

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func statLine(pid, ppid int, name string, utime, stime, rssPages int) string {
	return fmt.Sprintf("%d (%s) S %d 1 1 0 -1 4194304 10 0 0 0 %d %d 0 0 20 0 1 0 100 1000 %d 18446744073709551615 0 0 0 0 0 0 0",
		pid, name, ppid, utime, stime, rssPages)
}

func TestParseProcStat(t *testing.T) {
	s, ok := parseProcStat(statLine(12, 3, "srcds (main) x", 150, 50, 4))
	if !ok {
		t.Fatal("line not parsed")
	}
	want := procSample{pid: 12, ppid: 3, ticks: 200, rssKB: uint64(4 * os.Getpagesize() / 1024)}
	if s != want {
		t.Fatalf("got %+v, want %+v", s, want)
	}
	for _, bad := range []string{"", "12 (x) S 1", "x " + statLine(1, 1, "a", 1, 1, 1)} {
		if _, ok := parseProcStat(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestProcessTreeHasOnlyTheDescendants(t *testing.T) {
	all := []procSample{
		{pid: 1, ppid: 0}, {pid: 10, ppid: 1}, {pid: 11, ppid: 10}, {pid: 12, ppid: 11},
		{pid: 20, ppid: 1}, {pid: 21, ppid: 20},
	}
	var got []int
	for _, s := range processTree(all, []int{10, 99}) {
		got = append(got, s.pid)
	}
	if !reflect.DeepEqual(got, []int{10, 11, 12}) {
		t.Fatalf("tree = %v", got)
	}
}

func TestProcStatsCPUIsTheUseSinceTheLastMeasurement(t *testing.T) {
	c := &procStatsCache{}
	start := time.Unix(1000, 0)

	// A process that used a lot of CPU long ago shows no usage in the first measurement.
	cpu, ram, pids := c.update("a", []procSample{{pid: 1, ticks: 100000, rssKB: 1024 * 1024}}, start)
	if cpu != 0 || ram != 1 || !reflect.DeepEqual(pids, []int{1}) {
		t.Fatalf("first: %v %v %v", cpu, ram, pids)
	}

	// Half a core over 10 s is 500 ticks; a new process adds its whole 100 ticks.
	cpu, _, _ = c.update("a", []procSample{{pid: 1, ticks: 100500}, {pid: 2, ticks: 100}}, start.Add(10*time.Second))
	if cpu != 60 {
		t.Fatalf("second: cpu = %v, want 60", cpu)
	}

	// Measured again right away: the last value stands, the short interval says nothing.
	cpu, _, _ = c.update("a", []procSample{{pid: 1, ticks: 200000}}, start.Add(10*time.Second+time.Millisecond))
	if cpu != 60 {
		t.Fatalf("too soon: cpu = %v", cpu)
	}
	if cached, _, _, ok := c.recent("a", start.Add(11*time.Second)); !ok || cached != 60 {
		t.Fatalf("recent: %v %v", cached, ok)
	}
	if _, _, _, ok := c.recent("a", start.Add(13*time.Second)); ok {
		t.Fatal("old measurement reused")
	}

	// Another server keeps its own numbers, and a stopped server is forgotten.
	if cpu, _, _ = c.update("b", []procSample{{pid: 3, ticks: 5}}, start); cpu != 0 {
		t.Fatalf("other server: cpu = %v", cpu)
	}
	if cpu, ram, pids = c.update("a", nil, start.Add(20*time.Second)); cpu != 0 || ram != 0 || pids != nil {
		t.Fatalf("stopped: %v %v %v", cpu, ram, pids)
	}
	if _, ok := c.entries["a"]; ok {
		t.Fatal("stopped server kept")
	}
}

// slowDiscoverRuntime blocks discovery until released and counts the calls.
type slowDiscoverRuntime struct {
	fakeRuntime
	calls   atomic.Int32
	blocked chan struct{}
	release chan struct{}
}

func (s *slowDiscoverRuntime) Discover([]string) ([]*GameServerInstance, error) {
	if s.calls.Add(1) > 1 && s.release != nil {
		s.blocked <- struct{}{}
		<-s.release
	}
	return []*GameServerInstance{{ID: "a", Script: "vhserver", Dir: "/srv/a", User: "a", Status: "running", ParsedPorts: []PortProbe{{Port: 1, Protocol: "udp"}}}}, nil
}

func TestDiscoveryDoesNotBlockOtherRequests(t *testing.T) {
	rt := &slowDiscoverRuntime{blocked: make(chan struct{}), release: make(chan struct{})}
	im := NewInstanceManager(t.TempDir(), rt)

	done := make(chan struct{})
	go func() {
		im.ScanInstances()
		close(done)
	}()
	<-rt.blocked

	finished := make(chan bool, 1)
	go func() {
		_, ok := im.getServer("a")
		finished <- ok
	}()
	select {
	case ok := <-finished:
		if !ok {
			t.Error("server not found")
		}
	case <-time.After(5 * time.Second):
		t.Error("getServer waited for the discovery")
	}
	close(rt.release)
	<-done
}

func TestListingsShareOneDiscovery(t *testing.T) {
	rt := &slowDiscoverRuntime{}
	im := NewInstanceManager(t.TempDir(), rt)
	if n := rt.calls.Load(); n != 1 {
		t.Fatalf("discoveries after start = %d", n)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if len(im.GetInstances()) != 1 {
				t.Error("server missing")
			}
		}()
	}
	wg.Wait()
	if n := rt.calls.Load(); n != 1 {
		t.Fatalf("listings ran %d discoveries, want the one from the start", n)
	}

	im.ScanInstances()
	if n := rt.calls.Load(); n != 2 {
		t.Fatalf("an explicit scan must always discover, calls = %d", n)
	}
}

// A timeout must end a command whose child keeps the output pipe, like a hanging tmux client below
// runuser. Without it, one hanging tmux would stop every server listing.
func TestHostCommandTimeoutWithChildHoldingThePipe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	var out bytes.Buffer
	err := runHostCmd(exec.CommandContext(ctx, "sh", "-c", "sleep 30 & wait"), ExecIO{Stdout: &out})
	if err == nil {
		t.Error("timeout not reported")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("returned after %v", d)
	}

	// A command that succeeded stays a success when it leaves a background process behind.
	start = time.Now()
	err = runHostCmd(exec.CommandContext(context.Background(), "sh", "-c", "echo done; sleep 30 &"), ExecIO{Stdout: &out})
	if err != nil {
		t.Errorf("background process turned success into %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("returned after %v", d)
	}
	if !strings.Contains(out.String(), "done") {
		t.Errorf("output lost: %q", out.String())
	}
}
