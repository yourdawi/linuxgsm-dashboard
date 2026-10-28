package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRuntime records every call and answers with canned results.
type fakeRuntime struct {
	mu       sync.Mutex
	calls    []string
	crontab  string
	lgsmOut  string
	lgsmErr  error
	deleted  []bool
	installs []InstallRequest
}

func (f *fakeRuntime) record(format string, args ...interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeRuntime) Name() string               { return "fake" }
func (f *fakeRuntime) Capabilities() Capabilities { return Capabilities{Runtime: "fake"} }
func (f *fakeRuntime) Init()                      {}
func (f *fakeRuntime) Discover([]string) ([]*GameServerInstance, error) {
	return nil, nil
}
func (f *fakeRuntime) ProcessStats(GameServerInstance) (float64, float64, []int) {
	return 1.5, 2, []int{42}
}
func (f *fakeRuntime) RunLGSM(ctx context.Context, srv GameServerInstance, stdio ExecIO, args ...string) error {
	f.record("lgsm %s %s", srv.Script, strings.Join(args, " "))
	if stdio.Stdout != nil {
		_, _ = io.WriteString(stdio.Stdout, f.lgsmOut)
	}
	return f.lgsmErr
}
func (f *fakeRuntime) RunAsUser(ctx context.Context, srv GameServerInstance, stdio ExecIO, login bool, args ...string) error {
	f.record("user %s login=%v %s", srv.User, login, strings.Join(args, " "))
	return nil
}
func (f *fakeRuntime) ValidateInstall(req InstallRequest) error {
	if req.Dir == "/bad" {
		return fmt.Errorf("bad dir")
	}
	return nil
}
func (f *fakeRuntime) Install(ctx context.Context, req InstallRequest, logf func(string)) error {
	f.mu.Lock()
	f.installs = append(f.installs, req)
	f.mu.Unlock()
	logf("installing " + req.GameCmd + "\n")
	return &ExitError{Code: 3}
}
func (f *fakeRuntime) Clone(ctx context.Context, srv GameServerInstance, suffix string) (string, error) {
	f.record("clone %s %s", srv.ID, suffix)
	return srv.ID + "-" + suffix, nil
}
func (f *fakeRuntime) Delete(ctx context.Context, srv GameServerInstance, sharedUser bool) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, sharedUser)
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) ReadCrontab(GameServerInstance) (string, error) { return f.crontab, nil }
func (f *fakeRuntime) WriteCrontab(srv GameServerInstance, content string) error {
	f.crontab = content
	return nil
}
func (f *fakeRuntime) InstallAutostart(srv GameServerInstance) error {
	f.record("autostart %s", srv.Script)
	return nil
}

func newFakeManager(rt *fakeRuntime, instances ...GameServerInstance) *InstanceManager {
	im := NewStaticInstanceManager("", instances...)
	im.rt = rt
	return im
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{&ExitError{Code: 7}, 7},
		{fmt.Errorf("wrapped: %w", &ExitError{Code: 2}), 2},
		{&StartError{Err: errors.New("no runuser")}, 1},
		{errors.New("other"), 1},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	if !isStartError(&StartError{Err: errors.New("x")}) || isStartError(&ExitError{Code: 1}) {
		t.Fatal("isStartError misclassifies errors")
	}
}

func TestHostRuntimeReportsStartErrors(t *testing.T) {
	err := runHostCmd(exec.Command("lgsm-dashboard-command-that-does-not-exist"), ExecIO{})
	if !isStartError(err) {
		t.Fatalf("expected a start error, got %v", err)
	}
}

func TestStreamLines(t *testing.T) {
	var lines []string
	err := streamLines(func(w io.Writer) error {
		_, _ = io.WriteString(w, "one\ntwo\nthree")
		return &ExitError{Code: 4}
	}, func(line string) { lines = append(lines, line) }, nil)
	if ExitCode(err) != 4 {
		t.Fatalf("exit code not passed through: %v", err)
	}
	if strings.Join(lines, "|") != "one\n|two\n|three" {
		t.Fatalf("unexpected lines %q", lines)
	}
}

func TestSelectRuntime(t *testing.T) {
	// Tests may run inside a container: pretend to be on a plain host.
	old := containerMarkers
	t.Cleanup(func() { containerMarkers = old })
	containerMarkers.files = nil
	containerMarkers.cgroup = filepath.Join(t.TempDir(), "missing")

	t.Setenv("LGSM_RUNTIME", "")
	if rt, err := SelectRuntime(""); err != nil || rt.Name() != "host" {
		t.Fatalf("default runtime: %v %v", rt, err)
	}
	t.Setenv("LGSM_RUNTIME", " Host ")
	if rt, err := SelectRuntime(""); err != nil || rt.Name() != "host" {
		t.Fatalf("explicit host runtime: %v %v", rt, err)
	}
	t.Setenv("LGSM_RUNTIME", "podman")
	if _, err := SelectRuntime(""); err == nil {
		t.Fatal("unknown runtime must be rejected")
	}
}

func TestHostRuntimeCapabilities(t *testing.T) {
	c := HostRuntime{}.Capabilities()
	if c.Runtime != "host" || !c.LinuxUsers || !c.CustomInstallDir || !c.ServerPaths || !c.MultiInstance ||
		!c.Cron || !c.Autostart || !c.Firewall || !c.SelfUpdate {
		t.Fatalf("host runtime must keep every feature: %+v", c)
	}
}

