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
	"strconv"
	"strings"
	"sync"
	"time"
)

type GameServerInstance struct {
	ID          string      `json:"id"`     // Username
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
}

type ConfigFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type InstanceManager struct {
	mu          sync.Mutex
	instances   map[string]*GameServerInstance
	isMock      bool
	mockServers []GameServerInstance
}

// Msg returns the English or German text depending on the language code.
func Msg(lang, en, de string) string {
	if lang == "de" {
		return de
	}
	return en
}

func NewInstanceManager(isMock bool) *InstanceManager {
	im := &InstanceManager{
		instances: make(map[string]*GameServerInstance),
		isMock:    isMock,
	}

	if isMock {
		im.mockServers = []GameServerInstance{
			{
				ID:     "arkserver",
				Name:   "Ark: Survival Evolved",
				User:   "arkserver",
				Script: "arkserver",
				Status: "running",
				Port:   7777,
				Game:   "ark",
				ParsedPorts: []PortProbe{
					{Port: 7777, Protocol: "UDP", Description: "Game"},
					{Port: 27015, Protocol: "UDP", Description: "Query"},
				},
			},
			{
				ID:     "vhserver",
				Name:   "Valheim",
				User:   "vhserver",
				Script: "vhserver",
				Status: "stopped",
				Port:   2456,
				Game:   "valheim",
				ParsedPorts: []PortProbe{
					{Port: 2456, Protocol: "UDP", Description: "Game"},
					{Port: 2457, Protocol: "UDP", Description: "Query"},
				},
			},
		}
	} else {
		cleanupLeftoverSudoers()
		im.ScanInstances()
	}

	return im
}

func (im *InstanceManager) GetInstances() []GameServerInstance {
	im.mu.Lock()
	defer im.mu.Unlock()

	if im.isMock {
		stats := GetMockSystemStats(im.mockServers)
		statsByID := make(map[string]ServerStats, len(stats.Servers))
		for _, stat := range stats.Servers {
			statsByID[stat.ID] = stat
		}

		list := make([]GameServerInstance, len(im.mockServers))
		for i, srv := range im.mockServers {
			list[i] = srv
			if stat, ok := statsByID[srv.ID]; ok {
				list[i].CPU = stat.CPU
				list[i].RAM = stat.RAM
				list[i].PIDs = stat.PIDs
			}
		}
		return list
	}

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
	if im.isMock {
		return
	}

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

	// Keep track of active statuses like 'installing' or 'updating'
	for k, v := range detected {
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

	for _, line := range lines {
		line = strings.TrimSpace(line)
		
		// 1. Try server name match
		if match := reName.FindStringSubmatch(line); len(match) > 1 {
			serverName = strings.TrimSpace(match[1])
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
	if !exists || im.isMock {
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
	if !exists || im.isMock {
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
		
		serverName, ports := parseDetailsOutput(string(outputBytes))
		
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
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}
	im.mu.Unlock()

	if srv == nil {
		return nil, fmt.Errorf("server %s not found", serverID)
	}

	if isMock {
		if srv.Status != "running" {
			return []string{Msg(lang, "[MOCK] Server is offline. tmux session is not active.", "[MOCK] Server ist offline. tmux Sitzung nicht aktiv.")}, nil
		}
		return []string{
			"=== MOCK CONSOLE OUTPUT ===",
			fmt.Sprintf("Status: running on port %d", srv.Port),
			fmt.Sprintf("OS: Mock OS (Windows) | Engine ID: %s", srv.Game),
			"SteamCMD client version 1.0.0 init...",
			"Initializing GameEngine Loop...",
			"Master Server registration: SUCCESS",
			"Active players: 3/32",
			"Tickrate: 30.1 Hz",
			"LOG: player [Admin] connected.",
			"LOG: player [Gamer1] connected.",
			"LOG: autosave triggered. Map saved.",
		}, nil
	}

	if mode == "tmux" {
		socketName, sessionName, ok := findLinuxGSMTmuxTarget(srv.User, srv.Script)
		if !ok {
			return []string{Msg(lang, "Server is offline. tmux session is not active.", "Server ist offline. tmux Sitzung nicht aktiv.")}, nil
		}

		// Capture tmux pane from the same socket/session pair that LinuxGSM uses.
		cmd := exec.Command("runuser", "-u", srv.User, "--", "tmux", "-L", socketName, "capture-pane", "-t", sessionName, "-p")
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			return []string{Msg(lang, "Failed to capture the tmux screen.", "Fehler beim Erfassen des tmux Bildschirms."), err.Error()}, nil
		}

		lines := strings.Split(out.String(), "\n")
		return lines, nil
	} else {
		// Read log file: look for logs under /home/<user>/log/console/<script>-console.log
		logPath := filepath.Join("/home", srv.User, "log", "console", fmt.Sprintf("%s-console.log", srv.Script))
		file, err := os.Open(logPath)
		if err != nil {
			// fallback check in lgsm log directory
			logPath = filepath.Join("/home", srv.User, "log", "script", fmt.Sprintf("%s-script.log", srv.Script))
			file, err = os.Open(logPath)
			if err != nil {
				return []string{
					Msg(lang, "Log file could not be opened.", "Logdatei konnte nicht geöffnet werden."),
					Msg(lang, "Searched path: ", "Gesuchter Pfad: ") + logPath,
				}, nil
			}
		}
		defer file.Close()

		// Get last 100 lines
		var lines []string
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			if len(lines) > 100 {
				lines = lines[1:]
			}
		}
		return lines, nil
	}
}

func (im *InstanceManager) GetConfigFiles(serverID string) ([]ConfigFile, error) {
	im.mu.Lock()
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}
	im.mu.Unlock()

	if srv == nil {
		return nil, fmt.Errorf("server not found")
	}

	if isMock {
		return []ConfigFile{
			{Name: "common.cfg", Path: "mock://common.cfg"},
			{Name: fmt.Sprintf("%s.cfg", srv.Script), Path: fmt.Sprintf("mock://%s.cfg", srv.Script)},
			{Name: "serverfiles/server.properties", Path: "mock://serverfiles/server.properties"},
			{Name: "serverfiles/server.cfg", Path: "mock://serverfiles/server.cfg"},
			{Name: "serverfiles/settings.json", Path: "mock://serverfiles/settings.json"},
		}, nil
	}

	var configs []ConfigFile

	// 1. Scan primary LinuxGSM config directory: /home/<user>/lgsm/config-lgsm/<script>/
	configDir := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	if files, err := os.ReadDir(configDir); err == nil {
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".cfg") {
				configs = append(configs, ConfigFile{
					Name: f.Name(),
					Path: filepath.Join(configDir, f.Name()),
				})
			}
		}
	}

	// 2. Scan gameserver files directory recursively: /home/<user>/serverfiles/
	serverfilesDir := filepath.Join("/home", srv.User, "serverfiles")
	if _, err := os.Stat(serverfilesDir); err == nil {
		found := findConfigsInDir(filepath.Join("/home", srv.User), serverfilesDir, 0)
		configs = append(configs, found...)
	}

	return configs, nil
}

