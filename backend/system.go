package backend

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type HostStats struct {
	CPU         float64 `json:"cpu"`
	CPUCores    int     `json:"cpuCores"`
	CPUModel    string  `json:"cpuModel"`
	RAMUsed     float64 `json:"ramUsed"`
	RAMTotal    float64 `json:"ramTotal"`
	RAMPercent  float64 `json:"ramPercent"`
	DiskFree    float64 `json:"diskFree"`
	DiskTotal   float64 `json:"diskTotal"`
	DiskPercent float64 `json:"diskPercent"`
}

type ServerStats struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	User   string  `json:"user"`
	Status string  `json:"status"`
	CPU    float64 `json:"cpu"`
	RAM    float64 `json:"ram"` // GB
	PIDs   []int   `json:"pids"`
}

type SystemStats struct {
	Host    HostStats     `json:"host"`
	Servers []ServerStats `json:"servers"`
}

type SystemMetricsCollector struct {
	mu           sync.Mutex
	lastCPUTime  uint64
	lastCPUIdle  uint64
	cpuModelName string
}

func NewSystemMetricsCollector() *SystemMetricsCollector {
	collector := &SystemMetricsCollector{}
	collector.cpuModelName = getCPUModelName()
	collector.readCPUUsageRaw() // Initial read to seed delta calculation
	return collector
}

func (s *SystemMetricsCollector) GetStats(servers []GameServerInstance) (*SystemStats, error) {
	hostStats := s.getHostStats()
	serverStats := s.getServersStats(servers)

	return &SystemStats{
		Host:    hostStats,
		Servers: serverStats,
	}, nil
}

func (s *SystemMetricsCollector) getHostStats() HostStats {
	cpuPercent := s.calculateCPUPercent()
	ramUsed, ramTotal, ramPercent := s.getRAMStats()
	diskFree, diskTotal, diskPercent := s.getDiskStats()

	return HostStats{
		CPU:         cpuPercent,
		CPUCores:    runtime.NumCPU(),
		CPUModel:    s.cpuModelName,
		RAMUsed:     ramUsed,
		RAMTotal:    ramTotal,
		RAMPercent:  ramPercent,
		DiskFree:    diskFree,
		DiskTotal:   diskTotal,
		DiskPercent: diskPercent,
	}
}

func (s *SystemMetricsCollector) getServersStats(servers []GameServerInstance) []ServerStats {
	stats := make([]ServerStats, len(servers))
	for i, srv := range servers {
		cpu, ram, pids := srv.CPU, srv.RAM, srv.PIDs
		if pids == nil && cpu == 0 && ram == 0 {
			cpu, ram, pids = srv.GetProcessResourceUsage()
		}

		stats[i] = ServerStats{
			ID:     srv.ID,
			Name:   srv.Name,
			User:   srv.User,
			Status: srv.Status,
			CPU:    cpu,
			RAM:    ram,
			PIDs:   pids,
		}
	}
	return stats
}

func (s *SystemMetricsCollector) readCPUUsageRaw() (idle, total uint64) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) > 4 && fields[0] == "cpu" {
			var sum uint64
			var idleTime uint64
			for i, field := range fields[1:] {
				val, err := strconv.ParseUint(field, 10, 64)
				if err == nil {
					sum += val
					if i == 3 { // Idle field is the 4th value (index 3 of values)
						idleTime = val
					}
				}
			}
			return idleTime, sum
		}
	}
	return 0, 0
}

func (s *SystemMetricsCollector) calculateCPUPercent() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	idle, total := s.readCPUUsageRaw()
	if total == 0 {
		return 0
	}

	idleDelta := idle - s.lastCPUIdle
	totalDelta := total - s.lastCPUTime

	s.lastCPUIdle = idle
	s.lastCPUTime = total

	if totalDelta == 0 {
		return 0
	}

	percent := (1.0 - float64(idleDelta)/float64(totalDelta)) * 100.0
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return percent
}

func (s *SystemMetricsCollector) getRAMStats() (used, total, percent float64) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	defer file.Close()

	var memTotal, memAvailable float64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}

		if fields[0] == "MemTotal:" {
			memTotal = val / 1024 / 1024 // Convert KB to GB
		} else if fields[0] == "MemAvailable:" {
			memAvailable = val / 1024 / 1024
		}
	}

	if memTotal == 0 {
		return 0, 0, 0
	}

	// If MemAvailable is missing, calculate fallback
	if memAvailable == 0 {
		memAvailable = memTotal * 0.5 // Fallback to 50% available
	}

	used = memTotal - memAvailable
	percent = (used / memTotal) * 100
	return used, memTotal, percent
}

func (s *SystemMetricsCollector) getDiskStats() (free, total, percent float64) {
	// Execute 'df -Pk /' to get disk space of root directory in KB
	cmd := exec.Command("df", "-Pk", "/")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return 0, 0, 0
	}

	lines := strings.Split(out.String(), "\n")
	if len(lines) < 2 {
		return 0, 0, 0
	}

	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 0, 0, 0
	}

	totalKB, err1 := strconv.ParseFloat(fields[1], 64)
	usedKB, err2 := strconv.ParseFloat(fields[2], 64)
	freeKB, err3 := strconv.ParseFloat(fields[3], 64)

	if err1 != nil || err2 != nil || err3 != nil || totalKB == 0 {
		return 0, 0, 0
	}

	totalGB := totalKB / 1024 / 1024
	freeGB := freeKB / 1024 / 1024
	usedGB := usedKB / 1024 / 1024
	diskPercent := (usedGB / totalGB) * 100

	return freeGB, totalGB, diskPercent
}

