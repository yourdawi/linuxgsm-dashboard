package backend

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// HostRuntime runs LinuxGSM directly on this machine. Every game server belongs to a Linux user;
// commands run as that user through runuser, schedules live in the user's crontab and the
// autostart is a systemd unit. The dashboard needs root privileges for this.
type HostRuntime struct{}

func (HostRuntime) Name() string { return "host" }

func (HostRuntime) Capabilities() Capabilities {
	return Capabilities{
		Runtime:          "host",
		LinuxUsers:       true,
		CustomInstallDir: true,
		ServerPaths:      true,
		MultiInstance:    true,
		Cron:             true,
		Autostart:        true,
		Firewall:         true,
		SelfUpdate:       true,
	}
}

// Init removes sudoers files left behind by an interrupted installation.
func (HostRuntime) Init() {
	cleanupLeftoverSudoers()
}

// -------------------------------------------------------------
// Commands
// -------------------------------------------------------------

// runHostCmd connects cmd to stdio and runs it. Start failures are reported as *StartError.
func runHostCmd(cmd *exec.Cmd, stdio ExecIO) error {
	cmd.Stdin = stdio.Stdin
	cmd.Stdout = stdio.Stdout
	cmd.Stderr = stdio.Stderr
	if err := cmd.Start(); err != nil {
		return &StartError{Err: err}
	}
	return cmd.Wait()
}

// systemdRunPath can be overridden in tests.
var systemdRunPath = "systemd-run"

// outsideServiceSandbox returns name and args wrapped in systemd-run when the dashboard runs as a
// systemd service. The service has ProtectSystem=true, which makes /usr read-only for every
// process it starts, so LinuxGSM's auto-install could not install missing packages (e.g. Java)
// with its temporary sudo. A transient unit has its own, unrestricted mount namespace and cgroup.
// Outside of a service (INVOCATION_ID unset) or without systemd-run the command is unchanged.
func outsideServiceSandbox(name string, args ...string) (string, []string) {
	if os.Getenv("INVOCATION_ID") == "" {
		return name, args
	}
	runner, err := exec.LookPath(systemdRunPath)
	if err != nil {
		return name, args
	}
	wrapped := []string{"--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
		"--description=LinuxGSM Dashboard: game server installation", "--", name}
	return runner, append(wrapped, args...)
}

// lgsmShellCommand builds the shell snippet executed through `runuser -l <user> -c`.
// Every interpolated value is single-quoted; values are additionally validated at discovery.
func lgsmShellCommand(srv GameServerInstance, args ...string) string {
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(ShellQuote(srv.Dir))
	b.WriteString(" && ./")
	b.WriteString(ShellQuote(srv.Script))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(ShellQuote(a))
	}
	return b.String()
}

// RunLGSM runs the LinuxGSM script of srv as its Linux user in a login shell.
func (HostRuntime) RunLGSM(ctx context.Context, srv GameServerInstance, stdio ExecIO, args ...string) error {
	return runHostCmd(exec.CommandContext(ctx, "runuser", "-l", srv.User, "-c", lgsmShellCommand(srv, args...)), stdio)
}

// RunAsUser runs argv as the Linux user of srv, either directly or through a login shell.
func (HostRuntime) RunAsUser(ctx context.Context, srv GameServerInstance, stdio ExecIO, login bool, args ...string) error {
	if len(args) == 0 {
		return &StartError{Err: fmt.Errorf("no command")}
	}
	if login {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = ShellQuote(a)
		}
		return runHostCmd(exec.CommandContext(ctx, "runuser", "-l", srv.User, "-c", strings.Join(quoted, " ")), stdio)
	}
	return runHostCmd(exec.CommandContext(ctx, "runuser", append([]string{"-u", srv.User, "--"}, args...)...), stdio)
}

// ProcessStats measures the processes in the server's tmux session, so several servers under one
// Linux user do not count each other.
func (h HostRuntime) ProcessStats(srv GameServerInstance) (cpu float64, ram float64, pids []int) {
	return procStats.measure(srv.ID, func() []procSample {
		panes := tmuxPanePIDs(h, srv)
		if len(panes) == 0 {
			return nil
		}
		return processTree(readHostProcesses(), panes)
	})
}

// -------------------------------------------------------------
// Discovery
// -------------------------------------------------------------

