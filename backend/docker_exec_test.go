package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// execCall is one exec the fake engine received.
type execCall struct {
	Container  string
	Cmd        []string
	User       string
	WorkingDir string
	Stdin      string
}

// execEngine is a fake Docker engine that implements the exec flow including the hijacked
// stream. respond decides the output and exit code of each command.
type execEngine struct {
	mu       sync.Mutex
	calls    []execCall
	pending  map[string]*execCall
	codes    map[string]int
	started  []string
	running  map[string]bool
	respond  func(c execCall) (stdout, stderr string, code int)
	server   *httptest.Server
	nextExec int

	missing  map[string]bool // containers that do not exist (404 on inspect)
	images   []string
	created  map[string]DockerCreateConfig
	removed  []string
	logs     string            // container log returned by /logs
	policies map[string]string // restart policy per container (POST /update)
	image    string            // Config.Image reported by inspect
}

func newExecEngine(t *testing.T, respond func(c execCall) (string, string, int)) *execEngine {
	t.Helper()
	e := &execEngine{pending: map[string]*execCall{}, codes: map[string]int{}, running: map[string]bool{}, respond: respond,
		missing: map[string]bool{}, created: map[string]DockerCreateConfig{}}
	e.server = httptest.NewServer(http.HandlerFunc(e.handle))
	t.Cleanup(e.server.Close)
	return e
}

