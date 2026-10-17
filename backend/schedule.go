package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// -------------------------------------------------------------
// Crontab helpers
// -------------------------------------------------------------

func scriptPathOf(srv GameServerInstance) string {
	return filepath.ToSlash(filepath.Join(srv.Dir, srv.Script))
}

// cronLineCommand parses a crontab line of the form "<schedule> <scriptPath> <command> ...".
// It only matches the exact script path, so "vhserver" never matches "vhserver-2".
func cronLineCommand(line, scriptPath string) (expr string, command string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	fields := strings.Fields(trimmed)
	for i, f := range fields {
		if f == scriptPath {
			if i == 0 || i+1 >= len(fields) {
				return "", "", false
			}
			return strings.Join(fields[:i], " "), fields[i+1], true
		}
	}
	return "", "", false
}

var reCronField = regexp.MustCompile(`^[0-9*/,-]{1,64}$`)

// IsValidCronExpr validates a five-field cron schedule (or a safe @macro). Anything else could
// inject additional crontab lines or commands.
func IsValidCronExpr(expr string) bool {
	expr = strings.TrimSpace(expr)
	switch expr {
	case "@hourly", "@daily", "@midnight", "@weekly", "@monthly", "@yearly", "@annually":
		return true
	}
	if strings.ContainsAny(expr, "\n\r") {
		return false
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	for _, f := range fields {
		if !reCronField.MatchString(f) {
			return false
		}
	}
	return true
}

func isIntInRange(s string, min, max int) bool {
	if s == "" || len(s) > 3 {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= min && n <= max && strconv.Itoa(n) == s
}

var reClockTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// ValidateCronSchedule validates the scheduler form values.
func ValidateCronSchedule(s CronSchedule) error {
	if s.MonitorInterval != "" && !isIntInRange(s.MonitorInterval, 1, 59) {
		return fmt.Errorf("invalid monitor interval")
	}
	if s.UpdateInterval != "" && !isIntInRange(s.UpdateInterval, 1, 59) {
		return fmt.Errorf("invalid update interval")
	}
	if s.RestartTime != "" && !reClockTime.MatchString(s.RestartTime) {
		return fmt.Errorf("invalid restart time")
	}
	if s.CoreUpdateDay != "" && !isIntInRange(s.CoreUpdateDay, 0, 7) {
		return fmt.Errorf("invalid core update day")
	}
	return nil
}

func maintenanceComment(srv GameServerInstance) string {
	return fmt.Sprintf("# LinuxGSM %s Geplante Wartungsaufgaben", sanitizeDisplayText(srv.Name, 128))
}

var maintenanceCommands = map[string]bool{"monitor": true, "update": true, "force-update": true, "update-lgsm": true}

// rewriteCrontab removes this server's lines whose command is in drop (all commands if drop is nil)
// plus our own comment line, then appends extra lines.
func rewriteCrontab(rt Runtime, srv GameServerInstance, drop map[string]bool, extra []string) error {
	crontabStr, err := rt.ReadCrontab(srv)
	if err != nil {
		return err
	}
	scriptPath := scriptPathOf(srv)
	comment := maintenanceComment(srv)
	var newLines []string
	for _, line := range strings.Split(crontabStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if drop == nil || drop["monitor"] {
			if trimmed == comment {
				continue
			}
		}
		if _, cmd, ok := cronLineCommand(trimmed, scriptPath); ok && (drop == nil || drop[cmd]) {
			continue
		}
		newLines = append(newLines, line)
	}
	newLines = append(newLines, extra...)
	return rt.WriteCrontab(srv, strings.Join(newLines, "\n"))
}

// removeCronLinesForScript removes cron entries of one instance (used when deleting a server that
// shares its Linux user with other instances).
func removeCronLinesForScript(rt Runtime, srv GameServerInstance, drop map[string]bool) error {
	return rewriteCrontab(rt, srv, drop, nil)
}

// -------------------------------------------------------------
// Scheduler
// -------------------------------------------------------------

func (im *InstanceManager) GetCronSchedule(serverID string) (CronSchedule, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return CronSchedule{}, fmt.Errorf("server not found")
	}

	sched := CronSchedule{}
	crontabStr, _ := im.rt.ReadCrontab(srv)
	scriptPath := scriptPathOf(srv)

	reEvery := regexp.MustCompile(`^\*/(\d+) \* \* \* \*$`)
	reDaily := regexp.MustCompile(`^(\d+) (\d+) \* \* \*$`)
	reWeekly := regexp.MustCompile(`^0 0 \* \* (\d+)$`)

	for _, line := range strings.Split(crontabStr, "\n") {
		expr, cmd, ok := cronLineCommand(line, scriptPath)
		if !ok {
			continue
		}
		switch cmd {
		case "monitor":
			if m := reEvery.FindStringSubmatch(expr); m != nil {
				sched.MonitorInterval = m[1]
			}
		case "update":
			if m := reEvery.FindStringSubmatch(expr); m != nil {
				sched.UpdateInterval = m[1]
			}
		case "force-update":
			if m := reDaily.FindStringSubmatch(expr); m != nil {
				hour, _ := strconv.Atoi(m[2])
				min, _ := strconv.Atoi(m[1])
				sched.RestartTime = fmt.Sprintf("%02d:%02d", hour, min)
			}
		case "update-lgsm":
			if m := reWeekly.FindStringSubmatch(expr); m != nil {
				sched.CoreUpdateDay = m[1]
			}
		}
	}

	return sched, nil
}

func (im *InstanceManager) SaveCronSchedule(serverID string, sched CronSchedule) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if err := ValidateCronSchedule(sched); err != nil {
		return err
	}

	scriptPath := scriptPathOf(srv)
	var crons []string
	if sched.MonitorInterval != "" {
		crons = append(crons, fmt.Sprintf("*/%s * * * * %s monitor > /dev/null 2>&1", sched.MonitorInterval, scriptPath))
	}
	if sched.UpdateInterval != "" {
		crons = append(crons, fmt.Sprintf("*/%s * * * * %s update > /dev/null 2>&1", sched.UpdateInterval, scriptPath))
	}
	if sched.RestartTime != "" {
		parts := strings.Split(sched.RestartTime, ":")
		hour, _ := strconv.Atoi(parts[0])
		min, _ := strconv.Atoi(parts[1])
		crons = append(crons, fmt.Sprintf("%d %d * * * %s force-update > /dev/null 2>&1", min, hour, scriptPath))
	}
	if sched.CoreUpdateDay != "" {
		crons = append(crons, fmt.Sprintf("0 0 * * %s %s update-lgsm > /dev/null 2>&1", sched.CoreUpdateDay, scriptPath))
	}

	extra := []string{}
	if len(crons) > 0 {
		extra = append(extra, maintenanceComment(srv))
		extra = append(extra, crons...)
	}
	return rewriteCrontab(im.rt, srv, maintenanceCommands, extra)
}