// passwdPath can be overridden in tests.
var passwdPath = "/etc/passwd"

type passwdEntry struct {
	Name string
	UID  int
	GID  int
	Home string
}

func readPasswd() ([]passwdEntry, error) {
	file, err := os.Open(passwdPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []passwdEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ":")
		if len(parts) < 6 {
			continue
		}
		uid, err1 := strconv.Atoi(parts[2])
		gid, err2 := strconv.Atoi(parts[3])
		if err1 != nil || err2 != nil {
			continue
		}
		entries = append(entries, passwdEntry{Name: parts[0], UID: uid, GID: gid, Home: parts[5]})
	}
	return entries, scanner.Err()
}

// isDiscoverableHome decides whether a passwd home directory is scanned for LinuxGSM scripts:
// every home below /home (previous behaviour) and the homes of regular users (UID >= 1000)
// located anywhere else, e.g. on a separate SSD.
func isDiscoverableHome(e passwdEntry) bool {
	if e.UID == 0 || !IsValidLinuxUsername(e.Name) || !IsSafeServerDir(e.Home) {
		return false
	}
	if strings.HasPrefix(e.Home, "/home/") {
		return true
	}
	return e.UID >= 1000 && e.UID < 65534
}

// Discover scans the home directories from /etc/passwd and the additional server directories.
func (HostRuntime) Discover(customPaths []string) ([]*GameServerInstance, error) {
	users, err := readPasswd()
	if err != nil {
		return nil, fmt.Errorf("reading /etc/passwd: %w", err)
	}

	var found []*GameServerInstance
	taken := make(map[string]bool)
	seenScripts := make(map[string]bool)

	add := func(inst *GameServerInstance) {
		key := filepath.Join(inst.Dir, inst.Script)
		if seenScripts[key] {
			return
		}
		seenScripts[key] = true
		id := inst.Script
		if taken[id] {
			id = inst.Script + "@" + inst.User
			for n := 2; taken[id]; n++ {
				id = fmt.Sprintf("%s@%s-%d", inst.Script, inst.User, n)
			}
		}
		inst.ID = id
		taken[id] = true
		found = append(found, inst)
	}

	// 1. Home directories of system users
	byUID := make(map[int]passwdEntry)
	for _, u := range users {
		if _, exists := byUID[u.UID]; !exists {
			byUID[u.UID] = u
		}
		if !isDiscoverableHome(u) {
			continue
		}
		for _, inst := range scanServerDir(u.Home, u, false) {
			add(inst)
		}
	}

	// 2. Additional server directories configured by an administrator. Each configured directory
	// and its direct sub directories are scanned. The owner of the directory determines the Linux
	// user the server runs as; root owned directories are never used.
	for _, base := range customPaths {
		candidates := []string{base}
		if entries, err := os.ReadDir(base); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					candidates = append(candidates, filepath.Join(base, e.Name()))
				}
			}
		}
		for _, dir := range candidates {
			dir = filepath.ToSlash(filepath.Clean(dir))
			if !IsSafeServerDir(dir) {
				continue
			}
			uid, err := lfileOwnerUID(dir)
			if err != nil || uid <= 0 {
				continue
			}
			owner, ok := byUID[uid]
			if !ok || !IsValidLinuxUsername(owner.Name) {
				continue
			}
			for _, inst := range scanServerDir(dir, owner, true) {
				add(inst)
			}
		}
	}
	return found, nil
}

