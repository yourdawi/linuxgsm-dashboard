package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	collect := func() ([]procSample, bool) {
		collects.Add(1)
		<-release
		return []procSample{{pid: 1, rssKB: 2 * 1024 * 1024}}, true
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

// tmuxRuntime answers tmux calls like a server without a session, one with a session or a hanging
// tmux, which blocks until the timeout.
type tmuxRuntime struct {
	fakeRuntime
	state  tmuxState
	stderr string
}

func (t *tmuxRuntime) RunAsUser(ctx context.Context, srv GameServerInstance, stdio ExecIO, login bool, args ...string) error {
	if len(args) == 0 || args[0] != "tmux" {
		return t.fakeRuntime.RunAsUser(ctx, srv, stdio, login, args...)
	}
	switch t.state {
	case tmuxUnknown:
		<-ctx.Done()
		return errors.New("signal: killed")
	case tmuxActive:
		if stdio.Stdout != nil {
			_, _ = io.WriteString(stdio.Stdout, srv.Script+"\n")
		}
		return nil
	}
	if stdio.Stderr != nil {
		_, _ = io.WriteString(stdio.Stderr, t.stderr)
	}
	return errors.New("exit status 1")
}

func TestTmuxSessionState(t *testing.T) {
	srv := GameServerInstance{Script: "vhserver", Dir: t.TempDir()}
	const noServer = "no server running on /tmp/tmux-1000/vhserver\n"
	for _, c := range []struct {
		state  tmuxState
		stderr string
		want   tmuxState
	}{
		{tmuxAbsent, noServer, tmuxAbsent},
		{tmuxAbsent, "error connecting to /tmp/tmux-1000/vhserver (No such file or directory)\n", tmuxAbsent},
		{tmuxAbsent, "error connecting to /tmp/tmux-1000/vhserver (Connection refused)\n", tmuxAbsent},
		{tmuxAbsent, "", tmuxUnknown}, // killed, or no message at all
		{tmuxAbsent, "server exited unexpectedly\n", tmuxUnknown},
		{tmuxAbsent, "error connecting to /tmp/tmux-1000/vhserver (Permission denied)\n", tmuxUnknown},
		{tmuxActive, "", tmuxActive},
		{tmuxUnknown, "", tmuxUnknown},
	} {
		state := c.want
		rt := &tmuxRuntime{state: c.state, stderr: c.stderr}
		if got := tmuxSessionState(rt, srv); got != state {
			t.Errorf("stderr %q: state = %v, want %v", c.stderr, got, state)
		}
		if got, want := isTmuxSessionActive(rt, srv), state != tmuxAbsent; got != want {
			t.Errorf("state %v: active = %v", state, got)
		}
	}
}

// statusRuntime returns one server whose tmux does not answer.
type statusRuntime struct {
	fakeRuntime
	unknown bool
	status  string
}

func (s *statusRuntime) Discover([]string) ([]*GameServerInstance, error) {
	return []*GameServerInstance{{ID: "a", Script: "vhserver", Dir: "/srv/a", User: "a", Status: s.status,
		statusUnknown: s.unknown, ParsedPorts: []PortProbe{{Port: 1, Protocol: "udp"}}}}, nil
}

func TestHangingTmuxKeepsTheLastStatus(t *testing.T) {
	status := func(im *InstanceManager) string {
		srv, _ := im.getServer("a")
		return srv.Status
	}
	for _, last := range []string{"running", "stopped"} {
		rt := &statusRuntime{status: last}
		im := NewInstanceManager(t.TempDir(), rt)
		if status(im) != last {
			t.Fatalf("first status = %s", status(im))
		}
		// A hanging tmux reports the default of the runtime, which must not count.
		rt.unknown, rt.status = true, "stopped"
		if last == "stopped" {
			rt.status = "running"
		}
		im.ScanInstances()
		if status(im) != last {
			t.Errorf("after a hang: %s, want %s", status(im), last)
		}
		// tmux answers again: its answer counts.
		rt.unknown = false
		im.ScanInstances()
		if status(im) != rt.status {
			t.Errorf("after the hang: %s, want %s", status(im), rt.status)
		}
	}
}

func TestNewServerWithoutAClearAnswerIsNotRunning(t *testing.T) {
	rt := &statusRuntime{status: "running", unknown: true}
	im := NewInstanceManager(t.TempDir(), rt)
	if srv, _ := im.getServer("a"); srv.Status != "stopped" {
		t.Errorf("status = %s", srv.Status)
	}
}

func TestDeleteRefusedWhileTmuxHangs(t *testing.T) {
	rt := &tmuxRuntime{state: tmuxUnknown}
	dir := t.TempDir()
	im := newFakeManager(&rt.fakeRuntime, GameServerInstance{ID: "vhserver", Script: "vhserver", User: "vh", Dir: dir})
	im.rt = rt
	for _, url := range []string{"/", "/?force=1"} {
		rec := httptest.NewRecorder()
		im.DeleteServer(rec, httptest.NewRequest("POST", url, nil), "vhserver")
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: code %d: %s", url, rec.Code, rec.Body.String())
		}
	}
	if len(rt.deleted) != 0 {
		t.Errorf("deleted: %v", rt.deleted)
	}
}

// The process must end inside the container, and only after the context reported the timeout.
func TestDockerCommandWithDeadlineEndsInTheContainer(t *testing.T) {
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	d := newTestDockerRuntime(t, e)
	srv := GameServerInstance{ID: "vh", Script: "vhserver", Dir: "/srv/lgsm/vh", Layout: LayoutData}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.RunAsUser(ctx, srv, ExecIO{}, false, "tmux", "ls"); err != nil {
		t.Fatal(err)
	}
	if err := d.RunAsUser(context.Background(), srv, ExecIO{}, false, "tar", "-xf", "a"); err != nil {
		t.Fatal(err)
	}
	if err := d.RunAsUser(ctx, srv, ExecIO{}, true, "rclone", "copy"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(e.calls[0].Cmd, " ")
	if !strings.HasPrefix(got, "timeout -s KILL ") || !strings.HasSuffix(got, " tmux ls") {
		t.Errorf("with a deadline: %q", got)
	}
	if s, _ := strconv.Atoi(e.calls[0].Cmd[3]); s < 6 {
		t.Errorf("the container must not end it before the context: %d s", s)
	}
	if got := strings.Join(e.calls[1].Cmd, " "); got != "tar -xf a" {
		t.Errorf("without a deadline: %q", got)
	}
	if e.calls[2].Cmd[0] != "bash" {
		t.Errorf("login path: %q", e.calls[2].Cmd)
	}
}

func TestProcStatsKeepsValuesWhenTmuxHangs(t *testing.T) {
	c := &procStatsCache{}
	c.update("a", []procSample{{pid: 1, rssKB: 1024 * 1024}}, time.Now().Add(-time.Minute))
	_, ram, pids := c.measure("a", func() ([]procSample, bool) { return nil, false })
	if ram != 1 || len(pids) != 1 {
		t.Fatalf("values lost: %v %v", ram, pids)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		running := c.inflight["a"] != nil
		c.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("measurement did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := c.entries["a"]; !ok {
		t.Fatal("entry dropped")
	}
	// A session that is really gone clears them.
	c.measure("a", func() ([]procSample, bool) { return nil, true })
	if _, ram, pids = c.measure("a", func() ([]procSample, bool) { return nil, true }); ram != 0 || pids != nil {
		t.Fatalf("stopped server keeps %v %v", ram, pids)
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
