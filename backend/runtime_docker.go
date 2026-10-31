package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	image   string // image repository of the game servers, tagged with the LinuxGSM shortname

	scheduleDir string           // schedules of the servers (see docker_scheduler.go)
	timeZone    *timeZoneSetting // time zone of the schedules (see timezone.go)
}

// defaultGameServerImage is the repository of the official LinuxGSM game server images.
const defaultGameServerImage = "gameservermanagers/gameserver"

// dockerStopTimeout gives LinuxGSM time for a graceful stop on "docker stop" (stop mode 4 waits
// up to 120 seconds, plus the optional player warning countdown).
const dockerStopTimeout = 180

var reImageRepo = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(\.[a-z0-9-]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)+$`)

// NewDockerRuntime connects to the Docker engine (DOCKER_HOST or the default socket). dataDir is
// the server data directory (LGSM_DATA_DIR), empty for /srv/lgsm. The schedules are kept in
// configDir/schedules.
func NewDockerRuntime(dataDir, configDir string) (*DockerRuntime, error) {
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
	image := strings.TrimSpace(os.Getenv("LGSM_GAMESERVER_IMAGE"))
	if image == "" {
		image = defaultGameServerImage
	}
	if !reImageRepo.MatchString(image) {
		return nil, fmt.Errorf("invalid LGSM_GAMESERVER_IMAGE %q: use an image repository without tag, e.g. %s", image, defaultGameServerImage)
	}
	d := &DockerRuntime{client: client, engine: engine, dataDir: dataDir, image: image}
	if configDir != "" {
		d.scheduleDir = filepath.Join(configDir, "schedules")
	}
	d.timeZone = newTimeZoneSetting(configDir)
	return d, nil
}

func (d *DockerRuntime) Name() string { return "docker" }

func (d *DockerRuntime) Capabilities() Capabilities {
	return Capabilities{Runtime: "docker", MultiInstance: true, Cron: true, ScheduleTimeZone: true}
}

func (d *DockerRuntime) Init() {
	fmt.Printf("[SYS] Docker engine %s (API %s, %s/%s), server data in %s\n",
		d.engine.Version, d.engine.APIVersion, d.engine.Os, d.engine.Arch, d.dataDir)
	if info, err := os.Stat(d.dataDir); err != nil || !info.IsDir() {
		fmt.Printf("[WARNING] Server data directory %s does not exist. Mount it at the same path as on the host.\n", d.dataDir)
	}
	d.startScheduler()
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
		if !IsValidLocalServerID(id) || !IsValidScriptName(script) || !IsSafeServerDir(dir) || !IsWithin(dataDir, dir) || dir == dataDir {
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
			Commands: linuxGSMCommands(lgsmCfgValuesAt(dir, filepath.Join(dir, "config-lgsm"), script),
				strings.TrimSuffix(gameServerName(script), "server")),
			UID:    uid,
			GID:    gid,
			Layout: LayoutData,
		})
	}
	return found
}

// ProcessStats sums up the processes of the linuxgsm user inside the container, measured the
// same way as on a host (ps).
// QueryHosts adds the Docker host to the live query targets. Game servers use host networking;
// a dashboard on a bridge network reaches them through its default gateway. With host
// networking for the dashboard 127.0.0.1 already works.
func (d *DockerRuntime) QueryHosts() []string {
	if gw := defaultGateway(); gw != "" {
		return []string{gw}
	}
	return nil
}

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
			if err := d.client.StartContainer(ctx, srv.ID); err != nil {
				return err
			}
			d.followServerState(srv, args[0])
			return nil
		}
	}
	cmd := append([]string{"./" + srv.Script}, args...)
	_, err := d.client.Exec(ctx, srv.ID, DockerExecConfig{Cmd: cmd, User: dockerServerUser, WorkingDir: dockerScriptDir}, stdio)
	if len(args) > 0 && (err == nil || ExitCode(err) == 3) {
		d.followServerState(srv, args[0])
	}
	return err
}

// followServerState keeps the restart policy in line with what the user did: the entrypoint
// starts the server on every container start, so a stopped server would come back after a
// reboot. The container itself keeps running, LinuxGSM commands like backup need it.
func (d *DockerRuntime) followServerState(srv GameServerInstance, command string) {
	policy := ""
	switch command {
	case "stop":
		policy = "no"
	case "start", "restart":
		policy = "unless-stopped"
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.client.SetRestartPolicy(ctx, srv.ID, policy); err != nil {
		fmt.Printf("[WARNING] %s: could not set the restart policy to %s: %v\n", srv.ID, policy, err)
	}
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

// gameShortname returns the LinuxGSM shortname of a game script (vhserver -> vh), which is the
// tag of its image.
func gameShortname(gameCmd string) (string, bool) {
	short := strings.TrimSuffix(gameCmd, "server")
	if short == gameCmd || short == "" || strings.ContainsAny(short, "-.") || !IsValidScriptName(gameCmd) {
		return "", false
	}
	return short, true
}

// serverDir is the data directory of a new server.
func (d *DockerRuntime) serverDir(name string) string {
	return filepath.ToSlash(filepath.Join(d.dataDir, name))
}

// checkNewServer verifies that neither a container nor a data directory named name exists.
func (d *DockerRuntime) checkNewServer(ctx context.Context, name string) error {
	if _, err := os.Lstat(d.serverDir(name)); err == nil {
		return fmt.Errorf("data directory %s already exists", d.serverDir(name))
	}
	_, found, err := d.client.InspectContainer(ctx, name)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("a container named %s already exists", name)
	}
	return nil
}

// ValidateInstall checks the server name (taken from the user name field of the installer) and
// the game. The server always lives in <data dir>/<name>.
func (d *DockerRuntime) ValidateInstall(req InstallRequest) error {
	if req.Dir != "" {
		return fmt.Errorf("a custom installation directory is not available in Docker mode: servers are stored in %s/<name>", d.dataDir)
	}
	if !IsValidLinuxUsername(req.Username) {
		return fmt.Errorf("invalid server name")
	}
	if _, ok := gameShortname(req.GameCmd); !ok {
		return fmt.Errorf("invalid game")
	}
	if info, err := os.Stat(d.dataDir); err != nil || !info.IsDir() {
		return fmt.Errorf("server data directory %s does not exist in the dashboard container", d.dataDir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return d.checkNewServer(ctx, req.Username)
}

// containerModsDir is where LinuxGSM keeps the list of installed mods inside the official images
// (${lgsmdir}/mods, not configurable). It is not part of /data, so the dashboard mounts
// <server dir>/mods there: the list survives re-creating the container and the dashboard can
// read it.
const containerModsDir = dockerScriptDir + "/lgsm/mods"

// createServerContainer creates and starts the container of a game server whose data lives in
// dir. LinuxGSM's own update cron and the update on every container start are switched off: the
// dashboard runs updates.
func (d *DockerRuntime) createServerContainer(ctx context.Context, name, gameCmd, image, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "mods"), 0755); err != nil {
		return err
	}
	cfg := DockerCreateConfig{
		Image: image,
		Env:   []string{"UPDATE_CHECK=0", "UPDATE_ON_START=false"},
		Labels: map[string]string{
			dockerLabelManaged:    "true",
			dockerLabelGameServer: gameCmd,
			dockerLabelDir:        dir,
		},
		StopTimeout: dockerStopTimeout,
		HostConfig: DockerHostConfig{
			Binds:         []string{dir + ":" + containerDataDir, dir + "/mods:" + containerModsDir},
			NetworkMode:   "host",
			RestartPolicy: DockerRestartPolicy{Name: "unless-stopped"},
			// The entrypoint copies the whole game console to stdout; without rotation it fills the disk.
			LogConfig: &DockerLogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		},
	}
	if _, err := d.client.CreateContainer(ctx, name, cfg); err != nil {
		return err
	}
	return d.client.StartContainer(ctx, name)
}

// commonCfgSeed is written before the first start, so LinuxGSM keeps backups in the data volume
// instead of the container (LinuxGSM only creates common.cfg when it is missing).
const commonCfgSeed = `# LinuxGSM common config, created by the LinuxGSM Dashboard.
# Settings here apply to every instance of this server and override _default.cfg.

