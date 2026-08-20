package backend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type GameServerInstance struct {
	ID          string      `json:"id"`     // Username or ScriptName
	Name        string      `json:"name"`   // Game Name
	User        string      `json:"user"`   // Linux User
	Script      string      `json:"script"` // Executable script name (e.g. arkserver)
	Status      string      `json:"status"` // running | stopped | installing | updating
	Port        int         `json:"port"`   // Game Port
	Game        string      `json:"game"`   // Game ID
	CPU         float64     `json:"cpu"`    // Process CPU usage percentage
	RAM         float64     `json:"ram"`    // Process RAM usage in GB
	PIDs        []int       `json:"pids,omitempty"`
	ParsedPorts []PortProbe `json:"parsed_ports,omitempty"`
	Tag         string      `json:"tag,omitempty"`
	QueryData   QueryInfo   `json:"query_data"`
}

type QueryInfo struct {
	Online     bool         `json:"online"`
	Map        string       `json:"map"`
	NumPlayers int          `json:"num_players"`
	MaxPlayers int          `json:"max_players"`
	Bots       int          `json:"bots,omitempty"`
	Ping       int          `json:"ping,omitempty"`
	Players    []PlayerInfo `json:"players,omitempty"`
}

type PlayerInfo struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
	Time  string `json:"time"`
}

type ConfigFile struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Layer string `json:"layer"` // "default" | "common" | "instance" | "secrets"
}

type BackupFile struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	Date time.Time `json:"date"`
	Path string    `json:"path"`
}

type BackupSettings struct {
	MaxBackups        string `json:"maxbackups"`
	MaxBackupDays     string `json:"maxbackupdays"`
	StopOnBackup      string `json:"stoponbackup"`
	AutoBackupEnabled bool   `json:"autobackup_enabled"`
	AutoBackupCron    string `json:"autobackup_cron"`
}

type CronSchedule struct {
	MonitorInterval string `json:"monitor_interval"`
	UpdateInterval  string `json:"update_interval"`
	RestartTime     string `json:"restart_time"`
	CoreUpdateDay   string `json:"core_update_day"`
}

type InstanceManager struct {
	mu        sync.Mutex
	instances map[string]*GameServerInstance
}

// Msg returns the English or German text depending on the language code.
func Msg(lang, en, de string) string {
	if lang == "de" {
		return de
	}
	return en
}

func NewInstanceManager() *InstanceManager {
	im := &InstanceManager{
		instances: make(map[string]*GameServerInstance),
	}

	cleanupLeftoverSudoers()
	im.ScanInstances()

	return im
}

func (im *InstanceManager) GetInstances() []GameServerInstance {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.ScanInstancesNoLock()

	list := make([]GameServerInstance, 0, len(im.instances))
	for _, inst := range im.instances {
		item := *inst
		item.CPU, item.RAM, item.PIDs = item.GetProcessResourceUsage()
		list = append(list, item)
	}
	return list
}

func (im *InstanceManager) ScanInstances() {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.ScanInstancesNoLock()
}

func (im *InstanceManager) ScanInstancesNoLock() {
	// 1. Read /etc/passwd to find users with home directory in /home/
	file, err := os.Open("/etc/passwd")
	if err != nil {
		fmt.Println("Error reading /etc/passwd:", err)
		return
	}
	defer file.Close()

	detected := make(map[string]*GameServerInstance)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, ":")
		if len(parts) < 6 {
			continue
		}

		username := parts[0]
		homeDir := parts[5]

		if !strings.HasPrefix(homeDir, "/home/") {
			continue
		}

		// 2. Scan home directory for LinuxGSM scripts
		entries, err := os.ReadDir(homeDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			scriptName := entry.Name()
			if scriptName == "linuxgsm.sh" {
				continue
			}
			scriptPath := filepath.Join(homeDir, scriptName)

			// If script is a symlink to another LinuxGSM script, convert it to a real copy.
			// LinuxGSM resolves symlinks via `readlink -f $0` or `realpath`, which causes
			// symlinked instances to execute the root server instead of the clone.
			if linfo, err := os.Lstat(scriptPath); err == nil && linfo.Mode()&os.ModeSymlink != 0 {
				if target, err := os.Readlink(scriptPath); err == nil {
					targetPath := target
					if !filepath.IsAbs(targetPath) {
						targetPath = filepath.Join(homeDir, target)
					}
					if isLinuxGSMScript(targetPath) {
						_ = os.Remove(scriptPath)
						_ = exec.Command("cp", "-p", targetPath, scriptPath).Run()
						_ = exec.Command("chown", fmt.Sprintf("%s:%s", username, username), scriptPath).Run()
						_ = exec.Command("chmod", "0755", scriptPath).Run()
					}
				}
			}

			info, err := os.Stat(scriptPath)
			if err != nil {
				continue
			}

			// Must be executable
			if info.Mode()&0111 == 0 {
				continue
			}

			// Must be a LinuxGSM script (shebang + comment check)
			if !isLinuxGSMScript(scriptPath) {
				continue
			}

			// 3. Detected a LinuxGSM instance!
			port := parseServerPort(homeDir, scriptName)

			status := "stopped"
			if isTmuxSessionActive(username, scriptName) {
				status = "running"
			}

			// Handle multi-instance name mapping (e.g. gmodserver-1 -> Garry's Mod #1)
			gameName := mapScriptToGameName(scriptName)

			detected[scriptName] = &GameServerInstance{
				ID:     scriptName,
				Name:   gameName,
				User:   username,
				Script: scriptName,
				Status: status,
				Port:   port,
				Game:   getGameFromScriptName(scriptName),
			}
		}
	}

	// Keep track of active statuses like 'installing' or 'updating' and retain tags
	tagsMap := loadServerTags()
	for k, v := range detected {
		if t, ok := tagsMap[k]; ok {
			v.Tag = t
		}
		if existing, ok := im.instances[k]; ok {
			if existing.Status == "installing" || existing.Status == "updating" {
				// Don't overwrite active operations statuses
				v.Status = existing.Status
			}
			// Retain parsed ports/name if already loaded
			if len(existing.ParsedPorts) > 0 {
				v.ParsedPorts = existing.ParsedPorts
				v.Name = existing.Name
				v.Port = existing.Port
			}
			if existing.Tag != "" {
				v.Tag = existing.Tag
			}
		}
	}

	im.instances = detected

	// Trigger details refresh in background for any newly detected or not-yet-parsed servers
	for k, v := range detected {
		if len(v.ParsedPorts) == 0 {
			go im.RefreshServerDetails(k)
		}
	}
}

// parseDetailsOutput parses the details command output to extract the server name and ports.
func parseDetailsOutput(output string) (string, []PortProbe) {
	serverName := ""
	ports := []PortProbe{}

	lines := strings.Split(output, "\n")
	reName := regexp.MustCompile(`(?i)Server name:\s*(.+)`)
	rePort := regexp.MustCompile(`(?i)^\s*([a-zA-Z0-9\s_\-]+)\s+(\d+)\s+(tcp|udp)`)
	reClean := regexp.MustCompile(`(?i)(\x1b|\\x1b|\\e|\\033)?\[[0-9;]*[a-zA-Z]`)

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 1. Try server name match
		if match := reName.FindStringSubmatch(line); len(match) > 1 {
			rawName := strings.TrimSpace(match[1])
			serverName = strings.TrimSpace(reClean.ReplaceAllString(rawName, ""))
			continue
		}

		// 2. Try port match
		if match := rePort.FindStringSubmatch(line); len(match) > 3 {
			desc := strings.TrimSpace(match[1])
			portStr := match[2]
			proto := strings.ToUpper(match[3])

			portVal := 0
			fmt.Sscanf(portStr, "%d", &portVal)

			if portVal > 0 {
				ports = append(ports, PortProbe{
					Port:        portVal,
					Protocol:    proto,
					Description: desc,
					Open:        false,
				})
			}
		}
	}

	return serverName, ports
}

// RefreshServerDetails queries details from LinuxGSM in the background and updates in-memory states.
func (im *InstanceManager) RefreshServerDetails(serverID string) {
	im.mu.Lock()
	srv, exists := im.instances[serverID]
	if !exists {
		im.mu.Unlock()
		return
	}
	// Avoid re-fetching if already populated
	if len(srv.ParsedPorts) > 0 {
		im.mu.Unlock()
		return
	}
	username := srv.User
	scriptName := srv.Script
	im.mu.Unlock()

	im.executeRefreshDetails(serverID, username, scriptName)
}

// RefreshServerDetailsForce forces a details query from LinuxGSM in the background.
func (im *InstanceManager) RefreshServerDetailsForce(serverID string) {
	im.mu.Lock()
	srv, exists := im.instances[serverID]
	if !exists {
		im.mu.Unlock()
		return
	}
	username := srv.User
	scriptName := srv.Script
	im.mu.Unlock()

	im.executeRefreshDetails(serverID, username, scriptName)
}

func (im *InstanceManager) executeRefreshDetails(serverID, username, scriptName string) {
	go func() {
		execCmd := fmt.Sprintf("./%s details", scriptName)
		cmd := exec.Command("runuser", "-l", username, "-c", execCmd)

		outputBytes, err := cmd.Output()
		if err != nil {
			fmt.Printf("[WARNING] failed to query details for server %s: %v\n", serverID, err)
			return
		}

		serverName, ports := parseDetailsOutput(stripAnsi(string(outputBytes)))

		im.mu.Lock()
		defer im.mu.Unlock()

		srv, exists := im.instances[serverID]
		if exists {
			if serverName != "" {
				srv.Name = serverName
			}
			if len(ports) > 0 {
				srv.ParsedPorts = ports
				// Update main game port
				for _, p := range ports {
					if strings.EqualFold(p.Description, "Game") {
						srv.Port = p.Port
						break
					}
				}
				if srv.Port == 0 && len(ports) > 0 {
					srv.Port = ports[0].Port
				}
			}
		}
	}()
}

func (srv *GameServerInstance) GetProcessResourceUsage() (cpu float64, ram float64, pids []int) {
	return GetProcessStatsForUser(srv.User)
}

