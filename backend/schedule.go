package backend

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// -------------------------------------------------------------
// Crontab helpers
// -------------------------------------------------------------

// scriptPathOf is the path of the LinuxGSM script as it appears in schedules.
func scriptPathOf(srv GameServerInstance) string {
	if srv.Layout == LayoutData {
		return dockerScriptDir + "/" + srv.Script
	}
	return filepath.ToSlash(filepath.Join(srv.Dir, srv.Script))
}

// cronLineCommand parses a crontab line of the form "<schedule> <scriptPath> <command> ..." or
// "<schedule> cd <dir> && ./<script> <command> ...". It only matches the exact script, so
// "vhserver" never matches "vhserver-2".
func cronLineCommand(line, scriptPath string) (expr string, command string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	fields := strings.Fields(trimmed)
	n := 5 // schedule fields
	if strings.HasPrefix(fields[0], "@") {
		n = 1
	}
	dir, script := path.Split(scriptPath)
	dir = strings.TrimSuffix(dir, "/")
	inDir := false
	for i := n; i < len(fields); i++ {
		f := fields[i]
		if f == "cd" && i+1 < len(fields) && strings.TrimRight(fields[i+1], ";") == dir {
			inDir = true
		}
		if f == scriptPath || (inDir && f == "./"+script) {
			if i+1 >= len(fields) {
				return "", "", false
			}
			return strings.Join(fields[:n], " "), fields[i+1], true
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

// ValidateCronExpr accepts what IsValidCronExpr allows and what the scheduler can also run: the
// regex alone lets through */0, 60 or 0 25 * * *, which crontab rejects and the Docker scheduler
// never runs.
func ValidateCronExpr(expr string) error {
	if !IsValidCronExpr(expr) {
		return fmt.Errorf("invalid cron expression %q", expr)
	}
	if _, err := ParseCron(expr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %v", expr, err)
	}
	return nil
}

// scheduleCommands are the LinuxGSM commands the scheduler form manages, in crontab order.
var scheduleCommands = []string{"monitor", "update", "force-update", "restart", "update-lgsm"}

// availableScheduleCommands lists the scheduler commands the game of srv has (core_getopt.sh):
// update not for the "noupdate" engines, force-update only for SteamCMD games, mta and hyt.
func availableScheduleCommands(srv GameServerInstance) []string {
	values := lgsmCfgValues(srv, true)
	short := strings.TrimSuffix(gameServerName(srv.Script), "server")
	if srv.Layout != LayoutData {
		short = strings.TrimSuffix(gameServerNameOf(srv.Dir, srv.Script), "server")
	}
	engine := values["engine"]
	hasUpdate := (short == "jk2" || short == "jk3" || short == "etl" || engine != "idtech3") &&
		short != "bf1942" && short != "bfv" && engine != "idtech2" && engine != "iw2.0" &&
		engine != "iw3.0" && engine != "quake" && short != "samp" && short != "ut2k4"
	out := []string{"monitor"}
	if hasUpdate {
		out = append(out, "update")
		if values["appid"] != "" || short == "mta" || short == "hyt" {
			out = append(out, "force-update")
		}
	}
	return append(out, "restart", "update-lgsm")
}

// maintenanceComment marks the maintenance lines of a server. It names the script, which never
// changes, unlike the server name that `details` reads from the game config.
func maintenanceComment(srv GameServerInstance) string {
	return fmt.Sprintf("# LinuxGSM %s: scheduled maintenance", srv.Script)
}

// isLegacyMaintenanceComment matches the comment of older versions ("# LinuxGSM <server name>
// Geplante Wartungsaufgaben"), which cannot be told apart by server.
func isLegacyMaintenanceComment(line string) bool {
	return strings.HasPrefix(line, "# LinuxGSM ") && strings.HasSuffix(line, " Geplante Wartungsaufgaben")
}

var maintenanceCommands = map[string]bool{"monitor": true, "update": true, "force-update": true, "restart": true, "update-lgsm": true}

// rewriteCrontab removes this server's lines whose command is in drop (all commands if drop is nil)
// plus our own comment line, then appends extra lines.
func rewriteCrontab(rt Runtime, srv GameServerInstance, drop map[string]bool, extra []string) error {
	crontabStr, err := rt.ReadCrontab(srv)
	if err != nil {
		return err
	}
	scriptPath := scriptPathOf(srv)
	comment := maintenanceComment(srv)
	dropComments := drop == nil || drop["monitor"]
	var newLines []string
	pendingLegacy := "" // a legacy comment is dropped together with the line after it
	for _, line := range strings.Split(crontabStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if dropComments && trimmed == comment {
			continue
		}
		if dropComments && isLegacyMaintenanceComment(trimmed) {
			if pendingLegacy != "" {
				newLines = append(newLines, pendingLegacy)
			}
			pendingLegacy = line
			continue
		}
		_, cmd, ok := cronLineCommand(trimmed, scriptPath)
		dropLine := ok && (drop == nil || drop[cmd])
		if pendingLegacy != "" {
			if !dropLine {
				newLines = append(newLines, pendingLegacy)
			}
			pendingLegacy = ""
		}
		if dropLine {
			continue
		}
		newLines = append(newLines, line)
	}
	if pendingLegacy != "" {
		newLines = append(newLines, pendingLegacy)
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

// cronJobLines returns this server's crontab lines per scheduler command, as they are.
func cronJobLines(crontab, scriptPath string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(crontab, "\n") {
		if _, cmd, ok := cronLineCommand(line, scriptPath); ok && maintenanceCommands[cmd] {
			out[cmd] = append(out[cmd], strings.TrimSpace(line))
		}
	}
	return out
}

func (im *InstanceManager) GetCronSchedule(serverID string) (CronSchedule, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return CronSchedule{}, fmt.Errorf("server not found")
	}
	crontab, _ := im.rt.ReadCrontab(srv)
	sched := CronSchedule{Jobs: map[string]string{}, Available: availableScheduleCommands(srv)}
	scriptPath := scriptPathOf(srv)
	for cmd, lines := range cronJobLines(crontab, scriptPath) {
		if expr, _, ok := cronLineCommand(lines[0], scriptPath); ok {
			sched.Jobs[cmd] = expr
		}
	}
	return sched, nil
}

// ValidateCronSchedule checks the jobs sent by the scheduler form.
func ValidateCronSchedule(s CronSchedule) error {
	for cmd, expr := range s.Jobs {
		if !maintenanceCommands[cmd] {
			return fmt.Errorf("unknown scheduler command %q", cmd)
		}
		if expr != "" {
			if err := ValidateCronExpr(expr); err != nil {
				return err
			}
		}
	}
	return nil
}

// SaveCronSchedule changes the jobs of the commands in sched.Jobs ("" removes a job). Jobs of
// other commands stay exactly as they are, so the form never removes or adds anything the user
// did not touch.
func (im *InstanceManager) SaveCronSchedule(serverID string, sched CronSchedule) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if err := ValidateCronSchedule(sched); err != nil {
		return err
	}
	available := map[string]bool{}
	for _, c := range availableScheduleCommands(srv) {
		available[c] = true
	}
	crontab, err := im.rt.ReadCrontab(srv)
	if err != nil {
		return err
	}
	scriptPath := scriptPathOf(srv)
	current := cronJobLines(crontab, scriptPath)
	var lines []string
	for _, cmd := range scheduleCommands {
		expr, changed := sched.Jobs[cmd]
		if !changed {
			lines = append(lines, current[cmd]...)
			continue
		}
		if expr == "" {
			continue
		}
		if !available[cmd] {
			return fmt.Errorf("%s does not exist for this game", cmd)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s > /dev/null 2>&1", strings.TrimSpace(expr), scriptPath, cmd))
	}
	if len(lines) > 0 {
		lines = append([]string{maintenanceComment(srv)}, lines...)
	}
	return rewriteCrontab(im.rt, srv, maintenanceCommands, lines)
}

// InstallCronjobs sets up the jobs LinuxGSM recommends: monitor every 5 minutes, update every
// 30 minutes (where the game has it) and update-lgsm weekly.
func (im *InstanceManager) InstallCronjobs(serverID string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	jobs := map[string]string{"monitor": "*/5 * * * *", "update-lgsm": "0 0 * * 0"}
	for _, c := range availableScheduleCommands(srv) {
		if c == "update" {
			jobs["update"] = "*/30 * * * *"
		}
	}
	return im.SaveCronSchedule(serverID, CronSchedule{Jobs: jobs})
}

// -------------------------------------------------------------
// Backup settings (LinuxGSM config + automatic backup cron)
// -------------------------------------------------------------

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
	if s.AutoBackupEnabled {
		if err := ValidateCronExpr(s.AutoBackupCron); err != nil {
			return err
		}
	}
	return nil
}

func (im *InstanceManager) GetBackupSettings(serverID string) (BackupSettings, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return BackupSettings{}, fmt.Errorf("server not found")
	}

	values := lgsmCfgValues(srv, false)
	settings := BackupSettings{
		MaxBackups:    values["maxbackups"],
		MaxBackupDays: values["maxbackupdays"],
		StopOnBackup:  values["stoponbackup"],
	}

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

	if err := saveInstanceCfgValues(srv, map[string]string{
		"maxbackups":    settings.MaxBackups,
		"maxbackupdays": settings.MaxBackupDays,
		"stoponbackup":  settings.StopOnBackup,
	}, []string{"maxbackups", "maxbackupdays", "stoponbackup"}, false); err != nil {
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