// scanServerDir detects LinuxGSM scripts in dir. When requireOwner is set, scripts must be owned
// by the given user (used for administrator configured directories).
func scanServerDir(dir string, owner passwdEntry, requireOwner bool) []*GameServerInstance {
	var found []*GameServerInstance
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		scriptName := entry.Name()
		if scriptName == "linuxgsm.sh" || !IsValidScriptName(scriptName) {
			continue
		}
		scriptPath := filepath.Join(dir, scriptName)

		// If script is a symlink to another LinuxGSM script, convert it to a real copy.
		// LinuxGSM resolves symlinks via `readlink -f $0` or `realpath`, which causes
		// symlinked instances to execute the root server instead of the clone.
		if entry.Type()&os.ModeSymlink != 0 {
			convertScriptSymlink(dir, scriptPath, owner)
		}

		info, err := os.Lstat(scriptPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}

		// Must be executable
		if info.Mode()&0111 == 0 {
			continue
		}

		if requireOwner {
			if uid, err := lfileOwnerUID(scriptPath); err != nil || uid != owner.UID {
				continue
			}
		}

		// Must be a LinuxGSM script (shebang + comment check)
		if !isLinuxGSMScript(scriptPath) {
			continue
		}

		inst := &GameServerInstance{
			ID:     scriptName,
			Name:   mapScriptToGameName(scriptName),
			User:   owner.Name,
			Script: scriptName,
			Dir:    dir,
			Status: "stopped",
			Port:   parseServerPort(dir, filepath.Join(dir, "lgsm", "config-lgsm"), scriptName),
			Game:   getGameFromScriptName(scriptName),
			Commands: linuxGSMCommands(lgsmCfgValuesAt(dir, filepath.Join(dir, "lgsm", "config-lgsm"), scriptName),
				shortnameOfScript(dir, scriptName)),
			UID: owner.UID,
			GID: owner.GID,
		}
		if isTmuxSessionActive(HostRuntime{}, *inst) {
			inst.Status = "running"
		}
		found = append(found, inst)
	}
	return found
}

// convertScriptSymlink replaces a symlinked instance script by a copy of its target. The target
// must be a LinuxGSM script inside the same directory and owned by the same user; the copy is
// made with the user's privileges, so a malicious link can never make root copy foreign files.
func convertScriptSymlink(dir, scriptPath string, owner passwdEntry) {
	target, err := filepath.EvalSymlinks(scriptPath)
	if err != nil {
		return
	}
	dirResolved, err := filepath.EvalSymlinks(dir)
	if err != nil || !IsWithin(dirResolved, target) || !isLinuxGSMScript(target) {
		return
	}
	if uid, err := fileOwnerUID(target); err != nil || (uid >= 0 && uid != owner.UID) {
		return
	}
	// The copy is made next to the link and renamed over it, so the link stays when copying fails.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tmp := scriptPath + ".lgsm-dashboard-copy"
	run := func(args ...string) error {
		return exec.CommandContext(ctx, "runuser", append([]string{"-u", owner.Name, "--"}, args...)...).Run()
	}
	if run("cp", "-p", "--", target, tmp) != nil || run("chmod", "0755", "--", tmp) != nil || run("mv", "-f", "-T", "--", tmp, scriptPath) != nil {
		_ = run("rm", "-f", "--", tmp)
	}
}

func cleanupLeftoverSudoers() {
	files, err := filepath.Glob("/etc/sudoers.d/lgsm-*")
	if err == nil {
		for _, f := range files {
			_ = os.Remove(f)
		}
	}
}

// -------------------------------------------------------------
// Installation and deletion
// -------------------------------------------------------------

// ValidateInstallDir checks an optional custom installation directory for a new server. The
// directory itself must not exist yet (useradd creates it), its parent must exist.
func ValidateInstallDir(dir string) error {
	if dir == "" {
		return nil
	}
	if !IsSafeServerDir(dir) {
		return fmt.Errorf("invalid installation directory: use an absolute path (letters, digits, / . _ - + @) outside of system directories")
	}
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("installation directory already exists")
	}
	parent := filepath.Dir(dir)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("parent directory %s does not exist", parent)
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("parent directory %s does not exist", parent)
	}
	if IsForbiddenServerDir(filepath.ToSlash(filepath.Join(resolved, filepath.Base(dir)))) {
		return fmt.Errorf("installation directory points into a system directory")
	}
	// Inside another user's tree that user could swap the new home for a symlink later.
	if err := checkTrustedAncestors(resolved); err != nil {
		return fmt.Errorf("installation directory cannot be used: %v", err)
	}
	// The uninstaller removes the dashboard directory with everything in it.
	if exe, err := os.Executable(); err == nil {
		if exeDir, err := filepath.EvalSymlinks(filepath.Dir(exe)); err == nil && IsWithin(exeDir, resolved) {
			return fmt.Errorf("installation directory is inside the dashboard directory %s", exeDir)
		}
	}
	return nil
}

func (HostRuntime) ValidateInstall(req InstallRequest) error {
	if existing, err := existingInstallUser(req); err != nil || existing != nil {
		return err
	}
	return ValidateInstallDir(req.Dir)
}