func (im *InstanceManager) GetConsole(serverID string, mode string, lang string) ([]string, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return nil, fmt.Errorf("server %s not found", serverID)
	}

	switch mode {
	case "tmux":
		socketName, sessionName, ok := findLinuxGSMTmuxTarget(srv.User, srv.Script)
		if !ok {
			return []string{Msg(lang, "Server is offline. tmux session is not active.", "Server ist offline. tmux Sitzung nicht aktiv.")}, nil
		}

		cmd := exec.Command("runuser", "-u", srv.User, "--", "tmux", "-L", socketName, "capture-pane", "-t", sessionName, "-p")
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			return []string{Msg(lang, "Failed to capture the tmux screen.", "Fehler beim Erfassen des tmux Bildschirms."), err.Error()}, nil
		}

		lines := strings.Split(out.String(), "\n")
		return lines, nil

	case "script":
		logPath := filepath.Join("/home", srv.User, "log", "script", fmt.Sprintf("%s-script.log", srv.Script))
		return readLastNLines(logPath, 150, lang)

	case "game":
		// Look in /home/<user>/log/game/ directory or serverfiles
		gameLogDir := filepath.Join("/home", srv.User, "log", "game")
		var logFile string
		if entries, err := os.ReadDir(gameLogDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					logFile = filepath.Join(gameLogDir, e.Name())
					break
				}
			}
		}
		if logFile == "" {
			// Fallback: look for common engine logs
			candidates := []string{
				filepath.Join("/home", srv.User, "serverfiles", "logs"),
				filepath.Join("/home", srv.User, "serverfiles", "server.log"),
				filepath.Join("/home", srv.User, "serverfiles", "logs", "latest.log"),
			}
			for _, c := range candidates {
				if _, err := os.Stat(c); err == nil {
					logFile = c
					break
				}
			}
		}
		if logFile == "" {
			return []string{
				Msg(lang, "Game engine log file not found.", "Spiele-Engine Logdatei nicht gefunden."),
				Msg(lang, "Checked directory: ", "Geprüfter Ordner: ") + gameLogDir,
			}, nil
		}
		return readLastNLines(logFile, 150, lang)

	default: // "console" or default
		logPath := filepath.Join("/home", srv.User, "log", "console", fmt.Sprintf("%s-console.log", srv.Script))
		return readLastNLines(logPath, 150, lang)
	}
}

func readLastNLines(logPath string, n int, lang string) ([]string, error) {
	file, err := os.Open(logPath)
	if err != nil {
		return []string{
			Msg(lang, "Log file could not be opened.", "Logdatei konnte nicht geöffnet werden."),
			Msg(lang, "Path: ", "Pfad: ") + logPath,
		}, nil
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		clean := stripAnsi(scanner.Text())
		lines = append(lines, clean)
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines, nil
}

func (im *InstanceManager) GetConfigFiles(serverID string) ([]ConfigFile, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return nil, fmt.Errorf("server not found")
	}

	var configs []ConfigFile
	seenPaths := make(map[string]bool)

	addConfig := func(name, path, layer string) {
		clean := filepath.Clean(path)
		if seenPaths[clean] {
			return
		}
		seenPaths[clean] = true
		configs = append(configs, ConfigFile{
			Name:  name,
			Path:  clean,
			Layer: layer,
		})
	}

	rootScript := strings.Split(srv.Script, "-")[0]

	// 1. Scan primary LinuxGSM config directory for this specific instance: /home/<user>/lgsm/config-lgsm/<script>/
	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	if files, err := os.ReadDir(configDir); err == nil {
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".cfg") {
				layer := "instance"
				if f.Name() == "_default.cfg" {
					layer = "default"
				} else if f.Name() == "common.cfg" {
					layer = "common"
				} else if strings.HasPrefix(f.Name(), "secrets-") {
					layer = "secrets"
				}
				addConfig(f.Name(), filepath.Join(configDir, f.Name()), layer)
			}
		}
	}

	// 2. If this is a multi-instance (e.g. mtaserver-2), also scan the root config directory /home/<user>/lgsm/config-lgsm/<rootScript>/
	if rootScript != srv.Script {
		rootConfigDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", rootScript)
		if files, err := os.ReadDir(rootConfigDir); err == nil {
			for _, f := range files {
				if !f.IsDir() && strings.HasSuffix(f.Name(), ".cfg") {
					if f.Name() == "_default.cfg" {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "default")
					} else if f.Name() == "common.cfg" {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "common")
					} else if f.Name() == fmt.Sprintf("%s.cfg", srv.Script) {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "instance")
					} else if strings.HasPrefix(f.Name(), fmt.Sprintf("secrets-%s", srv.Script)) {
						addConfig(f.Name(), filepath.Join(rootConfigDir, f.Name()), "secrets")
					}
				}
			}
		}
	}

	// 3. Scan gameserver files directory recursively: /home/<user>/serverfiles/
	serverfilesDir := filepath.Join("/home", srv.User, "serverfiles")
	if _, err := os.Stat(serverfilesDir); err == nil {
		found := findConfigsInDir(filepath.Join("/home", srv.User), serverfilesDir, 0)
		for _, cfg := range found {
			addConfig(cfg.Name, cfg.Path, "instance")
		}
	}

	return configs, nil
}

func isPathWithinDirectories(path string, allowedDirs ...string) bool {
	cleanPath := filepath.Clean(path)
	if evalPath, err := filepath.EvalSymlinks(cleanPath); err == nil {
		cleanPath = evalPath
	}
	for _, allowed := range allowedDirs {
		cleanAllowed := filepath.Clean(allowed)
		if evalAllowed, err := filepath.EvalSymlinks(cleanAllowed); err == nil {
			cleanAllowed = evalAllowed
		}
		if cleanPath == cleanAllowed || strings.HasPrefix(cleanPath, cleanAllowed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (im *InstanceManager) GetConfigFileContent(serverID, path string) (string, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return "", fmt.Errorf("server not found")
	}

	// SECURITY CHECK: Ensure path is within allowed directories
	rootScript := strings.Split(srv.Script, "-")[0]
	allowedDir1 := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	allowedDir2 := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", rootScript)
	allowedDir3 := filepath.Join("/home", srv.User, "serverfiles")
	cleanPath := filepath.Clean(path)

	if !isPathWithinDirectories(cleanPath, allowedDir1, allowedDir2, allowedDir3) {
		return "", fmt.Errorf("access denied: path outside allowed config directories")
	}

	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (im *InstanceManager) SaveConfigFileContent(serverID, path, content string) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	// SECURITY CHECK: Ensure path is within allowed directories
	rootScript := strings.Split(srv.Script, "-")[0]
	allowedDir1 := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	allowedDir2 := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", rootScript)
	allowedDir3 := filepath.Join("/home", srv.User, "serverfiles")
	cleanPath := filepath.Clean(path)

	if !isPathWithinDirectories(cleanPath, allowedDir1, allowedDir2, allowedDir3) {
		return fmt.Errorf("access denied: path outside allowed config directories")
	}

	err := os.WriteFile(cleanPath, []byte(content), 0644)
	if err == nil {
		go im.RefreshServerDetailsForce(serverID)
	}
	return err
}

func (im *InstanceManager) RunAction(w http.ResponseWriter, r *http.Request, serverID, action, lang string) {
	im.mu.Lock()
	srv := im.instances[serverID]

	if srv == nil {
		im.mu.Unlock()
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Set local state status to avoid concurrent triggers
	oldStatus := srv.Status
	if action == "update" || action == "update-lgsm" || action == "force-update" {
		srv.Status = "updating"
	}
	im.mu.Unlock()

	// Callback to update status inside streaming routine
	updateStatus := func(newStatus string) {
		im.mu.Lock()
		defer im.mu.Unlock()
		if s, ok := im.instances[serverID]; ok {
			s.Status = newStatus
		}
	}

	// Real execution on Linux
	flusher, ok := w.(http.Flusher)
	if !ok {
		updateStatus(oldStatus)
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(msgType string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}

	// Execute command via runuser: runuser -l <user> -c "cd /home/<user> && ./<script> <action>"
	execCmd := fmt.Sprintf("cd /home/%s && ./%s %s", srv.User, srv.Script, action)
	cmd := exec.Command("runuser", "-l", srv.User, "-c", execCmd)
	cmd.Stdin = strings.NewReader("y\ny\ny\n")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		updateStatus(oldStatus)
		sendSSE("message", map[string]interface{}{"type": "log", "text": Msg(lang, "Error creating stdout pipe: ", "Fehler beim Erstellen der stdout Pipe: ") + err.Error()})
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}
	cmd.Stderr = cmd.Stdout // Redirect stderr to stdout to stream both

	if err := cmd.Start(); err != nil {
		updateStatus(oldStatus)
		sendSSE("message", map[string]interface{}{"type": "log", "text": Msg(lang, "Error starting command: ", "Fehler beim Starten des Befehls: ") + err.Error()})
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}

	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				sendSSE("message", map[string]interface{}{"type": "log", "text": "\n" + Msg(lang, "Error reading output: ", "Fehler beim Lesen des Outputs: ") + err.Error()})
			}
			break
		}
		// Strip ANSI colors
		cleanLine := stripAnsi(line)
		sendSSE("message", map[string]interface{}{
			"type": "log",
			"text": cleanLine,
		})
	}

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}

	// Reset status from "updating" so ScanInstances detects the actual status
	im.mu.Lock()
	if s, ok := im.instances[serverID]; ok && s.Status == "updating" {
		s.Status = "stopped"
	}
	im.mu.Unlock()

	// Rescan instances to reflect status changes
	im.ScanInstances()

	// Force refresh details on start/restart/update actions
	if action == "start" || action == "restart" || action == "update" {
		go im.RefreshServerDetailsForce(serverID)
	}

	sendSSE("message", map[string]interface{}{
		"type": "exit",
		"code": exitCode,
	})
}

