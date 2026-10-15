package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type GameServerInstance struct {
	ID          string      `json:"id"`     // Script name (or script@user when the name is ambiguous)
	Name        string      `json:"name"`   // Game Name
	User        string      `json:"user"`   // Linux User
	Script      string      `json:"script"` // Executable script name (e.g. arkserver)
	Dir         string      `json:"dir"`    // LinuxGSM root directory (directory containing the script)
	Status      string      `json:"status"` // running | stopped | installing | updating
	Port        int         `json:"port"`   // Game Port
	Game        string      `json:"game"`   // Game ID
	CPU         float64     `json:"cpu"`    // Process CPU usage percentage
	RAM         float64     `json:"ram"`    // Process RAM usage in GB
	PIDs        []int       `json:"pids,omitempty"`
	ParsedPorts []PortProbe `json:"parsed_ports,omitempty"`
	Tag         string      `json:"tag,omitempty"`
	QueryData   QueryInfo   `json:"query_data"`
	Disk        *DiskUsage  `json:"disk,omitempty"`
	UID         int         `json:"-"`
	GID         int         `json:"-"`
	BindIP      string      `json:"-"` // "Server IP" reported by LinuxGSM details, used for queries
}

// QueryInfo is the live state reported by the game server itself (see query.go).
type QueryInfo struct {
	Online     bool         `json:"online"`
	Protocol   string       `json:"protocol,omitempty"`
	Name       string       `json:"name,omitempty"`
	Map        string       `json:"map"`
	NumPlayers int          `json:"num_players"`
	MaxPlayers int          `json:"max_players"`
	Bots       int          `json:"bots,omitempty"`
	Ping       int          `json:"ping,omitempty"`
	Players    []PlayerInfo `json:"players,omitempty"`
	Updated    time.Time    `json:"updated,omitempty"`
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
	configDir string
	static    bool // tests: disable discovery from /etc/passwd and the filesystem

	pathsMu     sync.RWMutex
	customPaths []string

	detailsMu    sync.Mutex
	detailsTried map[string]time.Time

	queryMu    sync.Mutex
	queryCache map[string]queryState

	diskMu       sync.Mutex
	diskCache    map[string]DiskUsage
	diskInflight map[string]bool
}

// Msg returns the English or German text depending on the language code.
func Msg(lang, en, de string) string {
	if lang == "de" {
		return de
	}
	return en
}

// passwdPath can be overridden in tests.
var passwdPath = "/etc/passwd"

// NewInstanceManager creates the manager and runs an initial discovery. configDir stores
// server tags and the list of additional (custom) server directories.
func NewInstanceManager(configDir string) *InstanceManager {
	im := &InstanceManager{
		instances:    make(map[string]*GameServerInstance),
		configDir:    configDir,
		detailsTried: make(map[string]time.Time),
	}
	im.customPaths = im.loadCustomPaths()

	cleanupLeftoverSudoers()
	im.ScanInstances()
	im.startQueryLoop()
	im.startDiskLoop()

	return im
}

// NewStaticInstanceManager returns a manager with a fixed set of instances and discovery
// disabled. It is intended for tests.
func NewStaticInstanceManager(configDir string, instances ...GameServerInstance) *InstanceManager {
	im := &InstanceManager{
		instances:    make(map[string]*GameServerInstance),
		configDir:    configDir,
		static:       true,
		detailsTried: make(map[string]time.Time),
	}
	for i := range instances {
		inst := instances[i]
		im.instances[inst.ID] = &inst
	}
	return im
}

func (im *InstanceManager) GetInstances() []GameServerInstance {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.ScanInstancesNoLock()

	list := make([]GameServerInstance, 0, len(im.instances))
	for _, inst := range im.instances {
		item := *inst
		if !im.static {
			item.CPU, item.RAM, item.PIDs = item.GetProcessResourceUsage()
		}
		if item.Status == "running" {
			item.QueryData = im.cachedQuery(item.ID)
		} else {
			item.QueryData = QueryInfo{}
		}
		item.Disk = im.cachedDisk(item.Dir)
		list = append(list, item)
	}
	return list
}

// getServer returns a snapshot copy of an instance.
func (im *InstanceManager) getServer(serverID string) (GameServerInstance, bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	srv, ok := im.instances[serverID]
	if !ok {
		return GameServerInstance{}, false
	}
	return *srv, true
}

