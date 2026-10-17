package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// DockerRuntime manages game servers that run as containers of the official LinuxGSM images
// (gameservermanagers/gameserver). This is the foundation: it connects to the engine and finds
// the managed containers; commands, installation and scheduling follow.
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
	return dockerInstances(containers, d.dataDir), nil
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
		found = append(found, &GameServerInstance{
			ID:     id,
			Name:   mapScriptToGameName(script),
			User:   "linuxgsm",
			Script: script,
			Dir:    dir,
			Status: status,
			Game:   getGameFromScriptName(script),
			UID:    -1,
			GID:    -1,
		})
	}
	return found
}

func (d *DockerRuntime) ProcessStats(GameServerInstance) (float64, float64, []int) {
	return 0, 0, nil
}

func (d *DockerRuntime) RunLGSM(ctx context.Context, srv GameServerInstance, stdio ExecIO, args ...string) error {
	return &StartError{Err: errDockerNotYet}
}

func (d *DockerRuntime) RunAsUser(ctx context.Context, srv GameServerInstance, stdio ExecIO, login bool, args ...string) error {
	return &StartError{Err: errDockerNotYet}
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
