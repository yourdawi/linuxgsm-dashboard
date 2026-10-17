package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Labels on game server containers managed by the dashboard.
const (
	dockerLabelManaged    = "lgsm-dashboard.managed"    // "true"
	dockerLabelGameServer = "lgsm-dashboard.gameserver" // LinuxGSM script name, e.g. vhserver
	dockerLabelDir        = "lgsm-dashboard.dir"        // server data directory as seen by the dashboard
)

// defaultDockerDataDir is where the server data directories live. The directory must be
// mounted at the same path on the host and in the dashboard container, because the dashboard
// passes it to the Docker engine as bind mount source.
const defaultDockerDataDir = "/srv/lgsm"

// errDockerNotYet is returned by operations the Docker runtime does not support yet.
var errDockerNotYet = errors.New("not available in Docker mode yet")

// Inside the official images LinuxGSM runs as this user from /app.
const (
	dockerServerUser = "linuxgsm"
	dockerScriptDir  = "/app"
)

// DockerRuntime manages game servers that run as containers of the official LinuxGSM images
// (gameservermanagers/gameserver). Commands run through the Engine API exec endpoint as the
// linuxgsm user; the server data volume (/data) is visible to the dashboard below dataDir.
// Installation, deletion and scheduling follow.
type DockerRuntime struct {
	client  *DockerClient
	engine  DockerVersion
	dataDir string
}

