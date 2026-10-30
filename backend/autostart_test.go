package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSystemd(t *testing.T) (dir string, calls *[]string) {
	t.Helper()
	dir = t.TempDir()
	oldDir, oldCtl := systemdUnitDir, systemctl
	systemdUnitDir = dir
	var log []string
	systemctl = func(args ...string) error {
		log = append(log, strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { systemdUnitDir, systemctl = oldDir, oldCtl })
	return dir, &log
}

func legacyUnit(user, dir, script string) string {
	return "[Unit]\nDescription=LinuxGSM x Server\n\n[Service]\nType=simple\nUser=" + user +
		"\nWorkingDirectory=" + dir + "\nExecStart=" + dir + "/" + script + " start\nExecStop=" + dir + "/" + script +
		" stop\nRestart=always\nRestartSec=10\n"
}

func TestAutostartUnit(t *testing.T) {
	srv := GameServerInstance{ID: "cs2server", User: "alice", Dir: "/home/alice", Script: "cs2server", Name: "My %h \\ Server"}
	unit, err := autostartUnit(srv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Type=forking", "RemainAfterExit=yes", "Restart=no", "User=alice", "WorkingDirectory=/home/alice",
		"ExecStart=/home/alice/cs2server start", "ExecStop=-/home/alice/cs2server stop", "Description=LinuxGSM My h  Server Server"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit misses %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "Restart=always") || strings.Contains(unit, "Type=simple") {
		t.Errorf("unit must not restart on its own:\n%s", unit)
	}
	if got := autostartUnitName(srv); got != "linuxgsm-alice-cs2server.service" {
		t.Errorf("unit name %q", got)
	}
	if _, err := autostartUnit(GameServerInstance{User: "alice", Dir: "/home/alice\nExecStartPre=/bin/sh", Script: "cs2server"}); err == nil {
		t.Error("unsafe directory accepted")
	}
}

func TestInstallAutostartReplacesLegacyUnit(t *testing.T) {
	dir, calls := fakeSystemd(t)
	srv := GameServerInstance{ID: "cs2server", User: "alice", Dir: "/home/alice", Script: "cs2server"}
	legacy := filepath.Join(dir, "linuxgsm-cs2server.service")
	if err := os.WriteFile(legacy, []byte(legacyUnit("alice", "/home/alice", "cs2server")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := (HostRuntime{}).InstallAutostart(srv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy unit not removed")
	}
	data, err := os.ReadFile(filepath.Join(dir, "linuxgsm-alice-cs2server.service"))
	if err != nil || !strings.Contains(string(data), "Type=forking") {
		t.Fatalf("new unit: %v %s", err, data)
	}
	got := strings.Join(*calls, "|")
	for _, want := range []string{"stop linuxgsm-cs2server.service", "disable linuxgsm-cs2server.service", "enable linuxgsm-alice-cs2server.service"} {
		if !strings.Contains(got, want) {
			t.Errorf("systemctl calls %q miss %q", got, want)
		}
	}
}

func TestAutostartLeavesOtherUsersUnits(t *testing.T) {
	dir, calls := fakeSystemd(t)
	// bob runs a server with the same script name; its old unit has the same file name.
	bobUnit := filepath.Join(dir, "linuxgsm-cs2server.service")
	if err := os.WriteFile(bobUnit, []byte(legacyUnit("bob", "/home/bob", "cs2server")), 0644); err != nil {
		t.Fatal(err)
	}
	alice := GameServerInstance{ID: "cs2server", User: "alice", Dir: "/home/alice", Script: "cs2server"}
	if err := (HostRuntime{}).InstallAutostart(alice); err != nil {
		t.Fatal(err)
	}
	removeAutostart(alice)
	if _, err := os.Stat(bobUnit); err != nil {
		t.Fatal("bob's unit was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "linuxgsm-alice-cs2server.service")); !os.IsNotExist(err) {
		t.Error("alice's unit not removed")
	}
	for _, c := range *calls {
		if strings.HasSuffix(c, " linuxgsm-cs2server.service") {
			t.Errorf("touched bob's unit: %q", c)
		}
	}
}

func TestUnitRunsServerRejectsSymlink(t *testing.T) {
	dir, _ := fakeSystemd(t)
	target := filepath.Join(t.TempDir(), "unit")
	if err := os.WriteFile(target, []byte(legacyUnit("alice", "/home/alice", "cs2server")), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linuxgsm-cs2server.service")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if unitRunsServer(link, GameServerInstance{User: "alice", Dir: "/home/alice", Script: "cs2server"}) {
		t.Error("symlinked unit accepted")
	}
}