// existingInstallUser returns the passwd entry when the Linux user of an installation request
// already exists, e.g. left over from an earlier LinuxGSM setup. Such a user is reused if it is a
// regular user whose home directory is missing, holds only dotfiles or already holds LinuxGSM; it
// returns nil, nil when the user does not exist yet.
func existingInstallUser(req InstallRequest) (*passwdEntry, error) {
	users, err := readPasswd()
	if err != nil {
		return nil, fmt.Errorf("reading /etc/passwd: %w", err)
	}
	var entry *passwdEntry
	for i := range users {
		if users[i].Name == req.Username {
			entry = &users[i]
			break
		}
	}
	if entry == nil {
		return nil, nil
	}
	if entry.UID < 1000 || entry.UID >= 65534 {
		return nil, fmt.Errorf("user %s already exists and is a system account", req.Username)
	}
	if !IsSafeServerDir(entry.Home) || IsForbiddenServerDir(entry.Home) {
		return nil, fmt.Errorf("user %s already exists, but its home directory %s cannot be used", req.Username, entry.Home)
	}
	if req.Dir != "" && req.Dir != entry.Home {
		return nil, fmt.Errorf("user %s already exists with the home directory %s: leave the installation directory empty or use that directory", req.Username, entry.Home)
	}
	info, err := os.Lstat(entry.Home)
	if os.IsNotExist(err) {
		return entry, nil
	}
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("user %s already exists, but its home directory %s is not a directory", req.Username, entry.Home)
	}
	if uid, err := lfileOwnerUID(entry.Home); err != nil || (uid >= 0 && uid != entry.UID) {
		return nil, fmt.Errorf("user %s already exists, but its home directory %s belongs to another user", req.Username, entry.Home)
	}
	if !wasCreatedByDashboard(entry.Name, entry.UID) && isAdminAccount(entry.Name, entry.Home) {
		return nil, fmt.Errorf("user %s already exists and is an administrator or login account (sudo group or SSH keys): choose another user name", req.Username)
	}
	entries, err := os.ReadDir(entry.Home)
	if err != nil {
		return nil, fmt.Errorf("user %s already exists, but its home directory %s cannot be read", req.Username, entry.Home)
	}
	// An existing ./<game> is taken over by auto-install, so it must be LinuxGSM and stopped.
	script := filepath.Join(entry.Home, req.GameCmd)
	if info, err := os.Lstat(script); err == nil {
		if !info.Mode().IsRegular() || !isLinuxGSMScript(script) {
			return nil, fmt.Errorf("user %s already exists and %s is not a LinuxGSM script", req.Username, script)
		}
		srv := GameServerInstance{User: entry.Name, Script: req.GameCmd, Dir: entry.Home, UID: entry.UID, GID: entry.GID}
		if isTmuxSessionActive(HostRuntime{}, srv) {
			return nil, fmt.Errorf("user %s already exists and its server %s is running: stop it before installing", req.Username, req.GameCmd)
		}
	}
	// All scripts in a directory share serverfiles/, so a different game must not be added.
	gameRoot := strings.Split(req.GameCmd, "-")[0]
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && name != "linuxgsm.sh" && IsValidScriptName(name) && strings.Split(name, "-")[0] != gameRoot && isLinuxGSMScript(filepath.Join(entry.Home, name)) {
			return nil, fmt.Errorf("user %s already exists and its home directory %s holds the LinuxGSM server %s", req.Username, entry.Home, name)
		}
	}
	for _, e := range entries {
		if e.Name() == "lgsm" || e.Name() == "linuxgsm.sh" {
			return entry, nil
		}
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			return nil, fmt.Errorf("user %s already exists and its home directory %s contains other files", req.Username, entry.Home)
		}
	}
	return entry, nil
}