func (e *execEngine) client(t *testing.T) *DockerClient {
	t.Helper()
	c, err := NewDockerClient(context.Background(), "tcp://"+strings.TrimPrefix(e.server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeFrame(w io.Writer, stream byte, data string) {
	if data == "" {
		return
	}
	var h [8]byte
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(data)))
	_, _ = w.Write(h[:])
	_, _ = io.WriteString(w, data)
}

func (e *execEngine) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/version":
		_ = json.NewEncoder(w).Encode(DockerVersion{Version: "29.0.0", APIVersion: "1.52"})

	case strings.HasSuffix(path, "/exec") && strings.HasPrefix(path, "/v1.52/containers/"):
		container := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.52/containers/"), "/exec")
		var body struct {
			Cmd        []string
			User       string
			WorkingDir string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		running, known := e.running[container]
		if known && !running {
			e.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"container ` + container + ` is not running"}`))
			return
		}
		e.nextExec++
		id := "exec" + string(rune('a'+e.nextExec))
		e.pending[id] = &execCall{Container: container, Cmd: body.Cmd, User: body.User, WorkingDir: body.WorkingDir}
		e.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})

	case strings.HasPrefix(path, "/v1.52/exec/") && strings.HasSuffix(path, "/start"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.52/exec/"), "/start")
		e.mu.Lock()
		call := e.pending[id]
		e.mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body) // {"Detach":false,...}, before the stream takes over
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil || call == nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		_ = buf.Flush()
		if call.Cmd != nil && len(call.Cmd) > 0 && call.Cmd[0] == "cat" {
			data, _ := io.ReadAll(bufio.NewReader(buf))
			call.Stdin = string(data)
		}
		stdout, stderr, code := e.respond(*call)
		writeFrame(conn, 1, stdout)
		writeFrame(conn, 2, stderr)
		e.mu.Lock()
		e.calls = append(e.calls, *call)
		e.codes[id] = code
		e.mu.Unlock()

	case strings.HasPrefix(path, "/v1.52/exec/") && strings.HasSuffix(path, "/json"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.52/exec/"), "/json")
		e.mu.Lock()
		code := e.codes[id]
		e.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"Running": false, "ExitCode": code})

	case path == "/v1.52/images/create":
		e.mu.Lock()
		e.images = append(e.images, r.URL.Query().Get("fromImage")+":"+r.URL.Query().Get("tag"))
		e.mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"Pulling from gameservermanagers/gameserver","id":"vh"}`+"\n"+
			`{"status":"Downloading","progress":"[=>  ]","id":"abc"}`+"\n"+
			`{"status":"Pull complete","id":"abc"}`+"\n"+
			`{"status":"Status: Downloaded newer image for gameservermanagers/gameserver:vh"}`+"\n")

	case path == "/v1.52/containers/create":
		var cfg DockerCreateConfig
		_ = json.NewDecoder(r.Body).Decode(&cfg)
		name := r.URL.Query().Get("name")
		e.mu.Lock()
		e.created[name] = cfg
		delete(e.missing, name)
		e.running[name] = false
		e.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"Id":"c0ffee"}`)

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/v1.52/containers/"):
		e.mu.Lock()
		e.removed = append(e.removed, strings.TrimPrefix(path, "/v1.52/containers/")+"?"+r.URL.RawQuery)
		e.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case strings.HasPrefix(path, "/v1.52/containers/") && strings.HasSuffix(path, "/logs"):
		writeFrame(w, 1, e.logs)

	case strings.HasPrefix(path, "/v1.52/containers/") && strings.HasSuffix(path, "/json"):
		container := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.52/containers/"), "/json")
		e.mu.Lock()
		running, known := e.running[container]
		missing := e.missing[container]
		image := e.image
		e.mu.Unlock()
		if missing {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such container"}`)
			return
		}
		if !known {
			running = true
		}
		status := "exited"
		if running {
			status = "running"
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"State": map[string]interface{}{"Running": running, "Status": status},
			"Config": map[string]interface{}{"Image": image}})

	case strings.HasPrefix(path, "/v1.52/containers/") && strings.HasSuffix(path, "/update"):
		container := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.52/containers/"), "/update")
		var body struct{ RestartPolicy DockerRestartPolicy }
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		if e.policies == nil {
			e.policies = map[string]string{}
		}
		e.policies[container] = body.RestartPolicy.Name
		e.mu.Unlock()
		_, _ = w.Write([]byte("{}"))

	case strings.HasPrefix(path, "/v1.52/containers/") && strings.HasSuffix(path, "/start"):
		container := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.52/containers/"), "/start")
		e.mu.Lock()
		e.started = append(e.started, container)
		e.running[container] = true
		e.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestDemuxDockerStream(t *testing.T) {
	var stream bytes.Buffer
	writeFrame(&stream, 1, "out1 ")
	writeFrame(&stream, 2, "err1")
	writeFrame(&stream, 1, "out2")
	var out, errOut bytes.Buffer
	if err := demuxDockerStream(&stream, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "out1 out2" || errOut.String() != "err1" {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}

	// A truncated header is an error, nil writers discard.
	if err := demuxDockerStream(bytes.NewReader([]byte{1, 0, 0}), nil, nil); err == nil {
		t.Fatal("truncated frame header must fail")
	}
}

func TestDockerExec(t *testing.T) {
	e := newExecEngine(t, func(c execCall) (string, string, int) {
		if c.Cmd[0] == "cat" {
			return c.Stdin, "", 0
		}
		return "hello\n", "warning\n", 3
	})
	c := e.client(t)

	var out, errOut bytes.Buffer
	code, err := c.Exec(context.Background(), "vhserver", DockerExecConfig{Cmd: []string{"./vhserver", "details"}, User: "linuxgsm", WorkingDir: "/app"}, ExecIO{Stdout: &out, Stderr: &errOut})
	if code != 3 || ExitCode(err) != 3 || out.String() != "hello\n" || errOut.String() != "warning\n" {
		t.Fatalf("code=%d err=%v out=%q err=%q", code, err, out.String(), errOut.String())
	}
	if got := e.calls[0]; got.User != "linuxgsm" || got.WorkingDir != "/app" || strings.Join(got.Cmd, " ") != "./vhserver details" {
		t.Fatalf("unexpected exec %+v", got)
	}

	// stdin is sent and closed, so the command sees EOF.
	out.Reset()
	if _, err := c.Exec(context.Background(), "vhserver", DockerExecConfig{Cmd: []string{"cat"}}, ExecIO{Stdin: strings.NewReader("y\ny\n"), Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "y\ny\n" {
		t.Fatalf("stdin not passed through: %q", out.String())
	}

	// A stopped container cannot run commands.
	e.running["stopped"] = false
	if _, err := c.Exec(context.Background(), "stopped", DockerExecConfig{Cmd: []string{"true"}}, ExecIO{}); !isStartError(err) || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected a start error for a stopped container, got %v", err)
	}
}

func newTestDockerRuntime(t *testing.T, e *execEngine) *DockerRuntime {
	return &DockerRuntime{client: e.client(t), dataDir: "/srv/lgsm"}
}

func TestDockerRuntimeCommands(t *testing.T) {
	e := newExecEngine(t, func(c execCall) (string, string, int) {
		if c.Cmd[0] == "sh" {
			return "10 (srv) S 1 10 10 0 -1 4194304 1 0 0 0 150 50 0 0 20 0 1 0 100 1000 2 0 0 0\n11 (sh) S 10 10 10 0 -1 4194304 1 0 0 0 5 5 0 0 20 0 1 0 100 1000 1 0 0 0\n", "", 0
		}
		return "", "", 0
	})
	d := newTestDockerRuntime(t, e)
	srv := GameServerInstance{ID: "vh", Script: "vhserver", Dir: "/srv/lgsm/vh", Layout: LayoutData}

	if err := d.RunLGSM(context.Background(), srv, ExecIO{}, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := d.RunAsUser(context.Background(), srv, ExecIO{}, false, "tar", "-xf", "/srv/lgsm/vh/backup/a.tar.zst", "-C", "/srv/lgsm/vh"); err != nil {
		t.Fatal(err)
	}
	if err := d.RunAsUser(context.Background(), srv, ExecIO{}, true, "rclone", "copy", "/srv/lgsm/vh/backup", "remote:x y"); err != nil {
		t.Fatal(err)
	}
	cpu, ram, pids := d.ProcessStats(srv)
	if cpu != 0 || ram != float64(3*os.Getpagesize())/1024/1024/1024 || len(pids) != 2 {
		t.Fatalf("unexpected stats %v %v %v", cpu, ram, pids)
	}

	want := []string{
		"linuxgsm /app ./vhserver stop",
		"linuxgsm /data tar -xf /data/backup/a.tar.zst -C /data",
		"linuxgsm /data bash -lc 'rclone' 'copy' '/data/backup' 'remote:x y'",
		"root  sh -c ps -u linuxgsm -o pid= | while read -r p; do cat /proc/$p/stat 2>/dev/null; done",
	}
	for i, c := range e.calls {
		got := c.User + " " + c.WorkingDir + " " + strings.Join(c.Cmd, " ")
		if got != want[i] {
			t.Errorf("call %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestDockerRuntimeStartsStoppedContainer(t *testing.T) {
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	e.running["vh"] = false
	d := newTestDockerRuntime(t, e)
	srv := GameServerInstance{ID: "vh", Script: "vhserver", Dir: "/srv/lgsm/vh", Layout: LayoutData}

	var out bytes.Buffer
	if err := d.RunLGSM(context.Background(), srv, ExecIO{Stdout: &out}, "start"); err != nil {
		t.Fatal(err)
	}
	if len(e.started) != 1 || e.started[0] != "vh" || len(e.calls) != 0 || !strings.Contains(out.String(), "starting it") {
		t.Fatalf("container not started: started=%v calls=%v out=%q", e.started, e.calls, out.String())
	}
	// Running now: start goes to LinuxGSM.
	if err := d.RunLGSM(context.Background(), srv, ExecIO{}, "start"); err != nil || len(e.calls) != 1 {
		t.Fatalf("second start must run LinuxGSM: %v %v", err, e.calls)
	}
}

func TestDockerDiscoverChecksTmux(t *testing.T) {
	e := newExecEngine(t, func(c execCall) (string, string, int) {
		if slices.Contains(c.Cmd, "tmux") && c.Container == "vh-up" {
			return "vhserver\n", "", 0
		}
		return "", "no server running", 1
	})
	labels := func(dir string) map[string]string {
		return map[string]string{dockerLabelManaged: "true", dockerLabelGameServer: "vhserver", dockerLabelDir: dir}
	}
	list := []DockerContainer{
		{Names: []string{"/vh-up"}, State: "running", Labels: labels("/srv/lgsm/vh-up")},
		{Names: []string{"/vh-idle"}, State: "running", Labels: labels("/srv/lgsm/vh-idle")},
	}
	e.server.Config.Handler = wrapContainerList(e, list)
	d := newTestDockerRuntime(t, e)
	found, err := d.Discover(nil)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, f := range found {
		status[f.ID] = f.Status
		if f.Layout != LayoutData || f.User != "linuxgsm" {
			t.Fatalf("unexpected instance %+v", f)
		}
	}
	if status["vh-up"] != "running" || status["vh-idle"] != "stopped" {
		t.Fatalf("unexpected status %v", status)
	}
}

func TestDataLayoutPaths(t *testing.T) {
	srv := GameServerInstance{Dir: "/srv/lgsm/vh", Script: "vhserver", Layout: LayoutData}
	host := GameServerInstance{Dir: "/home/vh", Script: "vhserver"}

	if srv.configRel("vhserver") != "config-lgsm/vhserver" && srv.configRel("vhserver") != filepath.Join("config-lgsm", "vhserver") {
		t.Fatalf("unexpected config dir %s", srv.configRel("vhserver"))
	}
	if host.configRel("vhserver") != filepath.Join("lgsm", "config-lgsm", "vhserver") {
		t.Fatalf("host layout changed: %s", host.configRel("vhserver"))
	}
	if srv.lgsmDataRel() != "data" || host.lgsmDataRel() != filepath.Join("lgsm", "data") {
		t.Fatal("unexpected data dirs")
	}
	if got := srv.dashboardPath("/data/backup"); got != filepath.Join("/srv/lgsm/vh", "backup") {
		t.Fatalf("dashboardPath(/data/backup) = %s", got)
	}
	if srv.dashboardPath("/app/lgsm/backup") != "" || srv.dashboardPath("/database") != "" {
		t.Fatal("paths outside /data must not be visible")
	}
	if host.dashboardPath("/home/vh/backup") != "/home/vh/backup" {
		t.Fatal("host paths must stay unchanged")
	}
	if srv.containerPath("/srv/lgsm/vh/serverfiles/x") != "/data/serverfiles/x" || srv.containerPath("/srv/lgsm/vh") != "/data" ||
		srv.containerPath("/srv/lgsm/vh2/x") != "/srv/lgsm/vh2/x" || srv.containerPath("-xf") != "-xf" {
		t.Fatal("unexpected container path mapping")
	}
}

func TestCustomBackupDirInContainer(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config-lgsm", "vhserver")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "common.cfg"), []byte("backupdir=\"/data/backup\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	srv := GameServerInstance{Dir: dir, Script: "vhserver", Layout: LayoutData}
	if got := customBackupDir(srv); got != filepath.Join(dir, "backup") {
		t.Fatalf("customBackupDir = %q", got)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "common.cfg"), []byte("backupdir=\"${lgsmdir}/backup\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := customBackupDir(srv); got != "" {
		t.Fatalf("a backup dir inside /app is not visible, got %q", got)
	}
}

func TestContainerRestoreMembers(t *testing.T) {
	var m containerRestoreMembers
	for _, name := range []string{
		"././", "././ts3server", "././entrypoint.sh", "././lgsm/", "././lgsm/modules/core.sh",
		"././lgsm/config-lgsm/ts3server/common.cfg", "././lgsm/data/ts3server.uid",
		"././log/console/x.log", "././log/server/y.log", "././serverfiles/ts3server.ini", "././serverfiles/doc/a",
		"./logs/other", "././lgsm/node_modules/x/serverfiles/z", "././lgsm/mods/installed-mods.txt",
	} {
		m.add(name)
	}
	cmd := m.restoreCommand(ArchiveTarZst, "/srv/lgsm/vh/backup/a.tar.zst")
	if cmd[0] != "sh" || cmd[1] != "-c" || cmd[2] != containerRestoreScript {
		t.Fatalf("unexpected command %q", cmd[:3])
	}
	got := strings.Join(cmd[3:], " ")
	want := "restore /data/.lgsm-dashboard-restore /srv/lgsm/vh/backup/a.tar.zst --zstd ././lgsm/config-lgsm ././lgsm/data ././log ././serverfiles ././lgsm/mods"
	if got != want {
		t.Fatalf("restore args\n got %s\nwant %s", got, want)
	}
}

// wrapContainerList answers GET /containers/json with list and everything else with e.
func wrapContainerList(e *execEngine, list []DockerContainer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.52/containers/json" {
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		e.handle(w, r)
	})
}

// A server stopped in the dashboard must stay stopped after a reboot: the restart policy follows.
func TestDockerRestartPolicyFollowsStartStop(t *testing.T) {
	e := newExecEngine(t, func(c execCall) (string, string, int) { return "", "", 0 })
	d := newTestDockerRuntime(t, e)
	srv := GameServerInstance{ID: "vh", Script: "vhserver", Dir: "/srv/lgsm/vh", Layout: LayoutData}
	e.running["vh"] = true
	policy := func() string { e.mu.Lock(); defer e.mu.Unlock(); return e.policies["vh"] }
	if err := d.RunLGSM(context.Background(), srv, ExecIO{}, "stop"); err != nil {
		t.Fatal(err)
	}
	if policy() != "no" {
		t.Fatalf("after stop: %q", policy())
	}
	if err := d.RunLGSM(context.Background(), srv, ExecIO{}, "start"); err != nil {
		t.Fatal(err)
	}
	if policy() != "unless-stopped" {
		t.Fatalf("after start: %q", policy())
	}
	if err := d.RunLGSM(context.Background(), srv, ExecIO{}, "backup"); err != nil || policy() != "unless-stopped" {
		t.Fatalf("backup changed the policy: %q %v", policy(), err)
	}
}