// NewDockerRuntime connects to the Docker engine (DOCKER_HOST or the default socket). dataDir is
// the server data directory (LGSM_DATA_DIR), empty for /srv/lgsm.
func NewDockerRuntime(dataDir string) (*DockerRuntime, error) {
	if dataDir == "" {
		dataDir = defaultDockerDataDir
	}
	if !IsSafeServerDir(dataDir) {
		return nil, fmt.Errorf("invalid LGSM_DATA_DIR %q: use an absolute path outside of system directories", dataDir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := NewDockerClient(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("%w (mount /var/run/docker.sock into the dashboard container or set DOCKER_HOST)", err)
	}
	engine, _ := client.Version(ctx)
	return &DockerRuntime{client: client, engine: engine, dataDir: dataDir}, nil
}

func (d *DockerRuntime) Name() string { return "docker" }

func (d *DockerRuntime) Capabilities() Capabilities {
	return Capabilities{Runtime: "docker"}
}

func (d *DockerRuntime) Init() {
	fmt.Printf("[SYS] Docker engine %s (API %s, %s/%s), server data in %s\n",
		d.engine.Version, d.engine.APIVersion, d.engine.Os, d.engine.Arch, d.dataDir)
	if info, err := os.Stat(d.dataDir); err != nil || !info.IsDir() {
		fmt.Printf("[WARNING] Server data directory %s does not exist. Mount it at the same path as on the host.\n", d.dataDir)
	}
}

// Discover lists the containers labelled as managed by the dashboard.
func (d *DockerRuntime) Discover(customPaths []string) ([]*GameServerInstance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	containers, err := d.client.ListContainers(ctx, dockerLabelManaged+"=true")
	if err != nil {
		return nil, err
	}
	found := dockerInstances(containers, d.dataDir)
	for _, inst := range found {
		// A running container does not mean a running game server: LinuxGSM can be stopped
		// inside it. The tmux session tells.
		if inst.Status == "running" && !isTmuxSessionActive(d, *inst) {
			inst.Status = "stopped"
		}
	}
	return found, nil
}

// dockerInstances maps managed containers to instances. Containers with missing or unsafe labels
// are skipped: the labels end up in paths and commands.
func dockerInstances(containers []DockerContainer, dataDir string) []*GameServerInstance {
	var found []*GameServerInstance
	for _, c := range containers {
		id := c.Name()
		script := c.Labels[dockerLabelGameServer]
		dir := filepath.ToSlash(filepath.Clean(c.Labels[dockerLabelDir]))
		if !IsValidServerID(id) || !IsValidScriptName(script) || !IsSafeServerDir(dir) || !IsWithin(dataDir, dir) || dir == dataDir {
			continue
		}
		status := "stopped"
		if c.State == "running" {
			status = "running"
		}
		// Files written by the dashboard are handed to the owner of the data directory, which is
		// the linuxgsm user of the image (UID/GID 1000 unless configured otherwise).
		uid, gid, err := fileOwnerIDs(dir)
		if err != nil || uid <= 0 {
			uid, gid = -1, -1
		}
		found = append(found, &GameServerInstance{
			ID:     id,
			Name:   mapScriptToGameName(script),
			User:   dockerServerUser,
			Script: script,
			Dir:    dir,
			Status: status,
			Port:   parseServerPort(dir, filepath.Join(dir, "config-lgsm"), script),
			Game:   getGameFromScriptName(script),
			UID:    uid,
			GID:    gid,
			Layout: LayoutData,
		})
	}
	return found
}

// ProcessStats sums up the processes of the linuxgsm user inside the container, measured the
// same way as on a host (ps).
func (d *DockerRuntime) ProcessStats(srv GameServerInstance) (float64, float64, []int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	_, err := d.client.Exec(ctx, srv.ID, DockerExecConfig{
		Cmd:  []string{"ps", "-u", dockerServerUser, "-o", "pid,%cpu,rss", "--no-headers"},
		User: "root",
	}, ExecIO{Stdout: &out})
	if err != nil {
		return 0, 0, nil
	}
	return parsePsStats(&out)
}

// RunLGSM runs ./<script> in /app as the linuxgsm user. A start or restart of a server whose
// container is stopped starts the container instead; its entrypoint then starts the server.
func (d *DockerRuntime) RunLGSM(ctx context.Context, srv GameServerInstance, stdio ExecIO, args ...string) error {
	if len(args) > 0 && (args[0] == "start" || args[0] == "restart") {
		state, err := d.client.ContainerState(ctx, srv.ID)
		if err == nil && !state.Running {
			if stdio.Stdout != nil {
				fmt.Fprintf(stdio.Stdout, "Container %s is %s, starting it. The container starts %s itself.\n", srv.ID, state.Status, srv.Script)
			}
			return d.client.StartContainer(ctx, srv.ID)
		}
	}
	cmd := append([]string{"./" + srv.Script}, args...)
	_, err := d.client.Exec(ctx, srv.ID, DockerExecConfig{Cmd: cmd, User: dockerServerUser, WorkingDir: dockerScriptDir}, stdio)
	return err
}

// RunAsUser runs argv as the linuxgsm user inside the container. Paths below the server
// directory are translated to /data, so callers can pass the paths they see.
func (d *DockerRuntime) RunAsUser(ctx context.Context, srv GameServerInstance, stdio ExecIO, login bool, args ...string) error {
	if len(args) == 0 {
		return &StartError{Err: fmt.Errorf("no command")}
	}
	cmd := make([]string, len(args))
	for i, a := range args {
		cmd[i] = srv.containerPath(a)
	}
	if login {
		quoted := make([]string, len(cmd))
		for i, a := range cmd {
			quoted[i] = ShellQuote(a)
		}
		cmd = []string{"bash", "-lc", strings.Join(quoted, " ")}
	}
	_, err := d.client.Exec(ctx, srv.ID, DockerExecConfig{Cmd: cmd, User: dockerServerUser, WorkingDir: containerDataDir}, stdio)
	return err
}

func (d *DockerRuntime) ValidateInstall(InstallRequest) error {
	return fmt.Errorf("installing game servers is %v", errDockerNotYet)
}

func (d *DockerRuntime) Install(context.Context, InstallRequest, func(string)) error {
	return errDockerNotYet
}

func (d *DockerRuntime) Delete(context.Context, GameServerInstance, bool) error {
	return errDockerNotYet
}

func (d *DockerRuntime) ReadCrontab(GameServerInstance) (string, error) {
	return "", errDockerNotYet
}

func (d *DockerRuntime) WriteCrontab(GameServerInstance, string) error {
	return errDockerNotYet
}

func (d *DockerRuntime) InstallAutostart(GameServerInstance) error {
	return errDockerNotYet
}