// ServerExists reports whether serverID is a known instance. Unknown IDs trigger one rescan so
// freshly installed servers are found without waiting for the next listing.
func (im *InstanceManager) ServerExists(serverID string) bool {
	if _, ok := im.getServer(serverID); ok {
		return true
	}
	if im.static || !IsValidServerID(serverID) {
		return false
	}
	im.ScanInstances()
	_, ok := im.getServer(serverID)
	return ok
}

func (im *InstanceManager) ScanInstances() {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.ScanInstancesNoLock()
}

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

// IsSafeServerDir validates a directory that will be used as LinuxGSM root. The strict charset
// guarantees the path can be embedded in crontab lines, systemd units and shell commands.
func IsSafeServerDir(dir string) bool {
	if dir == "" || len(dir) > 255 || !strings.HasPrefix(dir, "/") {
		return false
	}
	for _, r := range dir {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+@", r)) {
			return false
		}
	}
	if strings.Contains(dir, "//") || cleanSlashPath(dir) != dir {
		return false
	}
	for _, part := range strings.Split(dir, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return !IsForbiddenServerDir(dir)
}

// cleanSlashPath cleans a slash separated path independent of the host OS.
func cleanSlashPath(p string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
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

func (im *InstanceManager) ScanInstancesNoLock() {
	if im.static {
		return
	}

	users, err := readPasswd()
	if err != nil {
		fmt.Println("Error reading /etc/passwd:", err)
		return
	}

	detected := make(map[string]*GameServerInstance)
	seenScripts := make(map[string]bool)

	add := func(inst *GameServerInstance) {
		key := filepath.Join(inst.Dir, inst.Script)
		if seenScripts[key] {
			return
		}
		seenScripts[key] = true
		id := inst.Script
		if _, taken := detected[id]; taken {
			id = inst.Script + "@" + inst.User
			for n := 2; detected[id] != nil; n++ {
				id = fmt.Sprintf("%s@%s-%d", inst.Script, inst.User, n)
			}
		}
		inst.ID = id
		detected[id] = inst
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
	for _, base := range im.GetCustomServerPaths() {
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

	// Keep track of active statuses like 'installing' or 'updating' and retain tags
	tagsMap := im.loadServerTags()
	for k, v := range detected {
		if t, ok := tagsMap[k]; ok {
			v.Tag = t
		}
		if existing, ok := im.instances[k]; ok && existing.Dir == v.Dir && existing.Script == v.Script {
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
			v.BindIP = existing.BindIP
			if existing.Tag != "" {
				v.Tag = existing.Tag
			}
		}
	}

	im.instances = detected

	// Measure the disk usage of directories that were never measured (new servers).
	dirs := make([]string, 0, len(detected))
	for _, v := range detected {
		dirs = append(dirs, v.Dir)
	}
	im.measureMissingDisks(dirs)

	// Trigger details refresh in background for newly detected or not-yet-parsed servers.
	// Attempts are throttled so failing `details` calls are not repeated on every poll.
	for k, v := range detected {
		if len(v.ParsedPorts) == 0 && im.shouldTryDetails(k) {
			go im.RefreshServerDetails(k)
		}
	}
}

func (im *InstanceManager) shouldTryDetails(serverID string) bool {
	im.detailsMu.Lock()
	defer im.detailsMu.Unlock()
	if last, ok := im.detailsTried[serverID]; ok && time.Since(last) < 10*time.Minute {
		return false
	}
	im.detailsTried[serverID] = time.Now()
	return true
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

		port := parseServerPort(dir, scriptName)

		status := "stopped"
		if isTmuxSessionActive(owner.Name, dir, scriptName) {
			status = "running"
		}

		found = append(found, &GameServerInstance{
			ID:     scriptName,
			Name:   mapScriptToGameName(scriptName),
			User:   owner.Name,
			Script: scriptName,
			Dir:    dir,
			Status: status,
			Port:   port,
			Game:   getGameFromScriptName(scriptName),
			UID:    owner.UID,
			GID:    owner.GID,
		})
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

// lgsmCommand runs the LinuxGSM script of srv as its Linux user in a login shell.
func lgsmCommand(ctx context.Context, srv GameServerInstance, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "runuser", "-l", srv.User, "-c", lgsmShellCommand(srv, args...))
}

// sanitizeDisplayText removes control characters (incl. line breaks) and limits the length of
// strings that originate from game server output (e.g. the server name).
func sanitizeDisplayText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// RefreshServerDetails queries details from LinuxGSM in the background and updates in-memory states.
func (im *InstanceManager) RefreshServerDetails(serverID string) {
	srv, ok := im.getServer(serverID)
	if !ok || len(srv.ParsedPorts) > 0 {
		return
	}
	im.executeRefreshDetails(serverID, srv)
}

// RefreshServerDetailsForce forces a details query from LinuxGSM in the background.
func (im *InstanceManager) RefreshServerDetailsForce(serverID string) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return
	}
	im.executeRefreshDetails(serverID, srv)
}

func (im *InstanceManager) executeRefreshDetails(serverID string, srv GameServerInstance) {
	if im.static {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		outputBytes, err := lgsmCommand(ctx, srv, "details").Output()
		if err != nil {
			fmt.Printf("[WARNING] failed to query details for server %s: %v\n", serverID, err)
			return
		}

		cleanOutput := stripAnsi(string(outputBytes))
		serverName, ports := parseDetailsOutput(cleanOutput)
		serverName = sanitizeDisplayText(serverName, 128)
		bindIP := parseDetailsIP(cleanOutput)

		im.mu.Lock()
		defer im.mu.Unlock()

		cur, exists := im.instances[serverID]
		if exists && cur.Dir == srv.Dir && cur.Script == srv.Script {
			if serverName != "" {
				cur.Name = serverName
			}
			if bindIP != "" {
				cur.BindIP = bindIP
			}
			if len(ports) > 0 {
				cur.ParsedPorts = ports
				// Update main game port
				for _, p := range ports {
					if strings.EqualFold(p.Description, "Game") {
						cur.Port = p.Port
						break
					}
				}
				if cur.Port == 0 && len(ports) > 0 {
					cur.Port = ports[0].Port
				}
			}
		}
	}()
}

func (srv *GameServerInstance) GetProcessResourceUsage() (cpu float64, ram float64, pids []int) {
	return GetProcessStatsForUser(srv.User)
}

// -------------------------------------------------------------
// Console & logs
// -------------------------------------------------------------

func (im *InstanceManager) GetConsole(serverID string, mode string, lang string) ([]string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return nil, fmt.Errorf("server not found")
	}

	switch mode {
	case "tmux":
		socketName, sessionName, ok := findLinuxGSMTmuxTarget(srv.User, srv.Dir, srv.Script)
		if !ok {
			return []string{Msg(lang, "Server is offline. tmux session is not active.", "Server ist offline. tmux Sitzung nicht aktiv.")}, nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "runuser", "-u", srv.User, "--", "tmux", "-L", socketName, "capture-pane", "-t", sessionName, "-p")
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return []string{Msg(lang, "Failed to capture the tmux screen.", "Fehler beim Erfassen des tmux Bildschirms.")}, nil
		}

		return strings.Split(out.String(), "\n"), nil

	case "script":
		return readServerLog(srv.Dir, filepath.Join("log", "script", srv.Script+"-script.log"), 150, lang)

	case "game":
		// Look in <rootdir>/log/game/ directory or serverfiles
		gameLogRel := filepath.Join("log", "game")
		if gameLogDir, err := ResolveInRoot(srv.Dir, gameLogRel); err == nil {
			if entries, err := os.ReadDir(gameLogDir); err == nil {
				for _, e := range entries {
					if e.Type().IsRegular() {
						return readServerLog(srv.Dir, filepath.Join(gameLogRel, e.Name()), 150, lang)
					}
				}
			}
		}
		// Fallback: look for common engine logs
		if sfRoot, err := ServerSubRoot(srv.Dir, "serverfiles", srv.UID); err == nil {
			for _, c := range []string{filepath.Join("logs", "latest.log"), "server.log"} {
				if p, err := ResolveInRoot(sfRoot, c); err == nil {
					if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
						return readServerLog(sfRoot, c, 150, lang)
					}
				}
			}
		}
		return []string{
			Msg(lang, "Game engine log file not found.", "Spiele-Engine Logdatei nicht gefunden."),
			Msg(lang, "Checked directory: ", "Geprüfter Ordner: ") + filepath.ToSlash(gameLogRel),
		}, nil

	default: // "console" or default
		return readServerLog(srv.Dir, filepath.Join("log", "console", srv.Script+"-console.log"), 150, lang)
	}
}

