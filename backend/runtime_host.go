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

// ProcessStats sums up all processes of the server's Linux user.
func (HostRuntime) ProcessStats(srv GameServerInstance) (cpu float64, ram float64, pids []int) {
	return GetProcessStatsForUser(srv.User)
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
			UID:    owner.UID,
			GID:    owner.GID,
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
	if err := os.Remove(scriptPath); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "runuser", "-u", owner.Name, "--", "cp", "-p", "--", target, scriptPath).Run()
	_ = exec.CommandContext(ctx, "runuser", "-u", owner.Name, "--", "chmod", "0755", "--", scriptPath).Run()
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
	if resolved, err := filepath.EvalSymlinks(parent); err == nil && IsForbiddenServerDir(filepath.ToSlash(filepath.Join(resolved, filepath.Base(dir)))) {
		return fmt.Errorf("installation directory points into a system directory")
	}
	return nil
}

func (HostRuntime) ValidateInstall(req InstallRequest) error {
	return ValidateInstallDir(req.Dir)
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
		}, func(line string) { logf(stripAnsi(line)) }, nil)
	}
	logLine := func(text string) {
		logf(text + "\n")
	}

	logLine(Msg(lang,
		"[INSTALL] Starting installation for "+gameCmd+" under system user '"+username+"'...",
		"[INSTALL] Starte Installation für "+gameCmd+" unter Systemuser '"+username+"'..."))

	homeDir := filepath.Join("/home", username)
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

	// Set password if provided
	if req.Password != "" {
		logLine(Msg(lang, "[INSTALL] Setting password for user...", "[INSTALL] Setze Passwort für Benutzer..."))
		chpasswdCmd := exec.CommandContext(ctx, "chpasswd")
		chpasswdCmd.Stdin = strings.NewReader(fmt.Sprintf("%s:%s\n", username, req.Password))
		if err := chpasswdCmd.Run(); err != nil {
			logLine(Msg(lang, "[WARNING] Failed to set password: ", "[WARNUNG] Passwort konnte nicht gesetzt werden: ") + err.Error())
		}
	}

	// 2. Download linuxgsm.sh bootstrap script (as the new user, into its own directory)
	logLine(Msg(lang, "[INSTALL] Downloading LinuxGSM bootstrap script...", "[INSTALL] Lade LinuxGSM Bootstrap-Skript herunter..."))
	bootstrapPath := filepath.Join(homeDir, "linuxgsm.sh")
	if err := runCommandWithLogs("runuser", "-u", username, "--", "wget", "-O", bootstrapPath, "https://linuxgsm.sh"); err != nil {
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

	// 3.5 Grant temporary sudo access for LinuxGSM dependency auto-installer
	sudoersFile := fmt.Sprintf("/etc/sudoers.d/lgsm-%s", username)
	sudoersContent := fmt.Sprintf("%s ALL=(ALL) NOPASSWD: ALL\n", username)
	if err := WriteFileAtomic(sudoersFile, []byte(sudoersContent), 0440); err != nil {
		logLine(Msg(lang, "[WARNING] Failed to configure temporary sudo permissions: ", "[WARNUNG] Sudo-Berechtigungen konnten nicht temporär konfiguriert werden: ") + err.Error())
	}
	defer func() {
		_ = os.Remove(sudoersFile)
	}()

	// 4. Run game auto-install: runuser -l <user> -c "cd '<dir>' && ./'<gameCmd>' auto-install"
	logLine(Msg(lang, "[INSTALL] Installing server via LinuxGSM (SteamCMD)...", "[INSTALL] Installiere Server via LinuxGSM (SteamCMD)..."))
	installCmd := fmt.Sprintf("cd %s && ./%s auto-install", ShellQuote(homeDir), ShellQuote(gameCmd))
	if err := runCommandWithLogs("runuser", "-l", username, "-c", installCmd); err != nil {
		logLine(Msg(lang, "[ERROR] Server installation failed: ", "[FEHLER] Server-Installation fehlgeschlagen: ") + err.Error())
		return err
	}
	logLine(Msg(lang, "[INSTALL] Installation completed successfully!", "[INSTALL] Installation erfolgreich abgeschlossen!"))
	return nil
}

// Delete removes the systemd unit and cron entries of a stopped server. A server with its own
// Linux user is removed together with the user and its home directory; on a shared user only
// the instance script and its config are removed.
func (h HostRuntime) Delete(ctx context.Context, srv GameServerInstance, sharedUser bool) error {
	// Remove systemd service if it exists (current and legacy naming)
	for _, unit := range []string{"linuxgsm-" + srv.Script, srv.ID} {
		if !IsValidScriptName(unit) {
			continue
		}
		servicePath := filepath.Join("/etc/systemd/system", unit+".service")
		if _, err := os.Stat(servicePath); err == nil {
			_ = exec.Command("systemctl", "stop", unit+".service").Run()
			_ = exec.Command("systemctl", "disable", unit+".service").Run()
			_ = os.Remove(servicePath)
			_ = exec.Command("systemctl", "daemon-reload").Run()
		}
	}

	// Remove crontab entries & files
	if sharedUser {
		// Shared user! Only remove this instance's cron lines, script and config.
		_ = removeCronLinesForScript(h, srv, nil)

		if p, err := ResolveInRoot(srv.Dir, srv.Script); err == nil {
			_ = os.Remove(p)
		}
		if p, err := ResolveInRoot(srv.Dir, filepath.Join("lgsm", "config-lgsm", srv.Script)); err == nil && filepath.Base(p) == srv.Script {
			_ = os.RemoveAll(p)
		}
		return nil
	}

	_ = exec.Command("crontab", "-u", srv.User, "-r").Run()

	// Exclusive user! Wipe everything by deleting user and home folder
	_ = exec.Command("pkill", "-u", srv.User).Run()
	time.Sleep(500 * time.Millisecond)
	_ = exec.Command("pkill", "-9", "-u", srv.User).Run()

	if err := exec.Command("userdel", "-r", srv.User).Run(); err != nil {
		fmt.Printf("[ERROR] Failed to delete user %s: %v\n", srv.User, err)
		return fmt.Errorf("failed to delete user")
	}
	return nil
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

// InstallAutostart writes and enables the systemd unit linuxgsm-<script>.service.
func (HostRuntime) InstallAutostart(srv GameServerInstance) error {
	if !IsSafeServerDir(srv.Dir) || !IsValidScriptName(srv.Script) || !IsValidLinuxUsername(srv.User) {
		return fmt.Errorf("server cannot be installed as systemd service")
	}
	// The description comes from game server output: strip anything systemd could interpret.
	desc := strings.Map(func(r rune) rune {
		if r == '%' || r == '\\' {
			return -1
		}
		return r
	}, sanitizeDisplayText(srv.Name, 100))

	scriptPath := scriptPathOf(srv)
	systemdCode := fmt.Sprintf(`[Unit]
Description=LinuxGSM %s Server
After=network.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s
ExecStart=%s start
ExecStop=%s stop
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, desc, srv.User, srv.Dir, scriptPath, scriptPath)

	unit := fmt.Sprintf("linuxgsm-%s.service", srv.Script)
	servicePath := filepath.Join("/etc/systemd/system", unit)
	if err := WriteFileAtomic(servicePath, []byte(systemdCode), 0644); err != nil {
		return err
	}

	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "enable", unit).Run()

	return nil
}