// Install creates a Linux user, bootstraps LinuxGSM and installs the game server. The server is
// installed into req.Dir, or /home/<username> when it is empty.
func (HostRuntime) Install(ctx context.Context, req InstallRequest, logf func(string)) error {
	lang := req.Lang
	username := req.Username
	gameCmd := req.GameCmd

	runCommandWithLogs := func(name string, args ...string) error {
		return streamLines(func(w io.Writer) error {
			return runHostCmd(exec.CommandContext(ctx, name, args...), ExecIO{Stdout: w, Stderr: w})
		}, func(line string) { logf(line) }, nil)
	}
	logLine := func(text string) {
		logf(text + "\n")
	}

	logLine(Msg(lang,
		"[INSTALL] Starting installation for "+gameCmd+" under system user '"+username+"'...",
		"[INSTALL] Starte Installation für "+gameCmd+" unter Systemuser '"+username+"'..."))

	existing, err := existingInstallUser(req)
	if err != nil {
		logLine(Msg(lang, "[ERROR] ", "[FEHLER] ") + err.Error())
		return err
	}

	homeDir := filepath.Join("/home", username)
	if existing != nil {
		// 1. Reuse the Linux user left over from an earlier installation
		homeDir = existing.Home
		logLine(Msg(lang,
			"[INSTALL] System user '"+username+"' already exists, using it with the home directory "+homeDir,
			"[INSTALL] Systembenutzer '"+username+"' existiert bereits, verwende ihn mit dem Home-Verzeichnis "+homeDir))
		if _, err := os.Lstat(homeDir); os.IsNotExist(err) {
			if err := os.Mkdir(homeDir, 0750); err != nil {
				logLine(Msg(lang, "[ERROR] Failed to create the home directory: ", "[FEHLER] Home-Verzeichnis konnte nicht erstellt werden: ") + err.Error())
				return err
			}
			if err := os.Chown(homeDir, existing.UID, existing.GID); err != nil {
				_ = os.Remove(homeDir)
				logLine(Msg(lang, "[ERROR] Failed to hand the home directory over to the user: ", "[FEHLER] Home-Verzeichnis konnte dem Benutzer nicht übergeben werden: ") + err.Error())
				return err
			}
		}
	} else {
		useraddArgs := []string{"-m", "-s", "/bin/bash"}
		if req.Dir != "" {
			homeDir = req.Dir
			useraddArgs = append(useraddArgs, "-d", req.Dir)
			logLine(Msg(lang, "[INSTALL] Installation directory: "+req.Dir, "[INSTALL] Installationsverzeichnis: "+req.Dir))
		}
		useraddArgs = append(useraddArgs, "--", username)

		// 1. Create Linux user
		logLine(Msg(lang, "[INSTALL] Creating system user...", "[INSTALL] Lege Systembenutzer an..."))
		if err := runCommandWithLogs("useradd", useraddArgs...); err != nil {
			logLine(Msg(lang, "[ERROR] Failed to create user: ", "[FEHLER] Benutzer konnte nicht erstellt werden: ") + err.Error())
			return err
		}
		// Only users the dashboard created are ever deleted together with their home.
		recordCreatedUser(username)
		// useradd leaves the home 0755 on Debian 12 and older Ubuntu; other server users must not
		// read this one's configs and secrets (LinuxGSM accepts 7xx).
		if err := os.Chmod(homeDir, 0750); err != nil {
			logLine(Msg(lang, "[WARNING] Could not restrict the home directory: ", "[WARNUNG] Home-Verzeichnis konnte nicht eingeschränkt werden: ") + err.Error())
		}
	}

	// Set the password of a new user. An existing user keeps its password: it may be a login
	// somebody uses.
	if req.Password != "" && existing != nil {
		logLine(Msg(lang, "[INSTALL] The user already exists, its password is not changed.",
			"[INSTALL] Der Benutzer existiert bereits, sein Passwort wird nicht geändert."))
	} else if req.Password != "" {
		logLine(Msg(lang, "[INSTALL] Setting password for user...", "[INSTALL] Setze Passwort für Benutzer..."))
		chpasswdCmd := exec.CommandContext(ctx, "chpasswd")
		chpasswdCmd.Stdin = strings.NewReader(fmt.Sprintf("%s:%s\n", username, req.Password))
		if err := chpasswdCmd.Run(); err != nil {
			logLine(Msg(lang, "[WARNING] Failed to set password: ", "[WARNUNG] Passwort konnte nicht gesetzt werden: ") + err.Error())
		}
	}

	// An existing ./<gameCmd> is used as is: the bootstrap would add a second instance <gameCmd>-2.
	if existing != nil && isLinuxGSMScript(filepath.Join(homeDir, gameCmd)) {
		logLine(Msg(lang,
			"[INSTALL] The LinuxGSM script "+gameCmd+" already exists, skipping the bootstrap.",
			"[INSTALL] Das LinuxGSM Skript "+gameCmd+" existiert bereits, überspringe das Bootstrap."))
	} else {
		// 2. Download linuxgsm.sh bootstrap script (as the new user, into its own directory)
		logLine(Msg(lang, "[INSTALL] Downloading LinuxGSM bootstrap script...", "[INSTALL] Lade LinuxGSM Bootstrap-Skript herunter..."))
		bootstrapPath := filepath.Join(homeDir, "linuxgsm.sh")
		if err := runCommandWithLogs("runuser", "-u", username, "--", "curl", "-fsSL", "-o", bootstrapPath, "https://linuxgsm.sh"); err != nil {
			logLine(Msg(lang, "[ERROR] Failed to download bootstrap script: ", "[FEHLER] Bootstrap-Skript konnte nicht geladen werden: ") + err.Error())
			return err
		}

		// Make executable
		if err := exec.CommandContext(ctx, "runuser", "-u", username, "--", "chmod", "+x", "--", bootstrapPath).Run(); err != nil {
			logLine(Msg(lang, "[ERROR] Failed to set permissions: ", "[FEHLER] Berechtigungskonfiguration schlug fehl: ") + err.Error())
			return err
		}

		// 3. Run bootstrap: runuser -l <user> -c "cd '<dir>' && ./linuxgsm.sh '<gameCmd>'"
		logLine(Msg(lang, "[INSTALL] Initializing LinuxGSM script...", "[INSTALL] Initialisiere LinuxGSM Skript..."))
		bootstrapCmd := fmt.Sprintf("cd %s && ./linuxgsm.sh %s", ShellQuote(homeDir), ShellQuote(gameCmd))
		if err := runCommandWithLogs("runuser", "-l", username, "-c", bootstrapCmd); err != nil {
			logLine(Msg(lang, "[ERROR] Script initialization failed: ", "[FEHLER] Skriptinitialisierung schlug fehl: ") + err.Error())
			return err
		}
	}

	// 3.5 Install the packages LinuxGSM needs as root; the game server user gets no sudo.
	logLine(Msg(lang, "[INSTALL] Checking dependencies...", "[INSTALL] Prüfe Abhängigkeiten..."))
	installDependencies(ctx, gameCmd, usesSteamCMD(ctx, gameCmd), lang, logf)

	if req.Steam.User != "" {
		uid, gid, err := fileOwnerIDs(homeDir)
		if err == nil {
			err = seedSteamLogin(filepath.Join(homeDir, "lgsm", "config-lgsm", gameCmd), req.Steam, uid, gid)
		}
		if err != nil {
			logLine(Msg(lang, "[ERROR] Failed to store the Steam login: ", "[FEHLER] Steam-Login konnte nicht gespeichert werden: ") + err.Error())
			return err
		}
		logLine(Msg(lang, "[INSTALL] Steam login stored in secrets-common.cfg.", "[INSTALL] Steam-Login in secrets-common.cfg gespeichert."))
	}

	// 4. Run game auto-install: runuser -l <user> -c "cd '<dir>' && ./'<gameCmd>' auto-install"
	logLine(Msg(lang, "[INSTALL] Installing server via LinuxGSM (SteamCMD)...", "[INSTALL] Installiere Server via LinuxGSM (SteamCMD)..."))
	installCmd := fmt.Sprintf("cd %s && ./%s auto-install", ShellQuote(homeDir), ShellQuote(gameCmd))
	installName, installArgs := outsideServiceSandbox("runuser", "-l", username, "-c", installCmd)
	if err := runCommandWithLogs(installName, installArgs...); err != nil {
		logLine(Msg(lang, "[ERROR] Server installation failed: ", "[FEHLER] Server-Installation fehlgeschlagen: ") + err.Error())
		return err
	}
	logLine(Msg(lang, "[INSTALL] Installation completed successfully!", "[INSTALL] Installation erfolgreich abgeschlossen!"))
	return nil
}