// readServerLog returns the last n lines of root/rel. Symlinks leaving root are refused.
func readServerLog(root, rel string, n int, lang string) ([]string, error) {
	failed := []string{
		Msg(lang, "Log file could not be opened.", "Logdatei konnte nicht geöffnet werden."),
		Msg(lang, "Path: ", "Pfad: ") + filepath.ToSlash(rel),
	}
	resolved, err := ResolveInRoot(root, rel)
	if err != nil {
		return failed, nil
	}
	file, err := OpenInRootForRead(resolved)
	if err != nil {
		return failed, nil
	}
	defer file.Close()
	lines, err := tailLines(file, n)
	if err != nil {
		return failed, nil
	}
	return lines, nil
}

// tailLines reads at most the last 256 KiB of a file and returns its last n lines.
func tailLines(file *os.File, n int) ([]string, error) {
	const maxTail = 256 * 1024
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	offset := int64(0)
	if info.Size() > maxTail {
		offset = info.Size() - maxTail
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTail))
	if err != nil {
		return nil, err
	}
	raw := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if offset > 0 && len(raw) > 0 {
		raw = raw[1:] // first line is most likely partial
	}
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	if len(raw) > n {
		raw = raw[len(raw)-n:]
	}
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = stripAnsi(l)
	}
	return lines, nil
}