func getCPUModelName() string {
	if runtime.GOOS == "windows" {
		return "Intel/AMD Processor (Windows)"
	}

	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "Unknown Processor"
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "Unknown Processor"
}

// GetProcessStatsForUser queries running processes for the specified user and returns CPU sum, RAM sum in GB, and PIDs
func GetProcessStatsForUser(username string) (cpu float64, ram float64, pids []int) {
	if runtime.GOOS == "windows" {
		return 0, 0, nil
	}

	// run: ps -u <username> -o pid,%cpu,rss --no-headers
	cmd := exec.Command("ps", "-u", username, "-o", "pid,%cpu,rss", "--no-headers")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		// No processes running or user doesn't exist
		return 0, 0, nil
	}

	scanner := bufio.NewScanner(&out)
	var totalCPU float64
	var totalRAMKB float64
	var pidList []int

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}

		pidVal, err1 := strconv.Atoi(fields[0])
		cpuVal, err2 := strconv.ParseFloat(fields[1], 64)
		ramVal, err3 := strconv.ParseFloat(fields[2], 64)

		if err1 == nil && err2 == nil && err3 == nil {
			pidList = append(pidList, pidVal)
			totalCPU += cpuVal
			totalRAMKB += ramVal
		}
	}

	totalRAMGB := totalRAMKB / 1024 / 1024 // Convert KB to GB
	return totalCPU, totalRAMGB, pidList
}

type FirewallStatus struct {
	Supported     bool     `json:"supported"`
	Tool          string   `json:"tool"`
	Active        bool     `json:"active"`
	HasPermission bool     `json:"hasPermission"`
	Message       string   `json:"message"`
	Rules         []string `json:"rules"`
}

func GetFirewallStatus() FirewallStatus {
	if runtime.GOOS == "windows" {
		return FirewallStatus{
			Supported:     false,
			Tool:          "none",
			Active:        false,
			HasPermission: false,
			Message:       "Firewall management is supported on Linux systems.",
			Rules:         nil,
		}
	}

	hasRoot := (os.Geteuid() == 0)

	// Check UFW
	if ufwPath, err := exec.LookPath("ufw"); err == nil && ufwPath != "" {
		cmd := exec.Command("ufw", "status")
		out, _ := cmd.Output()
		outStr := string(out)
		isActive := strings.Contains(outStr, "Status: active")
		var rules []string
		if isActive {
			for _, line := range strings.Split(outStr, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "Status:") && !strings.HasPrefix(line, "To") && !strings.HasPrefix(line, "--") {
					rules = append(rules, line)
				}
			}
		}
		msg := "UFW Firewall active."
		if !isActive {
			msg = "UFW Firewall is installed but inactive."
		}
		if !hasRoot {
			msg += " Dashboard runs without root privileges (manual sudo commands required)."
		}
		return FirewallStatus{
			Supported:     true,
			Tool:          "ufw",
			Active:        isActive,
			HasPermission: hasRoot,
			Message:       msg,
			Rules:         rules,
		}
	}

	// Check Firewalld
	if fwdPath, err := exec.LookPath("firewall-cmd"); err == nil && fwdPath != "" {
		cmd := exec.Command("firewall-cmd", "--state")
		out, _ := cmd.Output()
		isActive := strings.Contains(string(out), "running")
		var rules []string
		if isActive {
			cmdPorts := exec.Command("firewall-cmd", "--list-ports")
			pOut, _ := cmdPorts.Output()
			rules = strings.Fields(string(pOut))
		}
		msg := "Firewalld active."
		if !isActive {
			msg = "Firewalld installed but inactive."
		}
		if !hasRoot {
			msg += " Dashboard runs without root privileges (manual sudo commands required)."
		}
		return FirewallStatus{
			Supported:     true,
			Tool:          "firewalld",
			Active:        isActive,
			HasPermission: hasRoot,
			Message:       msg,
			Rules:         rules,
		}
	}

	return FirewallStatus{
		Supported:     true,
		Tool:          "none",
		Active:        false,
		HasPermission: hasRoot,
		Message:       "No active software firewall (UFW/Firewalld) detected on host.",
		Rules:         nil,
	}
}

func OpenFirewallPort(port int, protocol string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("firewall management is only supported on Linux")
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("insufficient permissions: dashboard is not running as root. Run manually: sudo ufw allow %d/%s", port, strings.ToLower(protocol))
	}

	proto := strings.ToLower(protocol)
	if proto != "tcp" && proto != "udp" {
		proto = "udp"
	}

	if ufwPath, err := exec.LookPath("ufw"); err == nil && ufwPath != "" {
		rule := fmt.Sprintf("%d/%s", port, proto)
		cmd := exec.Command("ufw", "allow", rule)
		return cmd.Run()
	}

	if fwdPath, err := exec.LookPath("firewall-cmd"); err == nil && fwdPath != "" {
		rule := fmt.Sprintf("%d/%s", port, proto)
		cmd := exec.Command("firewall-cmd", "--permanent", "--add-port="+rule)
		if err := cmd.Run(); err != nil {
			return err
		}
		return exec.Command("firewall-cmd", "--reload").Run()
	}

	return fmt.Errorf("no supported firewall tool (UFW/Firewalld) found to add rules")
}