func isPathWithinDirectories(path string, allowedDirs ...string) bool {
	cleanPath := filepath.Clean(path)
	for _, allowed := range allowedDirs {
		cleanAllowed := filepath.Clean(allowed)
		if cleanPath == cleanAllowed || strings.HasPrefix(cleanPath, cleanAllowed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (im *InstanceManager) GetConfigFileContent(serverID, path string) (string, error) {
	im.mu.Lock()
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}
	im.mu.Unlock()

	if srv == nil {
		return "", fmt.Errorf("server not found")
	}

	if isMock {
		if strings.HasSuffix(path, "common.cfg") {
			return `# Shared LinuxGSM Config Template for Mock Mode
ip="0.0.0.0"
port="2456"
queryport="27015"
maxplayers="16"
updateonstart="always"`, nil
		}
		if strings.HasSuffix(path, "server.properties") {
			return `# Minecraft server properties
# Tue Jul 07 14:30:00 CEST 2026
enable-jmx-monitoring=false
rcon.port=25575
level-seed=4815162342
gamemode=survival
enable-command-block=true
enable-query=false
generator-settings={}
query.port=25565
pvp=true
generate-structures=true
difficulty=easy
network-compression-threshold=256
max-players=20
require-resource-pack=false
use-native-transport=true
online-mode=true
server-port=25565
motd=Welcome to the LinuxGSM Minecraft Server!`, nil
		}
		if strings.HasSuffix(path, "server.cfg") {
			return `// Server configuration file
hostname "LinuxGSM Dedicated Server"
rcon_password "admin123"
sv_password ""
sv_maxplayers 16
sv_lan 0`, nil
		}
		if strings.HasSuffix(path, "settings.json") {
			return `{
  "ServerName": "My Palworld Server",
  "ServerPassword": "",
  "AdminPassword": "superadminpass",
  "Difficulty": "None",
  "DayTimeSpeedRate": 1.0,
  "NightTimeSpeedRate": 1.0,
  "ExpRate": 1.0,
  "PalCaptureRate": 1.0
}`, nil
		}
		return fmt.Sprintf(`# Instance Configuration for %s
servername="Sleek %s Server"
serverpassword="secretpassword"
adminpassword="adminpassword"
saveinterval="600"`, srv.Script, srv.Name), nil
	}

	// SECURITY CHECK: Ensure path is within allowed directories
	allowedDir1 := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	allowedDir2 := filepath.Join("/home", srv.User, "serverfiles")
	cleanPath := filepath.Clean(path)

	if !isPathWithinDirectories(cleanPath, allowedDir1, allowedDir2) {
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
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server not found")
	}

	if isMock {
		fmt.Printf("[MOCK SAVE] Saved config %s for %s\n", path, serverID)
		return nil
	}

	// SECURITY CHECK: Ensure path is within allowed directories
	allowedDir1 := filepath.Join("/home", srv.User, "lgsm", "config-lgsm", srv.Script)
	allowedDir2 := filepath.Join("/home", srv.User, "serverfiles")
	cleanPath := filepath.Clean(path)

	if !isPathWithinDirectories(cleanPath, allowedDir1, allowedDir2) {
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
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}

	if srv == nil {
		im.mu.Unlock()
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Set local state status to avoid concurrent triggers
	oldStatus := srv.Status
	if action == "update" {
		srv.Status = "updating"
	}
	im.mu.Unlock()

	// Callback to update status inside streaming routine
	updateStatus := func(newStatus string) {
		im.mu.Lock()
		defer im.mu.Unlock()
		if isMock {
			for i := range im.mockServers {
				if im.mockServers[i].ID == serverID {
					im.mockServers[i].Status = newStatus
					break
				}
			}
		} else {
			if s, ok := im.instances[serverID]; ok {
				s.Status = newStatus
			}
		}
	}

	if isMock {
		StreamMockAction(w, r, action, serverID, updateStatus)
		return
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
	if im.isMock {
		addServerCallback := func(srv GameServerInstance) {
			im.mu.Lock()
			defer im.mu.Unlock()
			im.mockServers = append(im.mockServers, srv)
		}
		StreamMockInstall(w, r, gameCmd, username, addServerCallback)
		return
	}

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
	configDir := filepath.Join(homeDir, "lgsm", "config-lgsm", scriptName)
	filesToTry := []string{
		filepath.Join(configDir, fmt.Sprintf("%s.cfg", scriptName)),
		filepath.Join(configDir, "common.cfg"),
		filepath.Join(configDir, "_default.cfg"),
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
	if scriptName == "mtaserver" {
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

	if scriptName == "mcserver" || scriptName == "minecraft" {
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
	// Regular expression to strip ANSI escape codes
	const ansi = "[\u001B\u009B][[()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-ORZcf-nqry=><]"
	re := regexp.MustCompile(ansi)
	return re.ReplaceAllString(str, "")
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
	clean := strings.Split(scriptName, "-")[0]
	return strings.TrimSuffix(clean, "server")
}

func (im *InstanceManager) SendConsoleCommand(serverID string, command string) error {
	im.mu.Lock()
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}
	im.mu.Unlock()

	if srv == nil {
		return fmt.Errorf("server %s not found", serverID)
	}

	if isMock {
		fmt.Printf("[MOCK CONSOLE INPUT] Server %s received command: %s\n", serverID, command)
		return nil
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
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}
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
	if isMock {
		// Mock responses
		if len(srv.ParsedPorts) > 0 {
			for _, pp := range srv.ParsedPorts {
				probes = append(probes, PortProbe{
					Port:        pp.Port,
					Protocol:    pp.Protocol,
					Open:        srv.Status == "running",
					Description: pp.Description,
				})
			}
		} else {
			probes = []PortProbe{
				{Port: srv.Port, Protocol: "TCP", Open: srv.Status == "running", Description: "Game Port (TCP)"},
				{Port: srv.Port, Protocol: "UDP", Open: srv.Status == "running", Description: "Game Query Port (UDP)"},
			}
			if srv.Game != "minecraft" {
				probes = append(probes, PortProbe{Port: srv.Port + 1, Protocol: "UDP", Open: srv.Status == "running", Description: "Steam Query Port (UDP)"})
			}
		}
	} else {
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
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"public_ip": publicIP,
		"probes":    probes,
	})
}

func (im *InstanceManager) DeleteServer(w http.ResponseWriter, r *http.Request, serverID string) {
	im.mu.Lock()
	isMock := im.isMock
	var srv *GameServerInstance
	if isMock {
		for i := range im.mockServers {
			if im.mockServers[i].ID == serverID {
				srv = &im.mockServers[i]
				break
			}
		}
	} else {
		srv = im.instances[serverID]
	}
	im.mu.Unlock()

	if srv == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	if isMock {
		im.mu.Lock()
		newMockServers := []GameServerInstance{}
		for _, ms := range im.mockServers {
			if ms.ID != serverID {
				newMockServers = append(newMockServers, ms)
			}
		}
		im.mockServers = newMockServers
		im.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "message": "Mock server deleted successfully"})
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
