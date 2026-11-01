package backend

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestGameShortname(t *testing.T) {
	for in, want := range map[string]string{"vhserver": "vh", "ts3server": "ts3", "csgoserver": "csgo", "pmcserver": "pmc"} {
		if got, ok := gameShortname(in); !ok || got != want {
			t.Errorf("gameShortname(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"server", "vh", "vhserver-2", "../server", "a.bserver"} {
		if _, ok := gameShortname(in); ok {
			t.Errorf("gameShortname(%q) must fail", in)
		}
	}
}

// dockerInstallRuntime returns a runtime whose data directory is a temporary directory.
func dockerInstallRuntime(t *testing.T, e *execEngine) (*DockerRuntime, string) {
	t.Helper()
	// Not below /tmp: system directories are never accepted as server directories.
	tmp, err := os.MkdirTemp(".", ".test-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	abs, err := filepath.Abs(tmp)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.ToSlash(abs)
	return &DockerRuntime{client: e.client(t), dataDir: dataDir, image: defaultGameServerImage}, dataDir
}

func TestDockerValidateInstall(t *testing.T) {
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	d, dataDir := dockerInstallRuntime(t, e)
	e.missing["vhserver"] = true

	if err := d.ValidateInstall(InstallRequest{GameCmd: "vhserver", Username: "vhserver"}); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := map[string]InstallRequest{
		"custom dir":       {GameCmd: "vhserver", Username: "vhserver", Dir: "/mnt/ssd/vh"},
		"bad name":         {GameCmd: "vhserver", Username: "Bad Name"},
		"bad game":         {GameCmd: "vhserver-2", Username: "vhserver"},
		"container in use": {GameCmd: "vhserver", Username: "taken"},
	}
	for name, req := range cases {
		if err := d.ValidateInstall(req); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "vhserver"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := d.ValidateInstall(InstallRequest{GameCmd: "vhserver", Username: "vhserver"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing data directory must be rejected, got %v", err)
	}
}

func TestDockerInstall(t *testing.T) {
	e := newExecEngine(t, func(c execCall) (string, string, int) {
		if slices.Contains(c.Cmd, "tmux") {
			return "vhserver\n", "", 0
		}
		return "", "", 0
	})
	e.logs = "Installing vhserver\n\x1b[32mOK\x1b[0m\nTail log files\nnever read\n"
	d, dataDir := dockerInstallRuntime(t, e)

	var log strings.Builder
	err := d.Install(context.Background(), InstallRequest{GameCmd: "vhserver", Username: "vh1", Lang: "en"}, func(s string) { log.WriteString(s) })
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, log.String())
	}
	if len(e.images) != 1 || e.images[0] != "gameservermanagers/gameserver:vh" {
		t.Fatalf("unexpected pull %v", e.images)
	}
	cfg, ok := e.created["vh1"]
	if !ok {
		t.Fatalf("container not created: %v", e.created)
	}
	dir := dataDir + "/vh1"
	if cfg.Image != "gameservermanagers/gameserver:vh" || cfg.StopTimeout != 180 ||
		strings.Join(cfg.Env, ",") != "UPDATE_CHECK=0,UPDATE_ON_START=false" ||
		cfg.Labels[dockerLabelManaged] != "true" || cfg.Labels[dockerLabelGameServer] != "vhserver" || cfg.Labels[dockerLabelDir] != dir ||
		len(cfg.HostConfig.Binds) != 2 || cfg.HostConfig.Binds[0] != dir+":/data" || cfg.HostConfig.Binds[1] != dir+"/mods:/app/lgsm/mods" ||
		cfg.HostConfig.NetworkMode != "host" || cfg.HostConfig.RestartPolicy.Name != "unless-stopped" ||
		cfg.HostConfig.LogConfig == nil || cfg.HostConfig.LogConfig.Config["max-size"] != "10m" {
		t.Fatalf("unexpected container config %+v", cfg)
	}
	if len(e.started) != 1 || e.started[0] != "vh1" {
		t.Fatalf("container not started: %v", e.started)
	}
	common, err := os.ReadFile(filepath.Join(dir, "config-lgsm", "vhserver", "common.cfg"))
	if err != nil || !strings.Contains(string(common), `backupdir="/data/backup"`) || !strings.Contains(string(common), `webdir="/data/public_html"`) {
		t.Fatalf("common.cfg not seeded: %v %q", err, common)
	}
	if info, err := os.Stat(filepath.Join(dir, "mods")); err != nil || !info.IsDir() {
		t.Fatalf("mods directory must exist before the container mounts it: %v", err)
	}
	out := log.String()
	for _, want := range []string{"Pull complete", "Installing vhserver", "OK\n", "Installation completed successfully"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Downloading") || strings.Contains(out, "never read") || strings.Contains(out, "\x1b[") {
		t.Fatalf("log contains progress, colours or lines after the end marker:\n%s", out)
	}
}

func TestDockerInstallReportsContainerExit(t *testing.T) {
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	e.logs = "Installing vhserver\nfatal error\n"
	d, _ := dockerInstallRuntime(t, e)
	var log strings.Builder
	err := d.Install(context.Background(), InstallRequest{GameCmd: "vhserver", Username: "vh1"}, func(s string) { log.WriteString(s) })
	if err == nil || !strings.Contains(log.String(), "stopped during the installation") {
		t.Fatalf("expected a failure, got %v:\n%s", err, log.String())
	}
}

func TestDockerDelete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("server directories are absolute Linux paths")
	}
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	d, dataDir := dockerInstallRuntime(t, e)
	dir := dataDir + "/vh1"
	if err := os.MkdirAll(filepath.Join(dir, "serverfiles"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(context.Background(), GameServerInstance{ID: "vh1", Dir: dir}, true); err != nil {
		t.Fatal(err)
	}
	if len(e.removed) != 1 || e.removed[0] != "vh1?force=1" {
		t.Fatalf("container not removed: %v", e.removed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("data directory still exists: %v", err)
	}

	for _, bad := range []string{dataDir, "/etc", dataDir + "/../other"} {
		if err := d.Delete(context.Background(), GameServerInstance{ID: "x", Dir: bad}, true); err == nil {
			t.Errorf("deleting %s must be refused", bad)
		}
	}
	if len(e.removed) != 1 {
		t.Fatalf("refused deletions must not touch containers: %v", e.removed)
	}
}

func TestDockerClone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cloning copies with cp -a, which is part of the Linux image")
	}
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	e.image = "gameservermanagers/gameserver:vh"
	e.missing["vh1-two"] = true
	d, dataDir := dockerInstallRuntime(t, e)
	src := dataDir + "/vh1"
	for rel, content := range map[string]string{
		"serverfiles/world.db":                      "world",
		"config-lgsm/vhserver/common.cfg":           `backupdir="/data/backup"`,
		"config-lgsm/vhserver/vhserver.cfg":         `port="2456"`,
		"config-lgsm/vhserver/secrets-vhserver.cfg": `serverpassword="secret"`,
		"log/console/vhserver-console.log":          "log",
		"mods/installed-mods.txt":                   "oxide\n",
		"mods/tmp/download.zip":                     "partial",
	} {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	srv := GameServerInstance{ID: "vh1", Script: "vhserver", Dir: src, Layout: LayoutData, UID: -1, GID: -1}
	name, err := d.Clone(context.Background(), srv, "two")
	if err != nil {
		t.Fatal(err)
	}
	dst := dataDir + "/vh1-two"
	if name != "vh1-two" || e.created[name].Image != "gameservermanagers/gameserver:vh" || e.created[name].Labels[dockerLabelDir] != dst {
		t.Fatalf("unexpected clone %s %+v", name, e.created[name])
	}
	read := func(rel string) string {
		data, _ := os.ReadFile(filepath.Join(dst, rel))
		return string(data)
	}
	if read("serverfiles/world.db") != "world" || !strings.Contains(read("config-lgsm/vhserver/common.cfg"), "backupdir") {
		t.Fatal("server files or shared configs not copied")
	}
	if strings.Contains(read("config-lgsm/vhserver/vhserver.cfg"), "2456") || !strings.Contains(read("config-lgsm/vhserver/vhserver.cfg"), "clone of vh1") {
		t.Fatalf("instance config not reset: %q", read("config-lgsm/vhserver/vhserver.cfg"))
	}
	if _, err := os.Stat(filepath.Join(dst, "config-lgsm/vhserver/secrets-vhserver.cfg")); !os.IsNotExist(err) {
		t.Fatal("instance secrets must not be copied")
	}
	if read("mods/installed-mods.txt") != "oxide\n" {
		t.Fatal("mods list not copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "mods", "tmp")); !os.IsNotExist(err) {
		t.Fatal("mods/tmp must not be copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "log")); !os.IsNotExist(err) {
		t.Fatal("logs must not be copied")
	}
	if _, err := d.Clone(context.Background(), srv, "two"); err == nil {
		t.Fatal("cloning onto an existing server must fail")
	}
}

func TestInstalledModsInContainerLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mods"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mods", "installed-mods.txt"), []byte("oxide\nrustedit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	im := NewStaticInstanceManager(t.TempDir(), GameServerInstance{ID: "rust1", Script: "rustserver", Dir: dir, Layout: LayoutData, UID: -1})
	mods, err := im.GetInstalledMods("rust1")
	if err != nil || strings.Join(mods, ",") != "oxide,rustedit" {
		t.Fatalf("unexpected mods %v %v", mods, err)
	}
}