// Delete removes the systemd unit and cron entries of a stopped server. A dedicated Linux user
// (see isDedicatedServerUser) that hosts only this server is removed together with its home
// directory. Otherwise only this instance is removed, and the LinuxGSM files of its directory
// once no other instance is left there; the Linux user is kept.
func (h HostRuntime) Delete(ctx context.Context, srv GameServerInstance, sharedUser bool) error {
	// Remove the autostart unit (current and legacy naming), only if it runs this server.
	removeAutostart(srv)

	if sharedUser || !mayDeleteLinuxUser(srv) {
		// Keep the Linux user: it hosts other servers or is not a dedicated game server user
		// (for example a personal account that LinuxGSM was installed into).
		_ = removeCronLinesForScript(h, srv, nil)
		return removeInstanceFiles(srv)
	}

	_ = exec.Command("crontab", "-u", srv.User, "-r").Run()

	// Exclusive user! Wipe everything by deleting user and home folder
	_ = exec.Command("pkill", "-u", srv.User).Run()
	time.Sleep(500 * time.Millisecond)
	_ = exec.Command("pkill", "-9", "-u", srv.User).Run()
	removeUserShm(srv.UID)

	if err := exec.Command("userdel", "-r", srv.User).Run(); err != nil {
		fmt.Printf("[ERROR] Failed to delete user %s: %v\n", srv.User, err)
		return fmt.Errorf("failed to delete user")
	}
	forgetCreatedUser(srv.User)
	return nil
}