func (im *InstanceManager) InstallGame(w http.ResponseWriter, r *http.Request, gameCmd, username, password, lang string) {
	// Real installation on Linux
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(msgType string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}

	logLine := func(text string) {
		sendSSE("message", map[string]interface{}{
			"type": "log",
			"text": text + "\n",
		})
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
			if err != nil {
				break
			}
			sendSSE("message", map[string]interface{}{
				"type": "log",
				"text": stripAnsi(line),
			})
		}
		return cmd.Wait()
	}

	logLine(Msg(lang,
		"[INSTALL] Starting installation for "+gameCmd+" under system user '"+username+"'...",
		"[INSTALL] Starte Installation für "+gameCmd+" unter Systemuser '"+username+"'..."))

	// 1. Create Linux user
	logLine(Msg(lang, "[INSTALL] Creating system user...", "[INSTALL] Lege Systembenutzer an..."))
	err := runCommandWithLogs("useradd", "-m", "-s", "/bin/bash", username)
	if err != nil {
		logLine(Msg(lang, "[ERROR] Failed to create user: ", "[FEHLER] Benutzer konnte nicht erstellt werden: ") + err.Error())
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}

	// Set password if provided
	if password != "" {
		logLine(Msg(lang, "[INSTALL] Setting password for user...", "[INSTALL] Setze Passwort für Benutzer..."))
		chpasswdCmd := exec.Command("chpasswd")
		chpasswdCmd.Stdin = strings.NewReader(fmt.Sprintf("%s:%s", username, password))
		err = chpasswdCmd.Run()
		if err != nil {
			logLine(Msg(lang, "[WARNING] Failed to set password: ", "[WARNUNG] Passwort konnte nicht gesetzt werden: ") + err.Error())
		}
	}

	homeDir := filepath.Join("/home", username)

	// 2. Download linuxgsm.sh bootstrap script
	logLine(Msg(lang, "[INSTALL] Downloading LinuxGSM bootstrap script...", "[INSTALL] Lade LinuxGSM Bootstrap-Skript herunter..."))
	err = runCommandWithLogs("wget", "-O", filepath.Join(homeDir, "linuxgsm.sh"), "https://linuxgsm.sh")
	if err != nil {
		logLine(Msg(lang, "[ERROR] Failed to download bootstrap script: ", "[FEHLER] Bootstrap-Skript konnte nicht geladen werden: ") + err.Error())
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}

	// Make executable & fix ownership
	err = exec.Command("chmod", "+x", filepath.Join(homeDir, "linuxgsm.sh")).Run()
	if err != nil {
		logLine(Msg(lang, "[ERROR] Failed to set permissions: ", "[FEHLER] Berechtigungskonfiguration schlug fehl: ") + err.Error())
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}
	err = exec.Command("chown", fmt.Sprintf("%s:%s", username, username), filepath.Join(homeDir, "linuxgsm.sh")).Run()
	if err != nil {
		logLine(Msg(lang, "[ERROR] Failed to change ownership: ", "[FEHLER] Eigentümeränderung schlug fehl: ") + err.Error())
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}

	// 3. Run bootstrap: runuser -l <user> -c "./linuxgsm.sh <gameCmd>"
	logLine(Msg(lang, "[INSTALL] Initializing LinuxGSM script...", "[INSTALL] Initialisiere LinuxGSM Skript..."))
	bootstrapCmd := fmt.Sprintf("cd %s && ./linuxgsm.sh %s", homeDir, gameCmd)
	err = runCommandWithLogs("runuser", "-l", username, "-c", bootstrapCmd)
	if err != nil {
		logLine(Msg(lang, "[ERROR] Script initialization failed: ", "[FEHLER] Skriptinitialisierung schlug fehl: ") + err.Error())
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}

	// 3.5 Grant temporary sudo access for LinuxGSM dependency auto-installer
	sudoersFile := fmt.Sprintf("/etc/sudoers.d/lgsm-%s", username)
	sudoersContent := fmt.Sprintf("%s ALL=(ALL) NOPASSWD: ALL\n", username)
	err = os.WriteFile(sudoersFile, []byte(sudoersContent), 0440)
	if err != nil {
		logLine(Msg(lang, "[WARNING] Failed to configure temporary sudo permissions: ", "[WARNUNG] Sudo-Berechtigungen konnten nicht temporär konfiguriert werden: ") + err.Error())
	}
	defer func() {
		_ = os.Remove(sudoersFile)
	}()

	// 4. Run game auto-install: runuser -l <user> -c "./<gameCmd> auto-install"
	logLine(Msg(lang, "[INSTALL] Installing server via LinuxGSM (SteamCMD)...", "[INSTALL] Installiere Server via LinuxGSM (SteamCMD)..."))
	installCmd := fmt.Sprintf("cd %s && ./%s auto-install", homeDir, gameCmd)
	err = runCommandWithLogs("runuser", "-l", username, "-c", installCmd)

	exitCode := 0
	if err != nil {
		logLine(Msg(lang, "[ERROR] Server installation failed: ", "[FEHLER] Server-Installation fehlgeschlagen: ") + err.Error())
		exitCode = 1
	} else {
		logLine(Msg(lang, "[INSTALL] Installation completed successfully!", "[INSTALL] Installation erfolgreich abgeschlossen!"))
	}

	// Trigger reload
	im.ScanInstances()

	sendSSE("message", map[string]interface{}{
		"type": "exit",
		"code": exitCode,
	})
}

// Helpers

func parseServerPort(homeDir string, scriptName string) int {
	// Try to find the port in config files
	// Order: <script>.cfg -> common.cfg -> _default.cfg
	rootScript := strings.Split(scriptName, "-")[0]
	configDir := filepath.Join(homeDir, "lgsm", "config-lgsm", scriptName)
	rootConfigDir := filepath.Join(homeDir, "lgsm", "config-lgsm", rootScript)
	filesToTry := []string{
		filepath.Join(configDir, fmt.Sprintf("%s.cfg", scriptName)),
		filepath.Join(rootConfigDir, fmt.Sprintf("%s.cfg", scriptName)),
		filepath.Join(configDir, "common.cfg"),
		filepath.Join(rootConfigDir, "common.cfg"),
		filepath.Join(configDir, "_default.cfg"),
		filepath.Join(rootConfigDir, "_default.cfg"),
	}

	portRegex := regexp.MustCompile(`port="?(\d+)"?`)

	for _, fp := range filesToTry {
		port := func() int {
			file, err := os.Open(fp)
			if err != nil {
				return 0
			}
			defer file.Close()

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				matches := portRegex.FindStringSubmatch(scanner.Text())
				if len(matches) > 1 {
					p, err := strconv.Atoi(matches[1])
					if err == nil {
						return p
					}
				}
			}
			return 0
		}()
		if port > 0 {
			return port
		}
	}

	// Game-specific fallbacks if not found in LGSM config
	if rootScript == "mtaserver" || scriptName == "mtaserver" {
		mtaConfig := filepath.Join(homeDir, "serverfiles", "mods", "deathmatch", "mtaserver.conf")
		file, err := os.Open(mtaConfig)
		if err == nil {
			defer file.Close()
			mtaRegex := regexp.MustCompile(`<serverport>(\d+)</serverport>`)
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				matches := mtaRegex.FindStringSubmatch(scanner.Text())
				if len(matches) > 1 {
					p, err := strconv.Atoi(matches[1])
					if err == nil && p > 0 {
						return p
					}
				}
			}
		}
	}

	if rootScript == "mcserver" || rootScript == "minecraft" || scriptName == "mcserver" || scriptName == "minecraft" {
		mcConfig := filepath.Join(homeDir, "serverfiles", "server.properties")
		file, err := os.Open(mcConfig)
		if err == nil {
			defer file.Close()
			mcRegex := regexp.MustCompile(`server-port=(\d+)`)
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				matches := mcRegex.FindStringSubmatch(scanner.Text())
				if len(matches) > 1 {
					p, err := strconv.Atoi(matches[1])
					if err == nil && p > 0 {
						return p
					}
				}
			}
		}
	}

	return 0 // default not found
}

func isTmuxSessionActive(username string, scriptName string) bool {
	_, _, ok := findLinuxGSMTmuxTarget(username, scriptName)
	return ok
}

func findLinuxGSMTmuxTarget(username string, scriptName string) (socketName string, sessionName string, ok bool) {
	homeDir := filepath.Join("/home", username)
	for _, socket := range linuxGSMTmuxSocketNames(homeDir, scriptName) {
		sessions, err := listTmuxSessions(username, socket)
		if err != nil {
			continue
		}
		if tmuxSessionExists(sessions, scriptName) {
			return socket, scriptName, true
		}
	}
	return "", "", false
}

func linuxGSMTmuxSocketNames(homeDir string, scriptName string) []string {
	var sockets []string
	uidPath := filepath.Join(homeDir, "lgsm", "data", scriptName+".uid")
	if uidBytes, err := os.ReadFile(uidPath); err == nil {
		uid := strings.TrimSpace(string(uidBytes))
		if uid != "" {
			sockets = appendUniqueString(sockets, scriptName+"-"+uid)
		}
	}

	// Legacy LinuxGSM versions used the session name as the socket name.
	return appendUniqueString(sockets, scriptName)
}

func appendUniqueString(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func listTmuxSessions(username string, socketName string) (string, error) {
	cmd := exec.Command("runuser", "-u", username, "--", "tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

func tmuxSessionExists(sessionList string, sessionName string) bool {
	for _, line := range strings.Split(sessionList, "\n") {
		if strings.TrimSpace(line) == sessionName {
			return true
		}
	}
	return false
}

func mapScriptToGameName(scriptName string) string {
	// Simple script name mappings
	mappings := map[string]string{
		"arkserver":   "Ark: Survival Evolved",
		"vhserver":    "Valheim",
		"cs2server":   "Counter-Strike 2",
		"csgoserver":  "CS:GO",
		"rustserver":  "Rust",
		"mcserver":    "Minecraft",
		"sdtdserver":  "7 Days to Die",
		"gmodserver":  "Garry's Mod",
		"tserver":     "Terraria",
		"fctrserver":  "Factorio",
		"tf2server":   "Team Fortress 2",
		"squadserver": "Squad",
		"l4d2server":  "Left 4 Dead 2",
		"insserver":   "Insurgency",
		"dstserver":   "Don't Starve Together",
		"ceserver":    "Conan Exiles",
		"dayzserver":  "DayZ",
		"arma3server": "Arma 3",
		"sfserver":    "Satisfactory",
		"pwserver":    "Palworld",
		"pzserver":    "Project Zomboid",
		"mtaserver":   "Multi Theft Auto",
	}

	// Extract suffix if it has an instance marker like -1 or -zombies
	baseName := scriptName
	suffix := ""
	if parts := strings.SplitN(scriptName, "-", 2); len(parts) > 1 {
		baseName = parts[0]
		if num, err := strconv.Atoi(parts[1]); err == nil {
			suffix = fmt.Sprintf(" #%d", num)
		} else {
			suffix = fmt.Sprintf(" (%s)", parts[1])
		}
	}

	if name, ok := mappings[baseName]; ok {
		return name + suffix
	}

	// Fallback format
	clean := strings.TrimSuffix(baseName, "server")
	if len(clean) > 0 {
		return strings.ToUpper(clean[0:1]) + clean[1:] + suffix
	}
	return scriptName
}

func stripAnsi(str string) string {
	// Regular expression to strip ANSI escape codes and VT100 control codes
	reAnsi := regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	clean := reAnsi.ReplaceAllString(str, "")

	// Secondary check for CSI / OSC control characters
	reControl := regexp.MustCompile(`[\x00-\x09\x0b-\x1f\x7f]`)
	return reControl.ReplaceAllString(clean, "")
}

func isLinuxGSMScript(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()

	buf := make([]byte, 500)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return false
	}

	content := string(buf[:n])

	// Must start with shebang
	if !strings.HasPrefix(content, "#!") {
		return false
	}

	// Check for LinuxGSM indicators
	return strings.Contains(content, "LinuxGSM") || strings.Contains(content, "lgsmdir") || strings.Contains(content, "lgsmversion")
}

func getGameFromScriptName(scriptName string) string {
	parts := strings.Split(scriptName, "-")
	clean := parts[0]
	clean = strings.TrimSuffix(clean, "server")
	return clean
}

func (im *InstanceManager) SendConsoleCommand(serverID string, command string) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server %s not found", serverID)
	}

	socketName, sessionName, ok := findLinuxGSMTmuxTarget(srv.User, srv.Script)
	if !ok {
		return fmt.Errorf("server is not running")
	}

	// Execute command safely against the LinuxGSM tmux socket.
	cmd := exec.Command("runuser", "-u", srv.User, "--", "tmux", "-L", socketName, "send-keys", "-t", sessionName, command, "ENTER")
	return cmd.Run()
}