func (im *InstanceManager) InstallCronjobs(serverID string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	scriptPath := scriptPathOf(srv)
	crons := []string{
		maintenanceComment(srv),
		fmt.Sprintf("*/5 * * * * %s monitor > /dev/null 2>&1", scriptPath),
		fmt.Sprintf("*/30 * * * * %s update > /dev/null 2>&1", scriptPath),
		fmt.Sprintf("30 4 * * * %s force-update > /dev/null 2>&1", scriptPath),
		fmt.Sprintf("0 0 * * 0 %s update-lgsm > /dev/null 2>&1", scriptPath),
	}
	return rewriteCrontab(im.rt, srv, maintenanceCommands, crons)
}

// -------------------------------------------------------------
// Backup settings (LinuxGSM config + automatic backup cron)
// -------------------------------------------------------------

var reBackupSetting = regexp.MustCompile(`^\s*(maxbackups|maxbackupdays|stoponbackup)\s*=\s*["']?([^"'\s#]*)["']?`)

// ValidateBackupSettings checks values written into the bash-sourced LinuxGSM config and crontab.
func ValidateBackupSettings(s BackupSettings) error {
	if s.MaxBackups != "" && !isIntInRange(s.MaxBackups, 0, 999) {
		return fmt.Errorf("invalid maxbackups value")
	}
	if s.MaxBackupDays != "" && !isIntInRange(s.MaxBackupDays, 0, 999) {
		return fmt.Errorf("invalid maxbackupdays value")
	}
	if s.StopOnBackup != "" && s.StopOnBackup != "on" && s.StopOnBackup != "off" {
		return fmt.Errorf("invalid stoponbackup value")
	}
	if s.AutoBackupEnabled && !IsValidCronExpr(s.AutoBackupCron) {
		return fmt.Errorf("invalid cron expression")
	}
	return nil
}