# Keep backups in the data volume, so they survive re-creating the container.
backupdir="/data/backup"

# Keep the FastDL files in the data volume, where the dashboard serves them under /fastdl/.
webdir="/data/public_html"
`

// Install pulls the image of the game, prepares the data directory, creates the container and
// follows its log until LinuxGSM has installed and started the server.
func (d *DockerRuntime) Install(ctx context.Context, req InstallRequest, logf func(string)) error {
	lang := req.Lang
	name, gameCmd := req.Username, req.GameCmd
	short, _ := gameShortname(gameCmd)
	image := d.image + ":" + short
	dir := d.serverDir(name)
	logLine := func(text string) { logf(text + "\n") }
	fail := func(en, de string, err error) error {
		logLine(Msg(lang, "[ERROR] "+en, "[FEHLER] "+de) + err.Error())
		return err
	}

	logLine(Msg(lang,
		"[INSTALL] Installing "+gameCmd+" as container '"+name+"' ("+image+")...",
		"[INSTALL] Installiere "+gameCmd+" als Container '"+name+"' ("+image+")..."))

	logLine(Msg(lang, "[INSTALL] Pulling the image...", "[INSTALL] Lade das Image..."))
	if err := d.client.PullImage(ctx, image, logf); err != nil {
		return fail("Failed to pull the image: ", "Image konnte nicht geladen werden: ", err)
	}

	logLine(Msg(lang, "[INSTALL] Server data: "+dir, "[INSTALL] Serverdaten: "+dir))
	cfgDir := filepath.Join(dir, "config-lgsm", gameCmd)
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return fail("Failed to create the data directory: ", "Datenverzeichnis konnte nicht angelegt werden: ", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "common.cfg"), []byte(commonCfgSeed), 0644); err != nil {
		return fail("Failed to write common.cfg: ", "common.cfg konnte nicht geschrieben werden: ", err)
	}

	logLine(Msg(lang, "[INSTALL] Creating and starting the container...", "[INSTALL] Erstelle und starte den Container..."))
	// A schedule left by an earlier server of this name must not run for the new one.
	_ = d.WriteCrontab(GameServerInstance{ID: name, Script: gameCmd, Dir: dir, Layout: LayoutData}, "")
	if err := d.createServerContainer(ctx, name, gameCmd, image, dir); err != nil {
		// Without a running container the directory is invisible and would block the name.
		_ = d.client.RemoveContainer(context.Background(), name, true)
		_ = os.RemoveAll(dir)
		return fail("Failed to create the container: ", "Container konnte nicht erstellt werden: ", err)
	}

	logLine(Msg(lang, "[INSTALL] Installing the server via LinuxGSM (container log)...", "[INSTALL] Installiere den Server via LinuxGSM (Container-Log)..."))
	ready := false
	err := d.client.FollowLogs(ctx, name, func(line string) bool {
		logf(stripAnsi(line))
		// The entrypoint prints this after install, start and details.
		if strings.Contains(line, "Tail log files") {
			ready = true
			return false
		}
		return true
	})
	if err != nil {
		return fail("Failed to read the container log: ", "Container-Log konnte nicht gelesen werden: ", err)
	}
	if !ready {
		state, _ := d.client.ContainerState(context.Background(), name)
		return fail("The container stopped during the installation: ", "Der Container wurde während der Installation beendet: ", fmt.Errorf("%s", state.Status))
	}

	srv := GameServerInstance{ID: name, Script: gameCmd, Dir: dir, Layout: LayoutData}
	if !isTmuxSessionActive(d, srv) {
		return fail("The server did not start, see the log above. ", "Der Server ist nicht gestartet, siehe Log oben. ", fmt.Errorf("tmux session missing"))
	}
	logLine(Msg(lang, "[INSTALL] Installation completed successfully!", "[INSTALL] Installation erfolgreich abgeschlossen!"))
	return nil
}

// Clone creates the container <id>-<suffix> with a copy of the server files, LinuxGSM configs and
// mods list of srv and its own, empty instance config, like a LinuxGSM multi-instance on a host. Ports
// therefore match the original until they are changed. Stop srv first for a consistent copy.
func (d *DockerRuntime) Clone(ctx context.Context, srv GameServerInstance, suffix string) (string, error) {
	name := srv.ID + "-" + suffix
	if !IsValidLocalServerID(name) || len(name) > 63 {
		return "", fmt.Errorf("invalid instance name")
	}
	if err := d.checkNewServer(ctx, name); err != nil {
		return "", err
	}
	info, found, err := d.client.InspectContainer(ctx, srv.ID)
	if err != nil || !found {
		return "", fmt.Errorf("source container not found")
	}

	dir := d.serverDir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create the data directory")
	}
	for _, sub := range []string{"serverfiles", "config-lgsm", "mods"} {
		src := filepath.Join(srv.Dir, sub)
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		if out, err := exec.CommandContext(ctx, "cp", "-a", src, dir+"/").CombinedOutput(); err != nil {
			fmt.Printf("[ERROR] Failed to copy %s to %s: %s (%v)\n", src, dir, strings.TrimSpace(string(out)), err)
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("failed to copy the server data")
		}
	}
	_ = os.RemoveAll(filepath.Join(dir, "mods", "tmp"))
	clone := GameServerInstance{ID: name, Script: srv.Script, Dir: dir, Layout: LayoutData, UID: srv.UID, GID: srv.GID}
	cfgDir := clone.configRel(srv.Script)
	_ = os.Remove(filepath.Join(dir, cfgDir, "secrets-"+srv.Script+".cfg"))
	initialConfig := fmt.Sprintf("## LinuxGSM Instance Config: %s (clone of %s)\n## Generated by LinuxGSM Dashboard\n\n", name, srv.ID)
	if err := writeServerFile(clone, filepath.Join(cfgDir, srv.Script+".cfg"), []byte(initialConfig)); err != nil {
		fmt.Printf("[WARNING] Failed to reset the instance config of %s: %v\n", name, err)
	}

	_ = d.WriteCrontab(clone, "")
	if err := d.createServerContainer(ctx, name, srv.Script, info.Config.Image, dir); err != nil {
		_ = d.client.RemoveContainer(context.Background(), name, true)
		_ = os.RemoveAll(dir)
		return "", err
	}
	return name, nil
}

// Delete removes the container and the data directory of a server.
func (d *DockerRuntime) Delete(ctx context.Context, srv GameServerInstance, _ bool) error {
	dir := filepath.ToSlash(filepath.Clean(srv.Dir))
	if !IsSafeServerDir(dir) || !IsWithin(d.dataDir, dir) || dir == d.dataDir {
		return fmt.Errorf("refusing to delete %s: not a server directory below %s", srv.Dir, d.dataDir)
	}
	if err := d.client.RemoveContainer(ctx, srv.ID, true); err != nil {
		return err
	}
	// The schedule goes first: it must not survive a failed removal of the data.
	if err := d.WriteCrontab(srv, ""); err != nil {
		fmt.Printf("[WARNING] Failed to remove the schedule of %s: %v\n", srv.ID, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("container removed, but deleting %s failed: %w", dir, err)
	}
	return nil
}

func (d *DockerRuntime) InstallAutostart(GameServerInstance) error {
	return errDockerNotYet
}