var skipDirs = map[string]bool{
	"steamapps":         true,
	"steam":             true,
	"Engine":            true,
	"Binaries":          true,
	"MonoBleedingEdge":  true,
	"linux64":           true,
	"node_modules":      true,
	".git":              true,
	".steam":            true,
	"lgsm":              true,
	"log":               true,
	"backups":           true,
	"saves":             true,
	"Save":              true,
	"Saved":             true,
	"serverfiles/steam": true,
	"steamclient":       true,
}

type FileNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"is_dir"`
	Size     int64      `json:"size"`
	ModTime  string     `json:"mod_time"`
	Children []FileNode `json:"children,omitempty"`
}

func (im *InstanceManager) GetServerFiles(serverID string, subPath string) ([]FileNode, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return nil, fmt.Errorf("server %s not found", serverID)
	}

	baseDir := filepath.Join("/home", srv.User, "serverfiles")
	targetDir := baseDir
	if subPath != "" {
		targetDir = filepath.Join(baseDir, subPath)
	}

	rel, err := filepath.Rel(baseDir, targetDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("invalid path traversal attempt")
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return []FileNode{}, nil
	}

	var nodes []FileNode
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		relPath, _ := filepath.Rel(baseDir, filepath.Join(targetDir, entry.Name()))
		relPath = filepath.ToSlash(relPath)

		nodes = append(nodes, FileNode{
			Name:    entry.Name(),
			Path:    relPath,
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	return nodes, nil
}

func (im *InstanceManager) GetServerFileContent(serverID string, relPath string) (string, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return "", fmt.Errorf("server %s not found", serverID)
	}

	baseDir := filepath.Join("/home", srv.User, "serverfiles")
	targetFile := filepath.Join(baseDir, relPath)
	rel, err := filepath.Rel(baseDir, targetFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid path traversal attempt")
	}

	content, err := os.ReadFile(targetFile)
	if err != nil {
		return "", err
	}

	return string(content), nil
}

func (im *InstanceManager) SaveServerFileContent(serverID string, relPath string, content string) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server %s not found", serverID)
	}

	baseDir := filepath.Join("/home", srv.User, "serverfiles")
	targetFile := filepath.Join(baseDir, relPath)
	rel, err := filepath.Rel(baseDir, targetFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("invalid path traversal attempt")
	}

	return os.WriteFile(targetFile, []byte(content), 0644)
}

func (im *InstanceManager) UploadServerFile(serverID string, subPath string, filename string, data []byte) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server %s not found", serverID)
	}

	baseDir := filepath.Join("/home", srv.User, "serverfiles")
	targetDir := baseDir
	if subPath != "" {
		targetDir = filepath.Join(baseDir, subPath)
	}

	rel, err := filepath.Rel(baseDir, targetDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("invalid path traversal attempt")
	}

	targetFile := filepath.Join(targetDir, filepath.Base(filename))
	err = os.WriteFile(targetFile, data, 0644)
	if err != nil {
		return err
	}

	_ = exec.Command("chown", fmt.Sprintf("%s:%s", srv.User, srv.User), targetFile).Run()
	return nil
}

func findConfigsInDir(baseDir string, dirPath string, depth int) []ConfigFile {
	if depth > 4 {
		return nil
	}

	var configs []ConfigFile
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		name := entry.Name()
		fullPath := filepath.Join(dirPath, name)

		if entry.IsDir() {
			if skipDirs[name] {
				continue
			}
			configs = append(configs, findConfigsInDir(baseDir, fullPath, depth+1)...)
		} else {
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".cfg" || ext == ".ini" || ext == ".properties" || ext == ".conf" || ext == ".json" {
				relPath, err := filepath.Rel(baseDir, fullPath)
				if err != nil {
					relPath = name
				}
				relPath = filepath.ToSlash(relPath)
				configs = append(configs, ConfigFile{
					Name: relPath,
					Path: fullPath,
				})
			}
		}
	}
	return configs
}

type PortProbe struct {
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Open        bool   `json:"open"`
	Description string `json:"description"`
}

func (im *InstanceManager) CheckServerPorts(w http.ResponseWriter, r *http.Request, serverID string) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// 1. Resolve Public IP
	publicIP, err := getPublicIP()
	if err != nil {
		publicIP = "127.0.0.1"
	}

	var probes []PortProbe
	var configProbes []PortProbe
	if len(srv.ParsedPorts) > 0 {
		configProbes = make([]PortProbe, len(srv.ParsedPorts))
		copy(configProbes, srv.ParsedPorts)
	} else {
		// Try to parse ports directly from config files
		homeDir := filepath.Join("/home", srv.User)
		configProbes = parseServerPortsFromConfig(homeDir, srv.Script, srv.Game)
	}

	// If no probes could be parsed, fallback to heuristics
	if len(configProbes) == 0 {
		p := srv.Port
		if p > 0 {
			if srv.Game == "minecraft" || srv.Game == "mc" {
				configProbes = append(configProbes, PortProbe{Port: p, Protocol: "TCP", Description: "Game Port"})
				rcon := p + 10
				if p == 25565 {
					rcon = 25575
				}
				configProbes = append(configProbes, PortProbe{Port: rcon, Protocol: "TCP", Description: "RCON Port"})
			} else {
				configProbes = append(configProbes, PortProbe{Port: p, Protocol: "UDP", Description: "Game Port"})
				configProbes = append(configProbes, PortProbe{Port: p + 1, Protocol: "UDP", Description: "Steam Query Port"})
				if p != 27015 && p+1 != 27015 {
					configProbes = append(configProbes, PortProbe{Port: 27015, Protocol: "UDP", Description: "Default Query Port"})
				}
			}
		}
	}

	// Run probes and copy to final slice
	for _, cp := range configProbes {
		open := false
		if strings.ToUpper(cp.Protocol) == "TCP" {
			open = checkTCP(publicIP, cp.Port)
		} else {
			open = checkUDPQuery(publicIP, cp.Port)
		}
		probes = append(probes, PortProbe{
			Port:        cp.Port,
			Protocol:    cp.Protocol,
			Open:        open,
			Description: cp.Description,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"public_ip": publicIP,
		"probes":    probes,
	})
}

func (im *InstanceManager) DeleteServer(w http.ResponseWriter, r *http.Request, serverID string) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// 1. Stop the server first
	stopCmd := exec.Command("runuser", "-l", srv.User, "-c", fmt.Sprintf("./%s stop", srv.Script))
	_ = stopCmd.Run()

	// 2. Count instances sharing the same user
	im.mu.Lock()
	userInstancesCount := 0
	for _, inst := range im.instances {
		if inst.User == srv.User {
			userInstancesCount++
		}
	}
	im.mu.Unlock()

	// 3. Remove systemd service if it exists
	servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", srv.ID)
	if _, err := os.Stat(servicePath); err == nil {
		_ = exec.Command("systemctl", "stop", srv.ID).Run()
		_ = exec.Command("systemctl", "disable", srv.ID).Run()
		_ = os.Remove(servicePath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}

	// 4. Remove crontab entries
	crontabCmd := exec.Command("crontab", "-u", srv.User, "-r")
	_ = crontabCmd.Run()

	// 5. Delete game files & user
	if userInstancesCount > 1 {
		// Shared user! Only delete script and config
		homeDir := filepath.Join("/home", srv.User)
		scriptPath := filepath.Join(homeDir, srv.Script)
		_ = os.Remove(scriptPath)

		configDir := filepath.Join(homeDir, "lgsm", "config-lgsm", srv.Script)
		_ = os.RemoveAll(configDir)
	} else {
		// Exclusive user! Wipe everything by deleting user and home folder
		_ = exec.Command("pkill", "-u", srv.User).Run()
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("pkill", "-9", "-u", srv.User).Run()

		userdelCmd := exec.Command("userdel", "-r", srv.User)
		err := userdelCmd.Run()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to delete user: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// 6. Force scan update
	im.ScanInstances()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "message": "Server deleted successfully"})
}

func getPublicIP() (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	ipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ipBytes)), nil
}

func checkTCP(ip string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func checkUDPQuery(ip string, port int) bool {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		return false
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return false
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(2 * time.Second))

	// Send A2S_INFO packet: \xff\xff\xff\xffTSource Engine Query\x00
	query := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x54, 0x53, 0x6F, 0x75, 0x72, 0x63, 0x65, 0x20, 0x45, 0x6E, 0x67, 0x69, 0x6E, 0x65, 0x20, 0x51, 0x75, 0x65, 0x72, 0x79, 0x00}
	_, err = conn.Write(query)
	if err != nil {
		return false
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil || n < 5 {
		return false
	}
	return buf[0] == 0xFF && buf[1] == 0xFF && buf[2] == 0xFF && buf[3] == 0xFF
}

func parseServerPortsFromConfig(homeDir string, scriptName string, game string) []PortProbe {
	// Game-specific overrides for port configurations
	if scriptName == "mtaserver" || game == "multitheftauto" {
		mtaConfig := filepath.Join(homeDir, "serverfiles", "mods", "deathmatch", "mtaserver.conf")
		file, err := os.Open(mtaConfig)
		if err == nil {
			defer file.Close()
			mtaPortRegex := regexp.MustCompile(`<serverport>(\d+)</serverport>`)
			mtaHttpRegex := regexp.MustCompile(`<httpport>(\d+)</httpport>`)

			serverPort := 22003 // default
			httpPort := 22005   // default

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if matches := mtaPortRegex.FindStringSubmatch(line); len(matches) > 1 {
					if p, err := strconv.Atoi(matches[1]); err == nil && p > 0 {
						serverPort = p
					}
				}
				if matches := mtaHttpRegex.FindStringSubmatch(line); len(matches) > 1 {
					if p, err := strconv.Atoi(matches[1]); err == nil && p > 0 {
						httpPort = p
					}
				}
			}

			var probes []PortProbe
			probes = append(probes, PortProbe{
				Port:        serverPort,
				Protocol:    "UDP",
				Description: "MTA Game Port",
			})
			probes = append(probes, PortProbe{
				Port:        serverPort + 123,
				Protocol:    "UDP",
				Description: "MTA ASE Query Port",
			})
			probes = append(probes, PortProbe{
				Port:        httpPort,
				Protocol:    "TCP",
				Description: "MTA HTTP Web Server",
			})
			return probes
		}
	}

	if scriptName == "mcserver" || scriptName == "minecraft" || game == "minecraft" || game == "mc" {
		mcConfig := filepath.Join(homeDir, "serverfiles", "server.properties")
		file, err := os.Open(mcConfig)
		if err == nil {
			defer file.Close()
			portRegex := regexp.MustCompile(`server-port=(\d+)`)
			queryRegex := regexp.MustCompile(`query.port=(\d+)`)
			rconRegex := regexp.MustCompile(`rcon.port=(\d+)`)

			serverPort := 25565
			queryPort := 25565
			rconPort := 25575
			hasQuery := false
			hasRcon := false

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if matches := portRegex.FindStringSubmatch(line); len(matches) > 1 {
					if p, err := strconv.Atoi(matches[1]); err == nil && p > 0 {
						serverPort = p
					}
				}
				if matches := queryRegex.FindStringSubmatch(line); len(matches) > 1 {
					if p, err := strconv.Atoi(matches[1]); err == nil && p > 0 {
						queryPort = p
						hasQuery = true
					}
				}
				if matches := rconRegex.FindStringSubmatch(line); len(matches) > 1 {
					if p, err := strconv.Atoi(matches[1]); err == nil && p > 0 {
						rconPort = p
						hasRcon = true
					}
				}
			}

			var probes []PortProbe
			probes = append(probes, PortProbe{
				Port:        serverPort,
				Protocol:    "TCP",
				Description: "Minecraft Game Port",
			})
			if hasQuery {
				probes = append(probes, PortProbe{
					Port:        queryPort,
					Protocol:    "UDP",
					Description: "Minecraft Query Port",
				})
			}
			if hasRcon {
				probes = append(probes, PortProbe{
					Port:        rconPort,
					Protocol:    "TCP",
					Description: "Minecraft RCON Port",
				})
			}
			return probes
		}
	}

	configDir := filepath.Join(homeDir, "lgsm", "config-lgsm", scriptName)
	filesToTry := []string{
		filepath.Join(configDir, fmt.Sprintf("%s.cfg", scriptName)),
		filepath.Join(configDir, "common.cfg"),
		filepath.Join(configDir, "_default.cfg"),
	}

	// Maps of parsed values
	parsedPorts := make(map[string]int)

	// Regex for port variables: var="port" or var=port
	varRegex := regexp.MustCompile(`^(port|queryport|rconport|appport|sourcetvport)="?(\d+)"?`)

	for _, fp := range filesToTry {
		file, err := os.Open(fp)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			// Remove comments
			if strings.HasPrefix(line, "#") {
				continue
			}
			matches := varRegex.FindStringSubmatch(line)
			if len(matches) == 3 {
				varName := matches[1]
				portVal, _ := strconv.Atoi(matches[2])
				if _, exists := parsedPorts[varName]; !exists && portVal > 0 {
					parsedPorts[varName] = portVal
				}
			}
		}
		file.Close()
	}

	var probes []PortProbe

	// 1. Game Port
	if p, ok := parsedPorts["port"]; ok {
		proto := "UDP"
		if game == "minecraft" || game == "mc" {
			proto = "TCP"
		}
		probes = append(probes, PortProbe{
			Port:        p,
			Protocol:    proto,
			Description: "Game Port",
		})
	}

	// 2. Query Port
	if p, ok := parsedPorts["queryport"]; ok {
		probes = append(probes, PortProbe{
			Port:        p,
			Protocol:    "UDP",
			Description: "Query Port",
		})
	}

	// 3. RCON Port
	if p, ok := parsedPorts["rconport"]; ok {
		probes = append(probes, PortProbe{
			Port:        p,
			Protocol:    "TCP",
			Description: "RCON Port",
		})
	}

	// 4. App Port
	if p, ok := parsedPorts["appport"]; ok {
		probes = append(probes, PortProbe{
			Port:        p,
			Protocol:    "TCP",
			Description: "App Port",
		})
	}

	// 5. SourceTV Port
	if p, ok := parsedPorts["sourcetvport"]; ok {
		probes = append(probes, PortProbe{
			Port:        p,
			Protocol:    "UDP",
			Description: "SourceTV Port",
		})
	}

	// Heuristics for missing Query Port for Steam/UDP games
	hasQuery := false
	for _, p := range probes {
		if p.Description == "Query Port" {
			hasQuery = true
			break
		}
	}

	if !hasQuery && game != "minecraft" && game != "mc" {
		if p, ok := parsedPorts["port"]; ok {
			probes = append(probes, PortProbe{
				Port:        p + 1,
				Protocol:    "UDP",
				Description: "Steam Query Port (Auto-detected)",
			})
			if p != 27015 && p+1 != 27015 {
				probes = append(probes, PortProbe{
					Port:        27015,
					Protocol:    "UDP",
					Description: "Default Query Port (Auto-detected)",
				})
			}
		}
	}

	return probes
}

func cleanupLeftoverSudoers() {
	files, err := filepath.Glob("/etc/sudoers.d/lgsm-*")
	if err == nil {
		for _, f := range files {
			_ = os.Remove(f)
		}
	}
}

// Backup Management Implementations

func (im *InstanceManager) getBackupDirsForServer(srv *GameServerInstance) []string {
	user := srv.User
	dirs := []string{
		filepath.Join("/home", user, "lgsm", "backup"),
		filepath.Join("/home", user, "lgsm", "backups"),
		filepath.Join("/home", user, "backup"),
		filepath.Join("/home", user, "backups"),
	}

	// Read backupdir from config files (priority: <script>.cfg -> common.cfg -> _default.cfg)
	configDir := filepath.Join("/home", user, "lgsm", "config-lgsm", srv.Script)
	filesToTry := []string{
		filepath.Join(configDir, fmt.Sprintf("%s.cfg", srv.Script)),
		filepath.Join(configDir, "common.cfg"),
		filepath.Join(configDir, "_default.cfg"),
	}

	backupDirRegex := regexp.MustCompile(`^\s*backupdir\s*=\s*["']?([^"']+)["']?`)

	for _, fp := range filesToTry {
		file, err := os.Open(fp)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(file)
		var customDir string
		for scanner.Scan() {
			matches := backupDirRegex.FindStringSubmatch(scanner.Text())
			if len(matches) > 1 {
				customDir = strings.TrimSpace(matches[1])
				break
			}
		}
		file.Close()

		if customDir != "" {
			// Resolve variables in customDir:
			customDir = strings.ReplaceAll(customDir, "${lgsmdir}", filepath.Join("/home", user, "lgsm"))
			customDir = strings.ReplaceAll(customDir, "$lgsmdir", filepath.Join("/home", user, "lgsm"))
			customDir = strings.ReplaceAll(customDir, "${rootdir}", filepath.Join("/home", user))
			customDir = strings.ReplaceAll(customDir, "$rootdir", filepath.Join("/home", user))
			customDir = strings.ReplaceAll(customDir, "${username}", user)
			customDir = strings.ReplaceAll(customDir, "$username", user)
			customDir = strings.ReplaceAll(customDir, "${selfname}", srv.Script)
			customDir = strings.ReplaceAll(customDir, "$selfname", srv.Script)

			customDir = filepath.Clean(customDir)

			// Prepend to directories list so it is searched first
			dirs = append([]string{customDir}, dirs...)
			break
		}
	}

	// Remove duplicates and keep order
	seen := make(map[string]bool)
	var uniqueDirs []string
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			uniqueDirs = append(uniqueDirs, d)
		}
	}

	return uniqueDirs
}

func (im *InstanceManager) getBackupFilePath(srv *GameServerInstance, filename string) (string, error) {
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.Contains(filename, "..") {
		return "", fmt.Errorf("invalid backup filename")
	}
	if !strings.HasSuffix(filename, ".tar.gz") {
		return "", fmt.Errorf("invalid backup file extension")
	}

	dirs := im.getBackupDirsForServer(srv)
	for _, dir := range dirs {
		path := filepath.Join(dir, filename)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("backup file not found")
}

func (im *InstanceManager) ListBackups(serverID string) ([]BackupFile, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return nil, fmt.Errorf("server not found")
	}

	var backups []BackupFile

	dirs := im.getBackupDirsForServer(srv)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tar.gz") {
				info, err := entry.Info()
				if err != nil {
					continue
				}
				backups = append(backups, BackupFile{
					Name: entry.Name(),
					Size: info.Size(),
					Date: info.ModTime(),
					Path: filepath.Join(dir, entry.Name()),
				})
			}
		}
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Date.After(backups[j].Date)
	})

	return backups, nil
}

func (im *InstanceManager) DeleteBackup(serverID, fileName string) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	filePath, err := im.getBackupFilePath(srv, fileName)
	if err != nil {
		return err
	}

	return os.Remove(filePath)
}

func (im *InstanceManager) GetBackupPath(serverID, fileName string) (string, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return "", fmt.Errorf("server not found")
	}

	return im.getBackupFilePath(srv, fileName)
}

func (im *InstanceManager) UploadBackup(serverID string, filename string, fileReader io.Reader) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	if !strings.HasSuffix(filename, ".tar.gz") {
		return fmt.Errorf("invalid backup file extension, only .tar.gz is allowed")
	}
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.Contains(filename, "..") {
		return fmt.Errorf("invalid backup filename")
	}

	dirs := im.getBackupDirsForServer(srv)
	var targetDir string
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err == nil {
			targetDir = dir
			break
		}
	}
	if targetDir == "" {
		targetDir = dirs[0]
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return err
		}
		_ = exec.Command("chown", fmt.Sprintf("%s:%s", srv.User, srv.User), targetDir).Run()
	}

	destPath := filepath.Join(targetDir, filename)
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, fileReader)
	if err != nil {
		return err
	}

	_ = exec.Command("chown", fmt.Sprintf("%s:%s", srv.User, srv.User), destPath).Run()

	return nil
}

func getCrontab(user string) (string, error) {
	cmd := exec.Command("crontab", "-u", user, "-l")
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

func saveCrontab(user string, content string) error {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		cmd := exec.Command("crontab", "-u", user, "-r")
		_ = cmd.Run()
		return nil
	}
	cmd := exec.Command("crontab", "-u", user, "-")
	cmd.Stdin = strings.NewReader(trimmed + "\n")
	return cmd.Run()
}

func (im *InstanceManager) GetBackupSettings(serverID string) (BackupSettings, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return BackupSettings{}, fmt.Errorf("server not found")
	}

	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	commonPath := filepath.Join(configDir, "common.cfg")
	instancePath := filepath.Join(configDir, fmt.Sprintf("%s.cfg", srv.Script))

	settings := BackupSettings{
		MaxBackups:    "",
		MaxBackupDays: "",
		StopOnBackup:  "",
	}

	parseFile := func(path string) {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			return
		}
		lines := strings.Split(string(contentBytes), "\n")
		re := regexp.MustCompile(`^\s*(maxbackups|maxbackupdays|stoponbackup)\s*=\s*["']?([^"'\s#]*)["']?`)
		for _, line := range lines {
			matches := re.FindStringSubmatch(line)
			if len(matches) > 2 {
				key := matches[1]
				val := matches[2]
				switch key {
				case "maxbackups":
					settings.MaxBackups = val
				case "maxbackupdays":
					settings.MaxBackupDays = val
				case "stoponbackup":
					settings.StopOnBackup = val
				}
			}
		}
	}

	parseFile(commonPath)
	parseFile(instancePath)

	// Read and parse crontab for automatic backups
	cronExpr := ""
	cronEnabled := false
	if crontabStr, err := getCrontab(srv.User); err == nil {
		lines := strings.Split(crontabStr, "\n")
		reCron := regexp.MustCompile(`^([^/]+)(/\S+\s+backup.*)`)
		for _, line := range lines {
			if strings.Contains(line, srv.Script) && strings.Contains(line, "backup") {
				matches := reCron.FindStringSubmatch(strings.TrimSpace(line))
				if len(matches) > 2 {
					cronExpr = strings.TrimSpace(matches[1])
					cronEnabled = true
					break
				}
			}
		}
	}
	settings.AutoBackupEnabled = cronEnabled
	settings.AutoBackupCron = cronExpr

	return settings, nil
}

func (im *InstanceManager) SaveBackupSettings(serverID string, settings BackupSettings) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	instancePath := filepath.Join(configDir, fmt.Sprintf("%s.cfg", srv.Script))

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	var content string
	if contentBytes, err := os.ReadFile(instancePath); err == nil {
		content = string(contentBytes)
	}

	lines := strings.Split(content, "\n")
	updated := map[string]bool{
		"maxbackups":    false,
		"maxbackupdays": false,
		"stoponbackup":  false,
	}

	reMaxBackups := regexp.MustCompile(`^\s*maxbackups\s*=`)
	reMaxBackupDays := regexp.MustCompile(`^\s*maxbackupdays\s*=`)
	reStopOnBackup := regexp.MustCompile(`^\s*stoponbackup\s*=`)

	for i, line := range lines {
		if reMaxBackups.MatchString(line) {
			lines[i] = fmt.Sprintf("maxbackups=\"%s\"", settings.MaxBackups)
			updated["maxbackups"] = true
		} else if reMaxBackupDays.MatchString(line) {
			lines[i] = fmt.Sprintf("maxbackupdays=\"%s\"", settings.MaxBackupDays)
			updated["maxbackupdays"] = true
		} else if reStopOnBackup.MatchString(line) {
			lines[i] = fmt.Sprintf("stoponbackup=\"%s\"", settings.StopOnBackup)
			updated["stoponbackup"] = true
		}
	}

	if !updated["maxbackups"] && settings.MaxBackups != "" {
		lines = append(lines, fmt.Sprintf("maxbackups=\"%s\"", settings.MaxBackups))
	}
	if !updated["maxbackupdays"] && settings.MaxBackupDays != "" {
		lines = append(lines, fmt.Sprintf("maxbackupdays=\"%s\"", settings.MaxBackupDays))
	}
	if !updated["stoponbackup"] && settings.StopOnBackup != "" {
		lines = append(lines, fmt.Sprintf("stoponbackup=\"%s\"", settings.StopOnBackup))
	}

	newContent := strings.Join(lines, "\n")
	err := os.WriteFile(instancePath, []byte(newContent), 0644)
	if err != nil {
		return err
	}

	// Update crontab
	if crontabStr, err := getCrontab(srv.User); err == nil {
		lines := strings.Split(crontabStr, "\n")
		var newLines []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.Contains(trimmed, srv.Script) && strings.Contains(trimmed, "backup") {
				continue
			}
			newLines = append(newLines, line)
		}

		if settings.AutoBackupEnabled && settings.AutoBackupCron != "" {
			newLine := fmt.Sprintf("%s /home/%s/%s backup > /dev/null 2>&1", settings.AutoBackupCron, srv.User, srv.Script)
			newLines = append(newLines, newLine)
		}

		newCrontab := strings.Join(newLines, "\n")
		_ = saveCrontab(srv.User, newCrontab)
	}

	return nil
}

func (im *InstanceManager) RestoreBackup(w http.ResponseWriter, r *http.Request, serverID, fileName, lang string) {
	im.mu.Lock()
	srv := im.instances[serverID]

	if srv == nil {
		im.mu.Unlock()
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	oldStatus := srv.Status
	srv.Status = "updating"
	im.mu.Unlock()

	updateStatus := func(newStatus string) {
		im.mu.Lock()
		defer im.mu.Unlock()
		if s, ok := im.instances[serverID]; ok {
			s.Status = newStatus
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		updateStatus(oldStatus)
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(msgType string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}

	logLine := func(text string) {
		sendSSE("message", map[string]interface{}{
			"type": "log",
			"text": text,
		})
	}

	filePath, err := im.getBackupFilePath(srv, fileName)
	if err != nil {
		updateStatus(oldStatus)
		sendSSE("message", map[string]interface{}{"type": "log", "text": "Error finding backup: " + err.Error()})
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
		return
	}

	go func() {
		defer func() {
			im.ScanInstances()
			updateStatus("stopped")
		}()

		if oldStatus == "running" {
			logLine(Msg(lang, "Stopping game server before restore...", "Stoppe Gameserver vor der Wiederherstellung..."))
			execCmd := fmt.Sprintf("cd /home/%s && ./%s stop", srv.User, srv.Script)
			cmd := exec.Command("runuser", "-l", srv.User, "-c", execCmd)
			_ = cmd.Run()
		}

		logLine(Msg(lang, fmt.Sprintf("Restoring backup archive %s...", fileName), fmt.Sprintf("Stelle Backup-Archiv %s wieder her...", fileName)))

		tarCmd := fmt.Sprintf("tar -xzf %s -C /home/%s", filePath, srv.User)
		cmd := exec.Command("runuser", "-l", srv.User, "-c", tarCmd)

		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf

		err := cmd.Run()
		if err != nil {
			logLine(Msg(lang, "Error during restore: ", "Fehler bei der Wiederherstellung: ") + err.Error())
			logLine(errBuf.String())
			sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
			return
		}

		logLine(Msg(lang, "Backup files extracted successfully.", "Backup-Dateien erfolgreich entpackt."))

		if oldStatus == "running" {
			logLine(Msg(lang, "Restarting game server...", "Starte Gameserver neu..."))
			execCmd := fmt.Sprintf("cd /home/%s && ./%s start", srv.User, srv.Script)
			cmd := exec.Command("runuser", "-l", srv.User, "-c", execCmd)
			_ = cmd.Run()
			updateStatus("running")
		} else {
			updateStatus("stopped")
		}

		logLine(Msg(lang, "Restore process completed successfully.", "Wiederherstellung erfolgreich abgeschlossen."))
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 0})
	}()
}

type AlertSettings struct {
	DiscordEnabled      bool   `json:"discord_enabled"`
	DiscordWebhook      string `json:"discord_webhook"`
	TelegramEnabled     bool   `json:"telegram_enabled"`
	TelegramToken       string `json:"telegram_token"`
	TelegramChatID      string `json:"telegram_chatid"`
	EmailEnabled        bool   `json:"email_enabled"`
	EmailSMTP           string `json:"email_smtp"`
	EmailPort           string `json:"email_port"`
	EmailUser           string `json:"email_user"`
	EmailPass           string `json:"email_pass"`
	EmailDest           string `json:"email_dest"`
	MatrixEnabled       bool   `json:"matrix_enabled"`
	MatrixHomeserver    string `json:"matrix_homeserver"`
	MatrixRoomID        string `json:"matrix_roomid"`
	MatrixAccessToken   string `json:"matrix_accesstoken"`
	NtfyEnabled         bool   `json:"ntfy_enabled"`
	NtfyURL             string `json:"ntfy_url"`
	NtfyTopic           string `json:"ntfy_topic"`
	NtfyAuthToken       string `json:"ntfy_authtoken"`
	SlackEnabled        bool   `json:"slack_enabled"`
	SlackWebhook        string `json:"slack_webhook"`
	PushoverEnabled     bool   `json:"pushover_enabled"`
	PushoverToken       string `json:"pushover_token"`
	PushoverUser        string `json:"pushover_user"`
	PushbulletEnabled   bool   `json:"pushbullet_enabled"`
	PushbulletToken     string `json:"pushbullet_token"`
	PushbulletChannel   string `json:"pushbullet_channel"`
	IftttEnabled        bool   `json:"ifttt_enabled"`
	IftttKey            string `json:"ifttt_key"`
	IftttEvent          string `json:"ifttt_event"`
	RocketchatEnabled   bool   `json:"rocketchat_enabled"`
	RocketchatWebhook   string `json:"rocketchat_webhook"`
}

func (im *InstanceManager) GetAlertSettings(serverID string) (AlertSettings, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return AlertSettings{}, fmt.Errorf("server not found")
	}

	rootScript := strings.Split(srv.Script, "-")[0]
	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	rootConfigDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", rootScript)

	commonPath := filepath.Join(configDir, "common.cfg")
	rootCommonPath := filepath.Join(rootConfigDir, "common.cfg")
	instancePath := filepath.Join(configDir, fmt.Sprintf("%s.cfg", srv.Script))
	rootInstancePath := filepath.Join(rootConfigDir, fmt.Sprintf("%s.cfg", srv.Script))

	settings := AlertSettings{}

	parseFile := func(path string) {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			return
		}
		lines := strings.Split(string(contentBytes), "\n")
		re := regexp.MustCompile(`^\s*([a-zA-Z0-9]+)\s*=\s*["']?([^"'\s#]*)["']?`)
		for _, line := range lines {
			matches := re.FindStringSubmatch(line)
			if len(matches) > 2 {
				key := strings.ToLower(matches[1])
				val := matches[2]
				switch key {
				case "discordalert":
					settings.DiscordEnabled = (val == "on")
				case "discordwebhook":
					settings.DiscordWebhook = val
				case "telegramalert":
					settings.TelegramEnabled = (val == "on")
				case "telegramtoken":
					settings.TelegramToken = val
				case "telegramchatid":
					settings.TelegramChatID = val
				case "emailalert":
					settings.EmailEnabled = (val == "on")
				case "emailserver":
					settings.EmailSMTP = val
				case "emailport":
					settings.EmailPort = val
				case "emailuser":
					settings.EmailUser = val
				case "emailpassword":
					settings.EmailPass = val
				case "emaildest":
					settings.EmailDest = val
				case "matrixalert":
					settings.MatrixEnabled = (val == "on")
				case "matrixhomeserver":
					settings.MatrixHomeserver = val
				case "matrixroomid":
					settings.MatrixRoomID = val
				case "matrixaccesstoken":
					settings.MatrixAccessToken = val
				case "ntfyalert":
					settings.NtfyEnabled = (val == "on")
				case "ntfyurl":
					settings.NtfyURL = val
				case "ntfytopic":
					settings.NtfyTopic = val
				case "ntfyauthtoken":
					settings.NtfyAuthToken = val
				case "slackalert":
					settings.SlackEnabled = (val == "on")
				case "slackwebhook":
					settings.SlackWebhook = val
				case "pushoveralert":
					settings.PushoverEnabled = (val == "on")
				case "pushovertoken":
					settings.PushoverToken = val
				case "pushoveruser":
					settings.PushoverUser = val
				case "pushbulletalert":
					settings.PushbulletEnabled = (val == "on")
				case "pushbullettoken":
					settings.PushbulletToken = val
				case "pushbulletchannel":
					settings.PushbulletChannel = val
				case "iftttalert":
					settings.IftttEnabled = (val == "on")
				case "iftttkey":
					settings.IftttKey = val
				case "iftttevent":
					settings.IftttEvent = val
				case "rocketchatalert":
					settings.RocketchatEnabled = (val == "on")
				case "rocketchatwebhook":
					settings.RocketchatWebhook = val
				}
			}
		}
	}

	parseFile(rootCommonPath)
	parseFile(commonPath)
	parseFile(rootInstancePath)
	parseFile(instancePath)

	return settings, nil
}

func (im *InstanceManager) SaveAlertSettings(serverID string, settings AlertSettings) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	rootScript := strings.Split(srv.Script, "-")[0]
	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	rootConfigDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", rootScript)

	instancePaths := []string{
		filepath.Join(configDir, fmt.Sprintf("%s.cfg", srv.Script)),
	}
	if rootScript != srv.Script {
		instancePaths = append(instancePaths, filepath.Join(rootConfigDir, fmt.Sprintf("%s.cfg", srv.Script)))
	}

	_ = os.MkdirAll(configDir, 0755)
	if rootScript != srv.Script {
		_ = os.MkdirAll(rootConfigDir, 0755)
	}

	valMap := map[string]string{
		"discordalert":      "off",
		"discordwebhook":    settings.DiscordWebhook,
		"telegramalert":     "off",
		"telegramtoken":     settings.TelegramToken,
		"telegramchatid":    settings.TelegramChatID,
		"emailalert":        "off",
		"emailserver":       settings.EmailSMTP,
		"emailport":         settings.EmailPort,
		"emailuser":         settings.EmailUser,
		"emailpassword":     settings.EmailPass,
		"emaildest":         settings.EmailDest,
		"matrixalert":       "off",
		"matrixhomeserver":  settings.MatrixHomeserver,
		"matrixroomid":      settings.MatrixRoomID,
		"matrixaccesstoken": settings.MatrixAccessToken,
		"ntfyalert":         "off",
		"ntfyurl":           settings.NtfyURL,
		"ntfytopic":          settings.NtfyTopic,
		"ntfyauthtoken":     settings.NtfyAuthToken,
		"slackalert":        "off",
		"slackwebhook":      settings.SlackWebhook,
		"pushoveralert":     "off",
		"pushovertoken":     settings.PushoverToken,
		"pushoveruser":      settings.PushoverUser,
		"pushbulletalert":   "off",
		"pushbullettoken":   settings.PushbulletToken,
		"pushbulletchannel": settings.PushbulletChannel,
		"iftttalert":        "off",
		"iftttkey":          settings.IftttKey,
		"iftttevent":        settings.IftttEvent,
		"rocketchatalert":   "off",
		"rocketchatwebhook": settings.RocketchatWebhook,
	}

	if settings.DiscordEnabled {
		valMap["discordalert"] = "on"
	}
	if settings.TelegramEnabled {
		valMap["telegramalert"] = "on"
	}
	if settings.EmailEnabled {
		valMap["emailalert"] = "on"
	}
	if settings.MatrixEnabled {
		valMap["matrixalert"] = "on"
	}
	if settings.NtfyEnabled {
		valMap["ntfyalert"] = "on"
	}
	if settings.SlackEnabled {
		valMap["slackalert"] = "on"
	}
	if settings.PushoverEnabled {
		valMap["pushoveralert"] = "on"
	}
	if settings.PushbulletEnabled {
		valMap["pushbulletalert"] = "on"
	}
	if settings.IftttEnabled {
		valMap["iftttalert"] = "on"
	}
	if settings.RocketchatEnabled {
		valMap["rocketchatalert"] = "on"
	}

	for _, instancePath := range instancePaths {
		var content string
		if contentBytes, err := os.ReadFile(instancePath); err == nil {
			content = string(contentBytes)
		}

		lines := strings.Split(content, "\n")
		updated := make(map[string]bool)

		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			for key, val := range valMap {
				re := regexp.MustCompile(fmt.Sprintf(`^\s*%s\s*=`, key))
				if re.MatchString(trimmed) {
					lines[i] = fmt.Sprintf("%s=\"%s\"", key, val)
					updated[key] = true
				}
			}
		}

		for key, val := range valMap {
			if !updated[key] {
				lines = append(lines, fmt.Sprintf("%s=\"%s\"", key, val))
			}
		}

		newContent := strings.Join(lines, "\n")
		_ = os.WriteFile(instancePath, []byte(newContent), 0644)
		_ = exec.Command("chown", fmt.Sprintf("%s:%s", srv.User, srv.User), instancePath).Run()
	}

	return nil
}

func (im *InstanceManager) InstallSystemdService(serverID string) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	systemdCode := fmt.Sprintf(`[Unit]
Description=LinuxGSM %s Server
After=network.target