// linuxGSMRootEntries are the entries LinuxGSM creates in its root directory, next to the scripts.
var linuxGSMRootEntries = map[string]bool{
	"linuxgsm.sh": true, "lgsm": true, "log": true, "serverfiles": true,
	"backup": true, "backups": true, "public_html": true,
}

// isDedicatedServerUser reports whether the Linux user of srv exists only for LinuxGSM: a regular
// user whose home directory is the server directory and holds nothing but dotfiles and LinuxGSM.
// Only such a user may be deleted together with its home directory.
func isDedicatedServerUser(srv GameServerInstance) bool {
	users, err := readPasswd()
	if err != nil {
		return false
	}
	var entry *passwdEntry
	for i := range users {
		if users[i].Name == srv.User {
			entry = &users[i]
			break
		}
	}
	if entry == nil || entry.UID < 1000 || entry.UID >= 65534 || entry.UID != srv.UID {
		return false
	}
	if filepath.Clean(entry.Home) != filepath.Clean(srv.Dir) {
		return false
	}
	entries, err := os.ReadDir(srv.Dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || linuxGSMRootEntries[name] {
			continue
		}
		if e.Type().IsRegular() && IsValidScriptName(name) && isLinuxGSMScript(filepath.Join(srv.Dir, name)) {
			continue
		}
		return false
	}
	return true
}

// removeInstanceFiles removes the script and the instance configs of srv. When no other LinuxGSM
// instance is left in its directory, the shared LinuxGSM files (serverfiles, lgsm, log and the
// bootstrap script) are removed as well. Symlinks are removed, never followed.
func removeInstanceFiles(srv GameServerInstance) error {
	root, err := filepath.EvalSymlinks(srv.Dir)
	if err != nil {
		return fmt.Errorf("server directory not found")
	}
	remove := func(rel string, all bool) {
		p := filepath.Join(root, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return
		}
		// Every parent must be a real directory inside the server directory.
		if parent, err := filepath.EvalSymlinks(filepath.Dir(p)); err != nil || parent != filepath.Dir(p) {
			return
		}
		if all && info.IsDir() {
			_ = os.RemoveAll(p)
		} else {
			_ = os.Remove(p)
		}
	}

	cfgRel := srv.lgsmConfigRel()
	remove(filepath.Join(cfgRel, srv.Script+".cfg"), false)
	remove(filepath.Join(cfgRel, "secrets-"+srv.Script+".cfg"), false)
	if legacy := srv.configRel(srv.Script); legacy != cfgRel {
		// Instance config directory created by older dashboard versions; LinuxGSM never reads it.
		remove(legacy, true)
	}
	remove(filepath.Join(srv.lgsmDataRel(), srv.Script+".uid"), false)
	remove(srv.Script, false)

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if name != "linuxgsm.sh" && e.Type().IsRegular() && IsValidScriptName(name) && isLinuxGSMScript(filepath.Join(root, name)) {
			return nil // another instance still uses the shared files
		}
	}
	for _, name := range []string{"serverfiles", "lgsm", "linuxgsm.sh"} {
		remove(name, true)
	}
	if info, err := os.Lstat(filepath.Join(root, "log", "script")); err == nil && info.IsDir() {
		remove("log", true)
	}
	return nil
}