func (im *InstanceManager) GetBackupSettings(serverID string) (BackupSettings, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return BackupSettings{}, fmt.Errorf("server not found")
	}

	configRel := srv.configRel(srv.Script)
	settings := BackupSettings{}

	parseFile := func(rel string) {
		content, err := readServerCfg(srv, rel)
		if err != nil {
			return
		}
		for _, line := range strings.Split(content, "\n") {
			matches := reBackupSetting.FindStringSubmatch(line)
			if len(matches) > 2 {
				switch matches[1] {
				case "maxbackups":
					settings.MaxBackups = matches[2]
				case "maxbackupdays":
					settings.MaxBackupDays = matches[2]
				case "stoponbackup":
					settings.StopOnBackup = matches[2]
				}
			}
		}
	}

	parseFile(filepath.Join(configRel, "common.cfg"))
	parseFile(filepath.Join(configRel, srv.Script+".cfg"))

	// Read and parse crontab for automatic backups
	if crontabStr, err := im.rt.ReadCrontab(srv); err == nil {
		scriptPath := scriptPathOf(srv)
		for _, line := range strings.Split(crontabStr, "\n") {
			if expr, cmd, ok := cronLineCommand(line, scriptPath); ok && cmd == "backup" {
				settings.AutoBackupEnabled = true
				settings.AutoBackupCron = expr
				break
			}
		}
	}

	return settings, nil
}

// setCfgValues replaces or appends key="value" lines in a LinuxGSM config file. All values must
// already be validated with IsSafeCfgValue.
func setCfgValues(content string, values map[string]string, order []string, appendEmpty bool) string {
	lines := strings.Split(content, "\n")
	reKey := regexp.MustCompile(`^\s*([A-Za-z0-9_]+)\s*=`)
	updated := make(map[string]bool)
	for i, line := range lines {
		m := reKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if val, ok := values[m[1]]; ok {
			lines[i] = fmt.Sprintf("%s=\"%s\"", m[1], val)
			updated[m[1]] = true
		}
	}
	for _, key := range order {
		val := values[key]
		if !updated[key] && (appendEmpty || val != "") {
			lines = append(lines, fmt.Sprintf("%s=\"%s\"", key, val))
		}
	}
	return strings.Join(lines, "\n")
}

// readServerCfg reads a config file inside the server directory (empty if missing).
func readServerCfg(srv GameServerInstance, rel string) (string, error) {
	resolved, err := ResolveInRoot(srv.Dir, rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(resolved); os.IsNotExist(err) {
		return "", nil
	}
	data, err := readResolvedFileLimited(resolved, MaxEditableFileSize)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SaveBackupSettings stores the LinuxGSM backup options. The automatic backup cron entry is only
// changed when allowSchedule is true (administrators and users with the schedule permission).
func (im *InstanceManager) SaveBackupSettings(serverID string, settings BackupSettings, allowSchedule bool) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if !allowSchedule {
		// Keep the current cron state; only the config values are validated & written.
		settings.AutoBackupEnabled = false
		settings.AutoBackupCron = ""
	}
	if err := ValidateBackupSettings(settings); err != nil {
		return err
	}

	rel := filepath.Join(srv.configRel(srv.Script), srv.Script+".cfg")
	content, err := readServerCfg(srv, rel)
	if err != nil {
		return fmt.Errorf("failed to read config")
	}
	newContent := setCfgValues(content, map[string]string{
		"maxbackups":    settings.MaxBackups,
		"maxbackupdays": settings.MaxBackupDays,
		"stoponbackup":  settings.StopOnBackup,
	}, []string{"maxbackups", "maxbackupdays", "stoponbackup"}, false)
	if err := writeServerFile(srv, rel, []byte(newContent)); err != nil {
		return fmt.Errorf("failed to write config")
	}

	if !allowSchedule {
		return nil
	}

	var extra []string
	if settings.AutoBackupEnabled && settings.AutoBackupCron != "" {
		extra = append(extra, fmt.Sprintf("%s %s backup > /dev/null 2>&1", strings.Join(strings.Fields(settings.AutoBackupCron), " "), scriptPathOf(srv)))
	}
	return rewriteCrontab(im.rt, srv, map[string]bool{"backup": true}, extra)
}

// -------------------------------------------------------------
// systemd
// -------------------------------------------------------------

func (im *InstanceManager) InstallSystemdService(serverID string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	return im.rt.InstallAutostart(srv)
}