[Service]
Type=simple
User=%s
WorkingDirectory=/home/%s
ExecStart=/home/%s/%s start
ExecStop=/home/%s/%s stop
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target`, srv.Name, srv.User, srv.User, srv.User, srv.Script, srv.User, srv.Script)

	servicePath := fmt.Sprintf("/etc/systemd/system/linuxgsm-%s.service", srv.Script)
	if err := os.WriteFile(servicePath, []byte(systemdCode), 0644); err != nil {
		return err
	}

	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "enable", fmt.Sprintf("linuxgsm-%s.service", srv.Script)).Run()

	return nil
}

func (im *InstanceManager) InstallCronjobs(serverID string) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	crons := []string{
		fmt.Sprintf("*/5 * * * * /home/%s/%s monitor > /dev/null 2>&1", srv.User, srv.Script),
		fmt.Sprintf("*/30 * * * * /home/%s/%s update > /dev/null 2>&1", srv.User, srv.Script),
		fmt.Sprintf("30 4 * * * /home/%s/%s force-update > /dev/null 2>&1", srv.User, srv.Script),
		fmt.Sprintf("0 0 * * 0 /home/%s/%s update-lgsm > /dev/null 2>&1", srv.User, srv.Script),
	}

	crontabStr, _ := getCrontab(srv.User)
	lines := strings.Split(crontabStr, "\n")
	var newLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, srv.Script) && (strings.Contains(trimmed, "monitor") || strings.Contains(trimmed, "update") || strings.Contains(trimmed, "force-update") || strings.Contains(trimmed, "update-lgsm")) {
			continue
		}
		newLines = append(newLines, line)
	}

	newLines = append(newLines, fmt.Sprintf("# LinuxGSM %s Geplante Wartungsaufgaben", srv.Name))
	newLines = append(newLines, crons...)

	newCrontab := strings.Join(newLines, "\n")
	return saveCrontab(srv.User, newCrontab)
}

const serverTagsFilePath = "server_tags.json"

func loadServerTags() map[string]string {
	tags := make(map[string]string)
	data, err := os.ReadFile(serverTagsFilePath)
	if err != nil {
		return tags
	}
	_ = json.Unmarshal(data, &tags)
	return tags
}

func saveServerTags(tags map[string]string) error {
	data, err := json.MarshalIndent(tags, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(serverTagsFilePath, data, 0644)
}

func (im *InstanceManager) SetServerTag(serverID string, tag string) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	srv, ok := im.instances[serverID]
	if !ok {
		return fmt.Errorf("server not found")
	}

	srv.Tag = strings.TrimSpace(tag)
	tags := loadServerTags()
	if srv.Tag == "" {
		delete(tags, serverID)
	} else {
		tags[serverID] = srv.Tag
	}
	return saveServerTags(tags)
}

func (im *InstanceManager) RunActionDirect(serverID, action string) error {
	im.mu.Lock()
	srv, ok := im.instances[serverID]
	im.mu.Unlock()

	if !ok {
		return fmt.Errorf("server not found")
	}

	user := srv.User
	script := srv.Script
	execCmd := fmt.Sprintf("cd /home/%s && ./%s %s", user, script, action)
	cmd := exec.Command("runuser", "-l", user, "-c", execCmd)
	return cmd.Run()
}

func (im *InstanceManager) RunBulkAction(action string, serverIDs []string) map[string]string {
	results := make(map[string]string)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, sid := range serverIDs {
		wg.Add(1)
		go func(serverID string) {
			defer wg.Done()
			err := im.RunActionDirect(serverID, action)
			mu.Lock()
			if err != nil {
				results[serverID] = "Error: " + err.Error()
			} else {
				results[serverID] = "Success"
			}
			mu.Unlock()
		}(sid)
	}
	wg.Wait()
	im.ScanInstances()
	return results
}

func (im *InstanceManager) GetCronSchedule(serverID string) (CronSchedule, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return CronSchedule{}, fmt.Errorf("server not found")
	}

	sched := CronSchedule{}
	crontabStr, _ := getCrontab(srv.User)
	lines := strings.Split(crontabStr, "\n")

	reMonitor := regexp.MustCompile(fmt.Sprintf(`\*/(\d+)\s+\*\s+\*\s+\*\s+\*\s+/home/%s/%s\s+monitor`, srv.User, srv.Script))
	reUpdate := regexp.MustCompile(fmt.Sprintf(`\*/(\d+)\s+\*\s+\*\s+\*\s+\*\s+/home/%s/%s\s+update`, srv.User, srv.Script))
	reRestart := regexp.MustCompile(fmt.Sprintf(`(\d+)\s+(\d+)\s+\*\s+\*\s+\*\s+/home/%s/%s\s+force-update`, srv.User, srv.Script))
	reCore := regexp.MustCompile(fmt.Sprintf(`0\s+0\s+\*\s+\*\s+(\d+)\s+/home/%s/%s\s+update-lgsm`, srv.User, srv.Script))

	for _, line := range lines {
		if m := reMonitor.FindStringSubmatch(line); len(m) > 1 {
			sched.MonitorInterval = m[1]
		}
		if m := reUpdate.FindStringSubmatch(line); len(m) > 1 {
			sched.UpdateInterval = m[1]
		}
		if m := reRestart.FindStringSubmatch(line); len(m) > 2 {
			hour, _ := strconv.Atoi(m[2])
			min, _ := strconv.Atoi(m[1])
			sched.RestartTime = fmt.Sprintf("%02d:%02d", hour, min)
		}
		if m := reCore.FindStringSubmatch(line); len(m) > 1 {
			sched.CoreUpdateDay = m[1]
		}
	}

	return sched, nil
}

func (im *InstanceManager) SaveCronSchedule(serverID string, sched CronSchedule) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	scriptPath := fmt.Sprintf("/home/%s/%s", srv.User, srv.Script)
	var crons []string

	if sched.MonitorInterval != "" {
		crons = append(crons, fmt.Sprintf("*/%s * * * * %s monitor > /dev/null 2>&1", sched.MonitorInterval, scriptPath))
	}
	if sched.UpdateInterval != "" {
		crons = append(crons, fmt.Sprintf("*/%s * * * * %s update > /dev/null 2>&1", sched.UpdateInterval, scriptPath))
	}
	if sched.RestartTime != "" {
		parts := strings.Split(sched.RestartTime, ":")
		if len(parts) == 2 {
			crons = append(crons, fmt.Sprintf("%s %s * * * %s force-update > /dev/null 2>&1", parts[1], parts[0], scriptPath))
		}
	}
	if sched.CoreUpdateDay != "" {
		crons = append(crons, fmt.Sprintf("0 0 * * %s %s update-lgsm > /dev/null 2>&1", sched.CoreUpdateDay, scriptPath))
	}

	crontabStr, _ := getCrontab(srv.User)
	lines := strings.Split(crontabStr, "\n")
	var newLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, srv.Script) && (strings.Contains(trimmed, "monitor") || strings.Contains(trimmed, "update") || strings.Contains(trimmed, "force-update") || strings.Contains(trimmed, "update-lgsm")) {
			continue
		}
		newLines = append(newLines, line)
	}

	newLines = append(newLines, fmt.Sprintf("# LinuxGSM %s Geplante Wartungsaufgaben", srv.Name))
	newLines = append(newLines, crons...)

	newCrontab := strings.Join(newLines, "\n")
	return saveCrontab(srv.User, newCrontab)
}

// -------------------------------------------------------------
// Stop Mode Configurator
// -------------------------------------------------------------

func (im *InstanceManager) GetStopModeSettings(serverID string) (string, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return "", fmt.Errorf("server not found")
	}

	rootScript := strings.Split(srv.Script, "-")[0]
	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	rootConfigDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", rootScript)

	commonPath := filepath.Join(configDir, "common.cfg")
	rootCommonPath := filepath.Join(rootConfigDir, "common.cfg")
	instancePath := filepath.Join(configDir, fmt.Sprintf("%s.cfg", srv.Script))
	rootInstancePath := filepath.Join(rootConfigDir, fmt.Sprintf("%s.cfg", srv.Script))

	stopMode := "default"
	parseFile := func(path string) {
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			return
		}
		lines := strings.Split(string(contentBytes), "\n")
		re := regexp.MustCompile(`^\s*stopmode\s*=\s*["']?([^"'\s#]*)["']?`)
		for _, line := range lines {
			matches := re.FindStringSubmatch(line)
			if len(matches) > 1 {
				val := matches[1]
				if val != "" {
					stopMode = val
				}
			}
		}
	}

	parseFile(rootCommonPath)
	parseFile(commonPath)
	parseFile(rootInstancePath)
	parseFile(instancePath)
	return stopMode, nil
}

func (im *InstanceManager) SaveStopModeSettings(serverID string, stopMode string) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	rootScript := strings.Split(srv.Script, "-")[0]
	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	rootConfigDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", rootScript)

	instancePaths := []string{
		filepath.Join(configDir, fmt.Sprintf("%s.cfg", srv.Script)),
	}
	if rootScript != srv.Script {
		instancePaths = append(instancePaths, filepath.Join(rootConfigDir, fmt.Sprintf("%s.cfg", srv.Script)))
	}

	_ = os.MkdirAll(configDir, 0755)
	if rootScript != srv.Script {
		_ = os.MkdirAll(rootConfigDir, 0755)
	}

	for _, instancePath := range instancePaths {
		var content string
		if contentBytes, err := os.ReadFile(instancePath); err == nil {
			content = string(contentBytes)
		}

		lines := strings.Split(content, "\n")
		updated := false
		reStopMode := regexp.MustCompile(`^\s*stopmode\s*=`)

		for i, line := range lines {
			if reStopMode.MatchString(line) {
				if stopMode == "default" || stopMode == "" {
					lines[i] = "# stopmode=\"\""
				} else {
					lines[i] = fmt.Sprintf("stopmode=\"%s\"", stopMode)
				}
				updated = true
				break
			}
		}

		if !updated && stopMode != "default" && stopMode != "" {
			lines = append(lines, fmt.Sprintf("stopmode=\"%s\"", stopMode))
		}

		newContent := strings.Join(lines, "\n")
		_ = os.WriteFile(instancePath, []byte(newContent), 0644)
		_ = exec.Command("chown", fmt.Sprintf("%s:%s", srv.User, srv.User), instancePath).Run()
	}

	return nil
}

// -------------------------------------------------------------
// Multi-Instance Creator

func (im *InstanceManager) CreateMultiInstance(serverID string, suffix string) (string, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return "", fmt.Errorf("server not found")
	}

	cleanSuffix := strings.ToLower(strings.TrimSpace(suffix))
	if cleanSuffix == "" {
		return "", fmt.Errorf("suffix cannot be empty")
	}
	if matched, _ := regexp.MatchString(`^[a-z0-9-]+$`, cleanSuffix); !matched {
		return "", fmt.Errorf("suffix can only contain lowercase letters, numbers, and hyphens")
	}

	baseScript := srv.Script
	parts := strings.Split(baseScript, "-")
	rootScript := parts[0]

	newInstanceScript := fmt.Sprintf("%s-%s", rootScript, cleanSuffix)
	homeDir := filepath.Join("/home", srv.User)
	newScriptPath := filepath.Join(homeDir, newInstanceScript)
	rootScriptPath := filepath.Join(homeDir, rootScript)

	// If rootScriptPath doesn't exist, fallback to baseScript
	sourceScript := rootScript
	if _, err := os.Stat(rootScriptPath); err != nil {
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
	copyCmd := fmt.Sprintf("cd /home/%s && cp -p %s %s && chmod 0755 %s", srv.User, sourceScript, newInstanceScript, newInstanceScript)
	cmd := exec.Command("runuser", "-l", srv.User, "-c", copyCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create instance script: %s (%v)", string(out), err)
	}

	// 1. Create config in lgsm/config-lgsm/<rootScript>/<newInstanceScript>.cfg (LinuxGSM standard)
	rootConfigDir := filepath.Join(homeDir, "lgsm", "config-lgsm", rootScript)
	_ = os.MkdirAll(rootConfigDir, 0755)
	rootInstanceConfig := filepath.Join(rootConfigDir, fmt.Sprintf("%s.cfg", newInstanceScript))
	if _, err := os.Stat(rootInstanceConfig); os.IsNotExist(err) {
		initialConfig := fmt.Sprintf("## LinuxGSM Multi-Instance Config: %s\n## Generated by LinuxGSM Dashboard\n\n", newInstanceScript)
		_ = os.WriteFile(rootInstanceConfig, []byte(initialConfig), 0644)
		_ = exec.Command("chown", "-R", fmt.Sprintf("%s:%s", srv.User, srv.User), rootConfigDir).Run()
	}

	// 2. Also ensure lgsm/config-lgsm/<newInstanceScript>/<newInstanceScript>.cfg is available
	instanceConfigDir := filepath.Join(homeDir, "lgsm", "config-lgsm", newInstanceScript)
	_ = os.MkdirAll(instanceConfigDir, 0755)
	newInstanceConfig := filepath.Join(instanceConfigDir, fmt.Sprintf("%s.cfg", newInstanceScript))
	if _, err := os.Stat(newInstanceConfig); os.IsNotExist(err) {
		initialConfig := fmt.Sprintf("## LinuxGSM Multi-Instance Config: %s\n## Generated by LinuxGSM Dashboard\n\n", newInstanceScript)
		_ = os.WriteFile(newInstanceConfig, []byte(initialConfig), 0644)
		_ = exec.Command("chown", "-R", fmt.Sprintf("%s:%s", srv.User, srv.User), instanceConfigDir).Run()
	}

	im.ScanInstances()
	return newInstanceScript, nil
}

// -------------------------------------------------------------
// Cloud Backup (rclone) Integration
// -------------------------------------------------------------

type CloudBackupSettings struct {
	Enabled  bool   `json:"enabled"`
	Remote   string `json:"remote"`
	Path     string `json:"path"`
	AutoSync bool   `json:"auto_sync"`
}

func (im *InstanceManager) GetCloudBackupSettings(serverID string) (CloudBackupSettings, error) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return CloudBackupSettings{}, fmt.Errorf("server not found")
	}

	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	cloudConfigFile := filepath.Join(configDir, "cloud_backup.json")

	data, err := os.ReadFile(cloudConfigFile)
	if err != nil {
		return CloudBackupSettings{Enabled: false, Remote: "", Path: "backups", AutoSync: false}, nil
	}

	var settings CloudBackupSettings
	_ = json.Unmarshal(data, &settings)
	return settings, nil
}

func (im *InstanceManager) SaveCloudBackupSettings(serverID string, settings CloudBackupSettings) error {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	_ = os.MkdirAll(configDir, 0755)
	cloudConfigFile := filepath.Join(configDir, "cloud_backup.json")

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(cloudConfigFile, data, 0644); err != nil {
		return err
	}
	_ = exec.Command("chown", fmt.Sprintf("%s:%s", srv.User, srv.User), cloudConfigFile).Run()
	return nil
}

func (im *InstanceManager) SyncCloudBackup(w http.ResponseWriter, r *http.Request, serverID, lang string) {
	im.mu.Lock()
	srv := im.instances[serverID]
	im.mu.Unlock()

	if srv == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	settings, err := im.GetCloudBackupSettings(serverID)
	if err != nil || !settings.Enabled || settings.Remote == "" {
		http.Error(w, "Cloud backup not configured or disabled", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(msgType string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}

	logLine := func(text string) {
		sendSSE("message", map[string]interface{}{
			"type": "log",
			"text": text,
		})
	}

	go func() {
		logLine(Msg(lang, fmt.Sprintf("Starting cloud backup sync to %s:%s...", settings.Remote, settings.Path), fmt.Sprintf("Starte Cloud-Backup-Synchronisierung nach %s:%s...", settings.Remote, settings.Path)))

		backupDir := filepath.Join("/home", srv.User, "lgsm", "backup")
		if _, err := os.Stat(backupDir); os.IsNotExist(err) {
			backupDir = filepath.Join("/home", srv.User, "backup")
		}

		targetDest := fmt.Sprintf("%s:%s", settings.Remote, settings.Path)
		rcloneCmd := fmt.Sprintf("rclone copy %s %s -v", backupDir, targetDest)
		cmd := exec.Command("runuser", "-l", srv.User, "-c", rcloneCmd)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			logLine("Error creating stdout pipe: " + err.Error())
			sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
			return
		}
		cmd.Stderr = cmd.Stdout

		if err := cmd.Start(); err != nil {
			logLine(Msg(lang, "Error starting rclone (is rclone installed?): ", "Fehler beim Starten von rclone (ist rclone installiert?): ") + err.Error())
			sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
			return
		}

		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					logLine("Read error: " + err.Error())
				}
				break
			}
			logLine(strings.TrimRight(line, "\r\n"))
		}

		if err := cmd.Wait(); err != nil {
			logLine(Msg(lang, "Sync finished with error: ", "Synchronisierung mit Fehler beendet: ") + err.Error())
			sendSSE("message", map[string]interface{}{"type": "exit", "code": 1})
			return
		}

		logLine(Msg(lang, "Cloud backup sync completed successfully!", "Cloud-Backup-Synchronisierung erfolgreich abgeschlossen!"))
		sendSSE("message", map[string]interface{}{"type": "exit", "code": 0})
	}()
}
