package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEngine answers the Docker API endpoints the client uses.
func fakeEngine(t *testing.T, containers []DockerContainer) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(DockerVersion{Version: "29.0.0", APIVersion: "1.52", Os: "linux", Arch: "amd64"})
		case "/v1.52/containers/json":
			_ = json.NewEncoder(w).Encode(containers)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"page not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestDockerClientNegotiatesAPIVersion(t *testing.T) {
	engine, seen := fakeEngine(t, []DockerContainer{{Names: []string{"/vhserver"}, State: "running"}})
	host := "tcp://" + strings.TrimPrefix(engine.URL, "http://")

	c, err := NewDockerClient(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListContainers(context.Background(), dockerLabelManaged+"=true")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name() != "vhserver" {
		t.Fatalf("unexpected containers %+v", list)
	}
	last := (*seen)[len(*seen)-1]
	if !strings.HasPrefix(last, "/v1.52/containers/json?") || !strings.Contains(last, "all=1") || !strings.Contains(last, "lgsm-dashboard.managed%3Dtrue") {
		t.Fatalf("unexpected request %s", last)
	}

	// API errors carry the engine message.
	if err := c.do(context.Background(), http.MethodGet, "/nope", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "page not found") {
		t.Fatalf("expected engine error message, got %v", err)
	}
}

func TestDockerClientRejectsBadHosts(t *testing.T) {
	for _, host := range []string{"ssh://host", "tcp://", "unix:///nonexistent/docker.sock"} {
		if _, err := NewDockerClient(context.Background(), host); err == nil {
			t.Errorf("host %q must fail", host)
		}
	}
}

func TestDockerInstances(t *testing.T) {
	labels := func(script, dir string) map[string]string {
		return map[string]string{dockerLabelManaged: "true", dockerLabelGameServer: script, dockerLabelDir: dir}
	}
	containers := []DockerContainer{
		{Names: []string{"/vhserver"}, State: "running", Labels: labels("vhserver", "/srv/lgsm/vhserver")},
		{Names: []string{"/rust-1"}, State: "exited", Labels: labels("rustserver", "/srv/lgsm/rust-1")},
		{Names: []string{"/outside"}, State: "running", Labels: labels("vhserver", "/etc")},
		{Names: []string{"/escape"}, State: "running", Labels: labels("vhserver", "/srv/lgsm/../../root")},
		{Names: []string{"/root-dir"}, State: "running", Labels: labels("vhserver", "/srv/lgsm")},
		{Names: []string{"/badscript"}, State: "running", Labels: labels("x';id;'", "/srv/lgsm/bad")},
		{Names: []string{"/bad name"}, State: "running", Labels: labels("vhserver", "/srv/lgsm/badname")},
	}
	found := dockerInstances(containers, "/srv/lgsm")
	if len(found) != 2 {
		t.Fatalf("expected 2 instances, got %d: %+v", len(found), found)
	}
	if found[0].ID != "vhserver" || found[0].Status != "running" || found[0].Dir != "/srv/lgsm/vhserver" || found[0].Script != "vhserver" {
		t.Fatalf("unexpected instance %+v", found[0])
	}
	if found[1].ID != "rust-1" || found[1].Status != "stopped" || found[1].Script != "rustserver" {
		t.Fatalf("unexpected instance %+v", found[1])
	}
}

func TestRuntimeModeDetection(t *testing.T) {
	dir := t.TempDir()
	old := containerMarkers
	t.Cleanup(func() { containerMarkers = old })
	cgroup := filepath.Join(dir, "cgroup")
	containerMarkers.files = []string{filepath.Join(dir, ".dockerenv")}
	containerMarkers.cgroup = cgroup

	mustWriteFile := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("LGSM_RUNTIME", "")
	mustWriteFile(cgroup, "0::/init.scope\n")
	if mode, _ := RuntimeMode(); mode != "host" {
		t.Fatalf("plain host detected as %s", mode)
	}
	mustWriteFile(cgroup, "0::/system.slice/docker-0123abcd.scope\n")
	if mode, _ := RuntimeMode(); mode != "docker" {
		t.Fatalf("docker cgroup detected as %s", mode)
	}
	mustWriteFile(cgroup, "0::/\n")
	mustWriteFile(filepath.Join(dir, ".dockerenv"), "")
	if mode, _ := RuntimeMode(); mode != "docker" {
		t.Fatalf(".dockerenv detected as %s", mode)
	}

	// An explicit setting wins over the detection.
	t.Setenv("LGSM_RUNTIME", "host")
	if mode, _ := RuntimeMode(); mode != "host" {
		t.Fatalf("LGSM_RUNTIME=host ignored: %s", mode)
	}
	t.Setenv("LGSM_RUNTIME", "docker")
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")
	if _, err := SelectRuntime(); err == nil || !strings.Contains(err.Error(), "docker.sock") {
		t.Fatalf("unreachable engine must fail with a hint, got %v", err)
	}
}

func TestDockerRuntimeSelection(t *testing.T) {
	engine, _ := fakeEngine(t, nil)
	t.Setenv("LGSM_RUNTIME", "docker")
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(engine.URL, "http://"))
	t.Setenv("LGSM_DATA_DIR", "/srv/games")
	rt, err := SelectRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if rt.Name() != "docker" || rt.Capabilities().Runtime != "docker" || rt.Capabilities().SelfUpdate {
		t.Fatalf("unexpected runtime %s %+v", rt.Name(), rt.Capabilities())
	}
	if err := rt.ValidateInstall(InstallRequest{}); err == nil {
		t.Fatal("installing must be refused until it is implemented")
	}
	if err := rt.RunLGSM(context.Background(), GameServerInstance{}, ExecIO{}, "start"); !isStartError(err) {
		t.Fatalf("commands must report a start error, got %v", err)
	}

	t.Setenv("LGSM_DATA_DIR", "/etc")
	if _, err := SelectRuntime(); err == nil {
		t.Fatal("a system directory must be rejected as data directory")
	}
}