// Clone creates the LinuxGSM instance <root script>-<suffix> next to srv: an independent copy
// of the script with its own, empty instance config. Server files and the other configs are
// shared with srv, as with LinuxGSM's own multi-instance setup.
func (h HostRuntime) Clone(ctx context.Context, srv GameServerInstance, cleanSuffix string) (string, error) {
	baseScript := srv.Script
	rootScript := strings.Split(baseScript, "-")[0]

	newInstanceScript := fmt.Sprintf("%s-%s", rootScript, cleanSuffix)
	if !IsValidScriptName(newInstanceScript) {
		return "", fmt.Errorf("invalid instance name")
	}
	newScriptPath := filepath.Join(srv.Dir, newInstanceScript)
	rootScriptPath := filepath.Join(srv.Dir, rootScript)

	// If rootScriptPath doesn't exist, fallback to baseScript
	sourceScript := rootScript
	if info, err := os.Lstat(rootScriptPath); err != nil || !info.Mode().IsRegular() {
		sourceScript = baseScript
	}

	// If newScriptPath already exists as a symlink or file
	if linfo, err := os.Lstat(newScriptPath); err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			// Remove legacy symlink so it can be replaced with a real executable script
			_ = os.Remove(newScriptPath)
		} else {
			return "", fmt.Errorf("instance script %s already exists", newInstanceScript)
		}
	}

	// LinuxGSM instances MUST be independent copies of the script (not symlinks),
	// because LinuxGSM internally resolves symlinks via `readlink -f $0` or `realpath`,
	// which causes a symlinked script to run the base server instead of the clone.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var out strings.Builder
	stdio := ExecIO{Stdout: &out, Stderr: &out}
	err := h.RunAsUser(ctx, srv, stdio, true, "cp", "-p", "--", filepath.Join(srv.Dir, sourceScript), newScriptPath)
	if err == nil {
		err = h.RunAsUser(ctx, srv, stdio, true, "chmod", "0755", "--", newScriptPath)
	}
	if err != nil {
		fmt.Printf("[ERROR] Failed to create instance script %s: %s (%v)\n", newInstanceScript, out.String(), err)
		return "", fmt.Errorf("failed to create instance script")
	}

	initialConfig := fmt.Sprintf("## LinuxGSM Multi-Instance Config: %s\n## Generated by LinuxGSM Dashboard\n\n", newInstanceScript)

	// LinuxGSM loads the instance config from config-lgsm/<gameservername>/<instance>.cfg.
	rel := filepath.Join(srv.configRel(rootScript), newInstanceScript+".cfg")
	if resolved, err := ResolveInRoot(srv.Dir, rel); err == nil {
		if _, err := os.Lstat(resolved); os.IsNotExist(err) {
			_ = writeServerFile(srv, rel, []byte(initialConfig))
		}
	}

	return newInstanceScript, nil
}

// shmDir can be overridden in tests.
var shmDir = "/dev/shm"

// removeUserShm deletes the shared memory files of a Linux user whose processes have ended. Game
// servers keep files there with fixed names (TeamSpeak 3, for example); left behind, they stop a
// server of another user from starting later. Only regular files owned by uid are removed.
func removeUserShm(uid int) {
	if uid <= 0 {
		return
	}
	entries, err := os.ReadDir(shmDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(shmDir, e.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if owner, err := lfileOwnerUID(path); err == nil && owner == uid {
			if err := os.Remove(path); err != nil {
				fmt.Printf("[WARNING] Failed to remove %s: %v\n", path, err)
			}
		}
	}
}

// -------------------------------------------------------------
// Crontab and systemd
// -------------------------------------------------------------

func (HostRuntime) ReadCrontab(srv GameServerInstance) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "crontab", "-u", srv.User, "-l")
	var outBytes bytes.Buffer
	cmd.Stdout = &outBytes
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", nil
		}
		return "", err
	}
	return outBytes.String(), nil
}

func (HostRuntime) WriteCrontab(srv GameServerInstance, content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		_ = exec.CommandContext(ctx, "crontab", "-u", srv.User, "-r").Run()
		return nil
	}
	cmd := exec.CommandContext(ctx, "crontab", "-u", srv.User, "-")
	cmd.Stdin = strings.NewReader(trimmed + "\n")
	return cmd.Run()
}
