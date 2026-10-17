package backend

// Helpers shared by the instance manager: LinuxGSM output parsing, game name mapping,
// port detection and probing. These functions only read data and never write files.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

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

// parseDetailsIP extracts the address from the "Server IP: 1.2.3.4:27015" line of `details`.
func parseDetailsIP(output string) string {
	re := regexp.MustCompile(`(?im)^\s*Server IP:\s*([0-9.]+)(?::\d+)?\s*$`)
	if m := re.FindStringSubmatch(output); m != nil && net.ParseIP(m[1]) != nil {
		return m[1]
	}
	return ""
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

func appendUniqueString(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
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
	srv, ok := im.getServer(serverID)
	if !ok {
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
		configProbes = parseServerPortsFromConfig(srv.Dir, srv.Script, srv.Game)
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
