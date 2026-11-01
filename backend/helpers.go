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
	"sync"
	"time"
)

// parseDetailsOutput parses the details command output to extract the server name and ports.
func parseDetailsOutput(output string) (string, []PortProbe) {
	serverName := ""
	ports := []PortProbe{}

	lines := strings.Split(output, "\n")
	reName := regexp.MustCompile(`(?i)^Server name:\s*(.+)$`)
	// LinuxGSM port table rows, e.g. "Game  27015  udp  1" or "Query (http)  8080  tcp  0".
	rePort := regexp.MustCompile(`(?i)^\s*([a-z0-9][a-z0-9 _\-()+:]*?)\s+(\d+)\s+(tcp|udp)\b`)
	reClean := regexp.MustCompile(`(?i)(\x1b|\\x1b|\\e|\\033)?\[[0-9;]*[a-zA-Z]`)

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 1. Try server name match
		if match := reName.FindStringSubmatch(line); len(match) > 1 {
			rawName := strings.TrimSpace(match[1])
			serverName = strings.TrimSpace(reClean.ReplaceAllString(rawName, ""))
			// LinuxGSM prints "NOT SET" when it cannot read the name from the game config
			// (info_game.sh), e.g. before a new instance created its config.
			if strings.EqualFold(serverName, "NOT SET") {
				serverName = ""
			}
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

// parseServerPort reads the game port from the LinuxGSM configs of the server in homeDir.
// configLgsm is the directory holding the per-instance config directories (lgsm/config-lgsm on a
// host).
func parseServerPort(homeDir, configLgsm string, scriptName string) int {
	// The game port as LinuxGSM sees it: port= from the merged configs.
	if p, err := strconv.Atoi(lgsmCfgValuesAt(homeDir, configLgsm, scriptName)["port"]); err == nil && p > 0 {
		return p
	}
	rootScript := gameServerNameOf(homeDir, scriptName)

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

type FileNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"is_dir"`
	Size     int64      `json:"size"`
	ModTime  string     `json:"mod_time"`
	Children []FileNode `json:"children,omitempty"`
}

type PortProbe struct {
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Open        bool   `json:"open"`
	Description string `json:"description"`
	Status      string `json:"status,omitempty"` // result of a port check, see the portStatus constants
}

// Results of a port check. It runs on the server itself, so it cannot prove that a port is
// reachable from the internet.
const (
	portStatusOpen       = "open"       // answers on the public IP
	portStatusLocal      = "local"      // answers on the machine, but not on the public IP
	portStatusClosed     = "closed"     // no answer
	portStatusUntestable = "untestable" // UDP port of a protocol the dashboard does not speak
)

// UDP has no connection to test: a port only shows up as open if the server answers a request in
// its own protocol. A silent port can still be fine.
const (
	portTestTCP = "tcp"
	portTestA2S = "a2s"
	portTestMCB = "mcbedrock"
)

// portTestFor picks the test of a port from the LinuxGSM querytype of the game, "" if there is
// none. Only the query port of a Valve protocol game answers; if the game has no query port of its
// own, that is the game port.
func portTestFor(p PortProbe, querytype string, hasQueryPort bool) string {
	if strings.EqualFold(p.Protocol, "TCP") {
		return portTestTCP
	}
	d := strings.ToLower(p.Description)
	isQuery := strings.Contains(d, "query") && !strings.Contains(d, "rcon")
	isGame := strings.Contains(d, "game")
	switch querytype {
	case "protocol-valve":
		if isQuery || (isGame && !hasQueryPort) {
			return portTestA2S
		}
	case "mbe":
		if isGame {
			return portTestMCB
		}
	}
	return ""
}

func runPortTest(test, host string, port int) bool {
	switch test {
	case portTestTCP:
		return checkTCP(host, port)
	case portTestA2S:
		return checkUDPQuery(host, port)
	case portTestMCB:
		_, err := QueryBedrock(net.JoinHostPort(host, strconv.Itoa(port)), 2*time.Second)
		return err == nil
	}
	return false
}

// probePort tests a port on the public IP ("" if unknown) and then on the local addresses.
func probePort(test, publicIP string, localHosts []string, port int) string {
	if test == "" {
		return portStatusUntestable
	}
	if publicIP != "" && runPortTest(test, publicIP, port) {
		return portStatusOpen
	}
	for _, host := range localHosts {
		if runPortTest(test, host, port) {
			return portStatusLocal
		}
	}
	return portStatusClosed
}

// uniquePorts drops repeated entries of the same port and protocol, keeping all descriptions:
// LinuxGSM lists a Source game port as both game and query port. The result is a new slice.
func uniquePorts(ports []PortProbe) []PortProbe {
	var out []PortProbe
next:
	for _, p := range ports {
		for i := range out {
			if out[i].Port == p.Port && strings.EqualFold(out[i].Protocol, p.Protocol) {
				if !strings.Contains(out[i].Description, p.Description) {
					out[i].Description += ", " + p.Description
				}
				continue next
			}
		}
		out = append(out, p)
	}
	return out
}

func (im *InstanceManager) CheckServerPorts(w http.ResponseWriter, r *http.Request, serverID string) {
	srv, ok := im.getServer(serverID)
	if !ok {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	publicIP, err := getPublicIP()
	if err != nil {
		publicIP = ""
	}

	configLgsm := filepath.Join(srv.Dir, srv.configLgsmRel())
	ports := srv.ParsedPorts
	if len(ports) == 0 {
		ports = parseServerPortsFromConfig(srv.Dir, configLgsm, srv.Script, srv.Game)
	}
	querytype := lgsmCfgValuesAt(srv.Dir, configLgsm, srv.Script)["querytype"]
	if len(ports) == 0 && srv.Port > 0 {
		proto := "UDP"
		if isMinecraftJavaGame(srv.Game) {
			proto = "TCP"
		}
		ports = []PortProbe{{Port: srv.Port, Protocol: proto, Description: "Game Port"}}
	}

	probes := uniquePorts(ports)
	hasQueryPort := false
	for _, p := range probes {
		d := strings.ToLower(p.Description)
		hasQueryPort = hasQueryPort || (strings.Contains(d, "query") && !strings.Contains(d, "rcon"))
	}

	localHosts := []string{"127.0.0.1"}
	if srv.BindIP != "" && srv.BindIP != "0.0.0.0" && net.ParseIP(srv.BindIP) != nil {
		localHosts = appendUniqueString(localHosts, srv.BindIP)
	}
	if p, ok := im.rt.(queryHostProvider); ok {
		for _, h := range p.QueryHosts() {
			if net.ParseIP(h) != nil {
				localHosts = appendUniqueString(localHosts, h)
			}
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range probes {
		wg.Add(1)
		sem <- struct{}{}
		go func(p *PortProbe) {
			defer wg.Done()
			defer func() { <-sem }()
			p.Status = probePort(portTestFor(*p, querytype, hasQueryPort), publicIP, localHosts, p.Port)
			p.Open = p.Status == portStatusOpen
		}(&probes[i])
	}
	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"public_ip": publicIP,
		"probes":    probes,
	})
}

// The public IP is looked up at most every publicIPCacheTime; port checks are frequent and the
// lookup is an outside request.
const publicIPCacheTime = 10 * time.Minute

var publicIPCache struct {
	sync.Mutex
	ip      string
	fetched time.Time
}

func getPublicIP() (string, error) {
	publicIPCache.Lock()
	defer publicIPCache.Unlock()
	if publicIPCache.ip != "" && time.Since(publicIPCache.fetched) < publicIPCacheTime {
		return publicIPCache.ip, nil
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	ipBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(ipBytes))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("invalid answer from the IP lookup")
	}
	publicIPCache.ip, publicIPCache.fetched = ip, time.Now()
	return ip, nil
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

func isMinecraftJavaGame(game string) bool {
	return game == "minecraft" || minecraftJavaGames[game]
}

func parseServerPortsFromConfig(homeDir, configLgsm string, scriptName string, game string) []PortProbe {
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

	if scriptName == "mcserver" || scriptName == "minecraft" || isMinecraftJavaGame(game) {
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

	// Port variables from the merged LinuxGSM configs
	parsedPorts := make(map[string]int)
	values := lgsmCfgValuesAt(homeDir, configLgsm, scriptName)
	for _, name := range []string{"port", "queryport", "rconport", "appport", "sourcetvport"} {
		if p, err := strconv.Atoi(values[name]); err == nil && p > 0 && p < 65536 {
			parsedPorts[name] = p
		}
	}

	var probes []PortProbe

	// 1. Game Port
	if p, ok := parsedPorts["port"]; ok {
		proto := "UDP"
		if isMinecraftJavaGame(game) {
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

	return probes
}