// SendConsoleCommand types a command into the LinuxGSM tmux session of the server.
func (im *InstanceManager) SendConsoleCommand(serverID string, command string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if err := ValidateConsoleCommand(command); err != nil {
		return err
	}

	socketName, sessionName, ok := findLinuxGSMTmuxTarget(srv.User, srv.Dir, srv.Script)
	if !ok {
		return fmt.Errorf("server is not running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// -l sends the text literally, so tmux never interprets it as key names (e.g. "C-c").
	if err := exec.CommandContext(ctx, "runuser", "-u", srv.User, "--", "tmux", "-L", socketName, "send-keys", "-t", sessionName, "-l", "--", command).Run(); err != nil {
		return fmt.Errorf("failed to send command")
	}
	if err := exec.CommandContext(ctx, "runuser", "-u", srv.User, "--", "tmux", "-L", socketName, "send-keys", "-t", sessionName, "Enter").Run(); err != nil {
		return fmt.Errorf("failed to send command")
	}
	return nil
}

// ValidateConsoleCommand restricts console/RCON input to a single printable line.
func ValidateConsoleCommand(command string) error {
	if strings.TrimSpace(command) == "" || len(command) > 1024 {
		return fmt.Errorf("invalid command")
	}
	for _, r := range command {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid command: control characters are not allowed")
		}
	}
	return nil
}

func isTmuxSessionActive(username, dir, scriptName string) bool {
	_, _, ok := findLinuxGSMTmuxTarget(username, dir, scriptName)
	return ok
}

func findLinuxGSMTmuxTarget(username, dir, scriptName string) (socketName string, sessionName string, ok bool) {
	for _, socket := range linuxGSMTmuxSocketNames(dir, scriptName) {
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

var reTmuxUID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func linuxGSMTmuxSocketNames(dir string, scriptName string) []string {
	var sockets []string
	uidPath := filepath.Join(dir, "lgsm", "data", scriptName+".uid")
	if f, err := os.Open(uidPath); err == nil {
		uidBytes, _ := io.ReadAll(io.LimitReader(f, 256))
		f.Close()
		uid := strings.TrimSpace(string(uidBytes))
		if reTmuxUID.MatchString(uid) {
			sockets = appendUniqueString(sockets, scriptName+"-"+uid)
		}
	}

	// Legacy LinuxGSM versions used the session name as the socket name.
	return appendUniqueString(sockets, scriptName)
}

func listTmuxSessions(username string, socketName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "runuser", "-u", username, "--", "tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

// -------------------------------------------------------------
// Actions
// -------------------------------------------------------------

// sseWriter streams Server-Sent-Event frames. Streaming responses are long lived, so the
// per-request write deadline set by the HTTP layer is lifted.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	DisableWriteDeadline(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	return &sseWriter{w: w, flusher: flusher}, true
}

func (s *sseWriter) send(data interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(s.w, "data: %s\n\n", jsonData)
	s.flusher.Flush()
}

func (s *sseWriter) log(text string) {
	s.send(map[string]interface{}{"type": "log", "text": text})
}

func (s *sseWriter) exit(code int) {
	s.send(map[string]interface{}{"type": "exit", "code": code})
}

func (im *InstanceManager) RunAction(w http.ResponseWriter, r *http.Request, serverID, action, lang string) {
	if _, ok := ActionPermission(action); !ok {
		http.Error(w, "Invalid action name", http.StatusBadRequest)
		return
	}

	im.mu.Lock()
	srvPtr := im.instances[serverID]
	if srvPtr == nil {
		im.mu.Unlock()
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	srv := *srvPtr

	// Set local state status to avoid concurrent triggers
	oldStatus := srvPtr.Status
	if action == "update" || action == "update-lgsm" || action == "force-update" {
		srvPtr.Status = "updating"
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

	sse, ok := newSSEWriter(w)
	if !ok {
		updateStatus(oldStatus)
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// runuser -l <user> -c "cd '<dir>' && ./'<script>' '<action>'"
	cmd := lgsmCommand(context.Background(), srv, action)
	cmd.Stdin = strings.NewReader("y\ny\ny\n")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		updateStatus(oldStatus)
		sse.log(Msg(lang, "Error creating stdout pipe.", "Fehler beim Erstellen der stdout Pipe."))
		sse.exit(1)
		return
	}
	cmd.Stderr = cmd.Stdout // Redirect stderr to stdout to stream both

	if err := cmd.Start(); err != nil {
		updateStatus(oldStatus)
		sse.log(Msg(lang, "Error starting command: ", "Fehler beim Starten des Befehls: ") + err.Error())
		sse.exit(1)
		return
	}

	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			sse.log(stripAnsi(line))
		}
		if err != nil {
			if err != io.EOF {
				sse.log("\n" + Msg(lang, "Error reading output: ", "Fehler beim Lesen des Outputs: ") + err.Error())
			}
			break
		}
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

	sse.exit(exitCode)
}

// RunActionDirect runs an allowlisted LinuxGSM command without streaming.
func (im *InstanceManager) RunActionDirect(serverID, action string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if _, ok := ActionPermission(action); !ok {
		return fmt.Errorf("invalid action")
	}
	return lgsmCommand(context.Background(), srv, action).Run()
}

func (im *InstanceManager) RunBulkAction(action string, serverIDs []string) map[string]string {
	results := make(map[string]string)
	if _, ok := BulkActionPermission(action); !ok {
		for _, sid := range serverIDs {
			results[sid] = "Error: invalid action"
		}
		return results
	}

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

// -------------------------------------------------------------
// Server deletion
// -------------------------------------------------------------

func (im *InstanceManager) DeleteServer(w http.ResponseWriter, r *http.Request, serverID string) {
	srv, ok := im.getServer(serverID)
	if !ok {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// 1. Stop the server first
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	_ = lgsmCommand(stopCtx, srv, "stop").Run()
	cancel()

	// 2. Count instances sharing the same user
	im.mu.Lock()
	userInstancesCount := 0
	for _, inst := range im.instances {
		if inst.User == srv.User {
			userInstancesCount++
		}
	}
	im.mu.Unlock()

	// 3. Remove systemd service if it exists (current and legacy naming)
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

	// 4./5. Remove crontab entries & files
	if userInstancesCount > 1 {
		// Shared user! Only remove this instance's cron lines, script and config.
		_ = removeCronLinesForScript(srv, nil)

		if p, err := ResolveInRoot(srv.Dir, srv.Script); err == nil {
			_ = os.Remove(p)
		}
		if p, err := ResolveInRoot(srv.Dir, filepath.Join("lgsm", "config-lgsm", srv.Script)); err == nil && filepath.Base(p) == srv.Script {
			_ = os.RemoveAll(p)
		}
	} else {
		_ = exec.Command("crontab", "-u", srv.User, "-r").Run()

		// Exclusive user! Wipe everything by deleting user and home folder
		_ = exec.Command("pkill", "-u", srv.User).Run()
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("pkill", "-9", "-u", srv.User).Run()

		if err := exec.Command("userdel", "-r", srv.User).Run(); err != nil {
			fmt.Printf("[ERROR] Failed to delete user %s: %v\n", srv.User, err)
			http.Error(w, "Failed to delete user", http.StatusInternalServerError)
			return
		}
	}

	// 6. Force scan update
	im.ScanInstances()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "message": "Server deleted successfully"})
}
