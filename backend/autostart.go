package backend

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Autostart units follow the LinuxGSM documentation (running on boot): "<script> start" launches
// the server in tmux and exits, so the unit is Type=forking and stays active afterwards
// (RemainAfterExit), which makes systemd run "<script> stop" at shutdown. The stop result is
// ignored ('-'): for a server already stopped from the dashboard it exits 2, which would mark the
// unit failed. Restarting a crashed server is LinuxGSM's monitor, not systemd's job.
//
// Units are named linuxgsm-<user>-<script>.service; older dashboards used
// linuxgsm-<script>.service with Type=simple and Restart=always, which stopped the server right
// after every start. Those units are rewritten at startup (MigrateAutostart).

// systemdUnitDir can be overridden in tests.
var systemdUnitDir = "/etc/systemd/system"

// systemctl runs systemctl; tests replace it.
var systemctl = func(args ...string) error { return exec.Command("systemctl", args...).Run() }

func autostartUnitName(srv GameServerInstance) string {
	return "linuxgsm-" + srv.User + "-" + srv.Script + ".service"
}

// legacyAutostartUnitNames are the names earlier versions used for a server's unit.
func legacyAutostartUnitNames(srv GameServerInstance) []string {
	names := []string{"linuxgsm-" + srv.Script + ".service"}
	if srv.ID != srv.Script && IsValidScriptName(srv.ID) {
		names = append(names, srv.ID+".service")
	}
	return names
}

// autostartUnit renders the unit file of a server.
func autostartUnit(srv GameServerInstance) (string, error) {
	if !IsSafeServerDir(srv.Dir) || !IsValidScriptName(srv.Script) || !IsValidLinuxUsername(srv.User) {
		return "", fmt.Errorf("server cannot be installed as systemd service")
	}
	// The description comes from game server output: strip anything systemd could interpret.
	desc := strings.Map(func(r rune) rune {
		if r == '%' || r == '\\' {
			return -1
		}
		return r
	}, sanitizeDisplayText(srv.Name, 100))
	scriptPath := scriptPathOf(srv)
	return fmt.Sprintf(`[Unit]
Description=LinuxGSM %s Server
After=network-online.target
Wants=network-online.target

[Service]
Type=forking
User=%s
WorkingDirectory=%s
RemainAfterExit=yes
ExecStart=%s start
ExecStop=-%s stop
Restart=no
TimeoutStartSec=15min
TimeoutStopSec=5min

[Install]
WantedBy=multi-user.target
`, desc, srv.User, srv.Dir, scriptPath, scriptPath), nil
}

// unitRunsServer reports whether a unit file was written for this server: same user, directory
// and script. Units of other users that happen to run a script with the same name are left alone.
func unitRunsServer(path string, srv GameServerInstance) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var user, dir, start string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "User="); ok {
			user = v
		} else if v, ok := strings.CutPrefix(line, "WorkingDirectory="); ok {
			dir = v
		} else if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			start = v
		}
	}
	return user == srv.User && filepath.Clean(dir) == filepath.Clean(srv.Dir) && start == scriptPathOf(srv)+" start"
}

// InstallAutostart writes and enables the server's unit and replaces a unit of an earlier
// version.
func (HostRuntime) InstallAutostart(srv GameServerInstance) error {
	unit, err := autostartUnit(srv)
	if err != nil {
		return err
	}
	removeLegacyAutostart(srv)
	name := autostartUnitName(srv)
	if err := WriteFileAtomic(filepath.Join(systemdUnitDir, name), []byte(unit), 0644); err != nil {
		return err
	}
	_ = systemctl("daemon-reload")
	if err := systemctl("enable", name); err != nil {
		return fmt.Errorf("systemctl enable %s failed", name)
	}
	return nil
}

// removeLegacyAutostart disables and deletes old-style units of this server. It reports whether
// one was found. The unit is stopped first: its restart loop would otherwise keep running
// (stopping an inactive unit only cancels a pending restart).
func removeLegacyAutostart(srv GameServerInstance) bool {
	found := false
	for _, name := range legacyAutostartUnitNames(srv) {
		path := filepath.Join(systemdUnitDir, name)
		if !unitRunsServer(path, srv) {
			continue
		}
		found = true
		_ = systemctl("stop", name)
		_ = systemctl("disable", name)
		_ = os.Remove(path)
	}
	if found {
		_ = systemctl("daemon-reload")
	}
	return found
}

// removeAutostart disables and deletes every unit of this server (current and legacy names).
func removeAutostart(srv GameServerInstance) {
	removeLegacyAutostart(srv)
	name := autostartUnitName(srv)
	path := filepath.Join(systemdUnitDir, name)
	if !unitRunsServer(path, srv) {
		return
	}
	_ = systemctl("stop", name)
	_ = systemctl("disable", name)
	_ = os.Remove(path)
	_ = systemctl("daemon-reload")
}

// MigrateAutostart replaces an old-style unit of a server with the current one. A server that
// was running is started again through the new unit, because stopping the old unit stops it.
func (h HostRuntime) MigrateAutostart(srv GameServerInstance) {
	if os.Geteuid() != 0 || srv.Layout == LayoutData {
		return
	}
	legacy := false
	for _, name := range legacyAutostartUnitNames(srv) {
		if unitRunsServer(filepath.Join(systemdUnitDir, name), srv) {
			legacy = true
		}
	}
	if !legacy {
		return
	}
	wasRunning := isTmuxSessionActive(h, srv)
	if err := h.InstallAutostart(srv); err != nil {
		fmt.Printf("[WARNING] could not migrate the autostart unit of %s: %v\n", srv.ID, err)
		return
	}
	fmt.Printf("[INFO] migrated the autostart unit of %s to %s\n", srv.ID, autostartUnitName(srv))
	if wasRunning && !isTmuxSessionActive(h, srv) {
		_ = systemctl("start", autostartUnitName(srv))
	}
}
