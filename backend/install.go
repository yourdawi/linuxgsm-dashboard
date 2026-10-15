package backend

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

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

// InstallGame creates a Linux user, bootstraps LinuxGSM and installs the game server.
// installDir is optional; when empty the server is installed into /home/<username>.
func (im *InstanceManager) InstallGame(w http.ResponseWriter, r *http.Request, gameCmd, username, password, installDir, lang string) {
	if err := ValidateInstallDir(installDir); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	logLine := func(text string) {
		sse.log(text + "\n")
	}

	runCommandWithLogs := func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = cmd.Stdout

		if err := cmd.Start(); err != nil {
			return err
		}

		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				sse.log(stripAnsi(line))
			}
			if err != nil {
				break
			}
		}
		return cmd.Wait()
	}

	logLine(Msg(lang,
		"[INSTALL] Starting installation for "+gameCmd+" under system user '"+username+"'...",
		"[INSTALL] Starte Installation für "+gameCmd+" unter Systemuser '"+username+"'..."))

	homeDir := filepath.Join("/home", username)
	useraddArgs := []string{"-m", "-s", "/bin/bash"}
	if installDir != "" {
		homeDir = installDir
		useraddArgs = append(useraddArgs, "-d", installDir)
		logLine(Msg(lang, "[INSTALL] Installation directory: "+installDir, "[INSTALL] Installationsverzeichnis: "+installDir))
	}
	useraddArgs = append(useraddArgs, "--", username)

	// 1. Create Linux user
	logLine(Msg(lang, "[INSTALL] Creating system user...", "[INSTALL] Lege Systembenutzer an..."))
	if err := runCommandWithLogs("useradd", useraddArgs...); err != nil {
		logLine(Msg(lang, "[ERROR] Failed to create user: ", "[FEHLER] Benutzer konnte nicht erstellt werden: ") + err.Error())
		sse.exit(1)
		return
	}

	// Set password if provided
	if password != "" {
		logLine(Msg(lang, "[INSTALL] Setting password for user...", "[INSTALL] Setze Passwort für Benutzer..."))
		chpasswdCmd := exec.Command("chpasswd")
		chpasswdCmd.Stdin = strings.NewReader(fmt.Sprintf("%s:%s\n", username, password))
		if err := chpasswdCmd.Run(); err != nil {
			logLine(Msg(lang, "[WARNING] Failed to set password: ", "[WARNUNG] Passwort konnte nicht gesetzt werden: ") + err.Error())
		}
	}

	// 2. Download linuxgsm.sh bootstrap script (as the new user, into its own directory)
	logLine(Msg(lang, "[INSTALL] Downloading LinuxGSM bootstrap script...", "[INSTALL] Lade LinuxGSM Bootstrap-Skript herunter..."))
	bootstrapPath := filepath.Join(homeDir, "linuxgsm.sh")
	if err := runCommandWithLogs("runuser", "-u", username, "--", "wget", "-O", bootstrapPath, "https://linuxgsm.sh"); err != nil {
		logLine(Msg(lang, "[ERROR] Failed to download bootstrap script: ", "[FEHLER] Bootstrap-Skript konnte nicht geladen werden: ") + err.Error())
		sse.exit(1)
		return
	}

	// Make executable
	if err := exec.Command("runuser", "-u", username, "--", "chmod", "+x", "--", bootstrapPath).Run(); err != nil {
		logLine(Msg(lang, "[ERROR] Failed to set permissions: ", "[FEHLER] Berechtigungskonfiguration schlug fehl: ") + err.Error())
		sse.exit(1)
		return
	}

	// 3. Run bootstrap: runuser -l <user> -c "cd '<dir>' && ./linuxgsm.sh '<gameCmd>'"
	logLine(Msg(lang, "[INSTALL] Initializing LinuxGSM script...", "[INSTALL] Initialisiere LinuxGSM Skript..."))
	bootstrapCmd := fmt.Sprintf("cd %s && ./linuxgsm.sh %s", ShellQuote(homeDir), ShellQuote(gameCmd))
	if err := runCommandWithLogs("runuser", "-l", username, "-c", bootstrapCmd); err != nil {
		logLine(Msg(lang, "[ERROR] Script initialization failed: ", "[FEHLER] Skriptinitialisierung schlug fehl: ") + err.Error())
		sse.exit(1)
		return
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
	err := runCommandWithLogs("runuser", "-l", username, "-c", installCmd)

	exitCode := 0
	if err != nil {
		logLine(Msg(lang, "[ERROR] Server installation failed: ", "[FEHLER] Server-Installation fehlgeschlagen: ") + err.Error())
		exitCode = 1
	} else {
		logLine(Msg(lang, "[INSTALL] Installation completed successfully!", "[INSTALL] Installation erfolgreich abgeschlossen!"))
	}

	// Trigger reload
	im.ScanInstances()

	sse.exit(exitCode)
}

// -------------------------------------------------------------
// Multi-Instance Creator
// -------------------------------------------------------------

var reInstanceSuffix = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func (im *InstanceManager) CreateMultiInstance(serverID string, suffix string) (string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return "", fmt.Errorf("server not found")
	}

	cleanSuffix := strings.ToLower(strings.TrimSpace(suffix))
	if cleanSuffix == "" {
		return "", fmt.Errorf("suffix cannot be empty")
	}
	if !reInstanceSuffix.MatchString(cleanSuffix) {
		return "", fmt.Errorf("suffix can only contain lowercase letters, numbers, and hyphens")
	}

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
	copyCmd := fmt.Sprintf("cd %s && cp -p -- %s %s && chmod 0755 -- %s",
		ShellQuote(srv.Dir), ShellQuote(sourceScript), ShellQuote(newInstanceScript), ShellQuote(newInstanceScript))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "runuser", "-l", srv.User, "-c", copyCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("[ERROR] Failed to create instance script %s: %s (%v)\n", newInstanceScript, string(out), err)
		return "", fmt.Errorf("failed to create instance script")
	}

	initialConfig := fmt.Sprintf("## LinuxGSM Multi-Instance Config: %s\n## Generated by LinuxGSM Dashboard\n\n", newInstanceScript)

	// 1. Create config in lgsm/config-lgsm/<rootScript>/<newInstanceScript>.cfg (LinuxGSM standard)
	// 2. Also ensure lgsm/config-lgsm/<newInstanceScript>/<newInstanceScript>.cfg is available
	for _, rel := range []string{
		filepath.Join("lgsm", "config-lgsm", rootScript, newInstanceScript+".cfg"),
		filepath.Join("lgsm", "config-lgsm", newInstanceScript, newInstanceScript+".cfg"),
	} {
		resolved, err := ResolveInRoot(srv.Dir, rel)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(resolved); os.IsNotExist(err) {
			_ = writeServerFile(srv, rel, []byte(initialConfig))
		}
	}

	im.ScanInstances()
	return newInstanceScript, nil
}