func TestManagerUsesRuntime(t *testing.T) {
	rt := &fakeRuntime{}
	srv := GameServerInstance{ID: "vhserver", Script: "vhserver", User: "vh", Dir: "/home/vh", Status: "running"}
	im := newFakeManager(rt, srv)

	if err := im.RunActionDirect("vhserver", "restart"); err != nil {
		t.Fatal(err)
	}
	if err := im.InstallSystemdService("vhserver"); err != nil {
		t.Fatal(err)
	}
	if cpu, ram, pids := im.ProcessStats(srv); cpu != 1.5 || ram != 2 || len(pids) != 1 {
		t.Fatalf("process stats not taken from the runtime: %v %v %v", cpu, ram, pids)
	}
	want := []string{"lgsm vhserver restart", "autostart vhserver"}
	if strings.Join(rt.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %q, want %q", rt.calls, want)
	}
}

func TestRunActionStreamsRuntimeOutput(t *testing.T) {
	rt := &fakeRuntime{lgsmOut: "\x1b[32mStarting\x1b[0m\nOK\n", lgsmErr: &ExitError{Code: 5}}
	im := newFakeManager(rt, GameServerInstance{ID: "vhserver", Script: "vhserver", User: "vh", Dir: "/home/vh"})

	rec := httptest.NewRecorder()
	im.RunAction(rec, httptest.NewRequest("POST", "/", nil), "vhserver", "start", "en")
	body := rec.Body.String()
	for _, want := range []string{`"text":"Starting\n"`, `"text":"OK\n"`, `"type":"exit"`, `"code":5`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestInstallGameUsesRuntime(t *testing.T) {
	rt := &fakeRuntime{}
	im := newFakeManager(rt)

	rec := httptest.NewRecorder()
	im.InstallGame(rec, httptest.NewRequest("POST", "/", nil), "vhserver", "vh", "", "/bad", "en")
	if rec.Code != 400 || len(rt.installs) != 0 {
		t.Fatalf("validation must stop the installation: %d %v", rec.Code, rt.installs)
	}

	rec = httptest.NewRecorder()
	im.InstallGame(rec, httptest.NewRequest("POST", "/", nil), "vhserver", "vh", "pw", "", "de")
	if len(rt.installs) != 1 || rt.installs[0].Username != "vh" || rt.installs[0].Lang != "de" {
		t.Fatalf("unexpected install request %+v", rt.installs)
	}
	if body := rec.Body.String(); !strings.Contains(body, "installing vhserver") || !strings.Contains(body, `"code":3`) {
		t.Fatalf("install output or exit code missing: %s", body)
	}
}

func TestDeleteServerPassesSharedUser(t *testing.T) {
	rt := &fakeRuntime{}
	im := newFakeManager(rt,
		GameServerInstance{ID: "vhserver", Script: "vhserver", User: "vh", Dir: "/home/vh"},
		GameServerInstance{ID: "vhserver-2", Script: "vhserver-2", User: "vh", Dir: "/home/vh"},
		GameServerInstance{ID: "csserver", Script: "csserver", User: "cs", Dir: "/home/cs"},
	)
	im.DeleteServer(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/", nil), "vhserver")
	im.DeleteServer(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/", nil), "csserver")
	if len(rt.deleted) != 2 || !rt.deleted[0] || rt.deleted[1] {
		t.Fatalf("sharedUser flags = %v, want [true false]", rt.deleted)
	}
	if rt.calls[0] != "lgsm vhserver stop" {
		t.Fatalf("server must be stopped before deletion: %q", rt.calls)
	}
}

func TestCronRewriteUsesRuntimeCrontab(t *testing.T) {
	srv := GameServerInstance{ID: "vhserver", Script: "vhserver", User: "vh", Dir: "/home/vh", Name: "Valheim"}
	rt := &fakeRuntime{crontab: "*/5 * * * * /home/vh/vhserver monitor > /dev/null 2>&1\n0 4 * * * /usr/bin/other\n"}
	if err := removeCronLinesForScript(rt, srv, nil); err != nil {
		t.Fatal(err)
	}
	if rt.crontab != "0 4 * * * /usr/bin/other" {
		t.Fatalf("unexpected crontab %q", rt.crontab)
	}
}

type discoverRuntime struct {
	*fakeRuntime
	found []GameServerInstance
}

func (d discoverRuntime) Discover([]string) ([]*GameServerInstance, error) {
	var out []*GameServerInstance
	for i := range d.found {
		inst := d.found[i]
		out = append(out, &inst)
	}
	return out, nil
}

// While LinuxGSM's auto-install runs, the dashboard must not start `details` for the new script.
func TestNoDetailsDuringInstall(t *testing.T) {
	rt := &fakeRuntime{}
	im := NewStaticInstanceManager(t.TempDir())
	im.static = false
	im.rt = discoverRuntime{rt, []GameServerInstance{{ID: "ts3server", Script: "ts3server", User: "ts3", Dir: t.TempDir(), Status: "stopped"}}}

	done := im.beginInstall("ts3")
	im.ScanInstances()
	if s, _ := im.getServer("ts3server"); s.Status != "installing" {
		t.Errorf("status during install = %q", s.Status)
	}
	im.RefreshServerDetailsForce("ts3server")
	time.Sleep(100 * time.Millisecond)
	rt.mu.Lock()
	calls := len(rt.calls)
	rt.mu.Unlock()
	if calls != 0 {
		t.Fatalf("LinuxGSM was run during the installation: %v", rt.calls)
	}

	done()
	im.ScanInstances()
	if s, _ := im.getServer("ts3server"); s.Status != "stopped" {
		t.Errorf("status after install = %q", s.Status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rt.mu.Lock()
		calls = len(rt.calls)
		rt.mu.Unlock()
		if calls > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("details did not run after the installation")
}
