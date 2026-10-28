package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseServerPortFollowsLinuxGSM(t *testing.T) {
	dir := t.TempDir()
	cfgLgsm := filepath.Join(dir, "lgsm", "config-lgsm")
	mustWrite(t, filepath.Join(cfgLgsm, "cssserver", "_default.cfg"), "port=\"27015\"\nclientport=\"27005\"\nsourcetvport=\"27020\"\n")
	mustWrite(t, filepath.Join(cfgLgsm, "cssserver", "cssserver-2.cfg"), "#port=\"1\"\nclientport=\"27006\"\nport=\"27025\"\n")
	mustWrite(t, filepath.Join(cfgLgsm, "cssserver", "cssserver-3.cfg"), "clientport=\"27007\"\n")

	if p := parseServerPort(dir, cfgLgsm, "cssserver"); p != 27015 {
		t.Errorf("cssserver port = %d", p)
	}
	if p := parseServerPort(dir, cfgLgsm, "cssserver-2"); p != 27025 {
		t.Errorf("cssserver-2 port = %d", p)
	}
	if p := parseServerPort(dir, cfgLgsm, "cssserver-3"); p != 27015 {
		t.Errorf("clientport taken as game port: %d", p)
	}
	probes := parseServerPortsFromConfig(dir, cfgLgsm, "cssserver-2", "css")
	if len(probes) < 2 || probes[0].Port != 27025 || probes[1].Port != 27020 {
		t.Errorf("probes = %+v", probes)
	}
}

// Instances like vhserver-2 keep their configs in config-lgsm/vhserver, as LinuxGSM loads them.
func TestInstanceSettingsUseLinuxGSMConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "lgsm", "config-lgsm", "vhserver")
	mustWrite(t, filepath.Join(cfgDir, "_default.cfg"), "stopmode=\"7\"\nmaxbackups=\"4\"\nbackupdir=\"${lgsmdir}/backup\"\n")
	mustWrite(t, filepath.Join(cfgDir, "common.cfg"), "maxbackups=\"6\"\n")
	mustWrite(t, filepath.Join(cfgDir, "vhserver-2.cfg"), "port=\"2466\"\n")
	mustWrite(t, filepath.Join(cfgDir, "secrets-vhserver-2.cfg"), "discordwebhook=\"https://old\"\n")
	mustWrite(t, filepath.Join(cfgDir, "vhserver-20.cfg"), "")
	mustWrite(t, filepath.Join(cfgDir, "secrets-vhserver-20.cfg"), "")
	srv := GameServerInstance{ID: "vhserver-2", User: "vhserver", Script: "vhserver-2", Dir: dir}
	im := NewStaticInstanceManager(t.TempDir(), srv)

	b, err := im.GetBackupSettings("vhserver-2")
	if err != nil || b.MaxBackups != "6" {
		t.Fatalf("backup settings %+v %v", b, err)
	}
	b.MaxBackups, b.StopOnBackup = "9", "on"
	if err := im.SaveBackupSettings("vhserver-2", b, false); err != nil {
		t.Fatal(err)
	}
	if mode, _ := im.GetStopModeSettings("vhserver-2"); mode != "default" {
		t.Errorf("stop mode from _default.cfg shown as override: %q", mode)
	}
	if err := im.SaveStopModeSettings("vhserver-2", "3"); err != nil {
		t.Fatal(err)
	}

	alerts, _ := im.GetAlertSettings("vhserver-2")
	alerts.DiscordEnabled, alerts.DiscordWebhook = true, "https://new"
	if err := im.SaveAlertSettings("vhserver-2", alerts); err != nil {
		t.Fatal(err)
	}

	inst, _ := os.ReadFile(filepath.Join(cfgDir, "vhserver-2.cfg"))
	for _, want := range []string{`port="2466"`, `maxbackups="9"`, `stoponbackup="on"`, `stopmode="3"`, `discordalert="on"`} {
		if !strings.Contains(string(inst), want) {
			t.Errorf("instance config misses %s:\n%s", want, inst)
		}
	}
	if secrets, _ := os.ReadFile(filepath.Join(cfgDir, "secrets-vhserver-2.cfg")); string(secrets) != "discordwebhook=\"https://new\"\n" {
		t.Errorf("secrets file not updated: %q", secrets)
	}
	if _, err := os.Stat(filepath.Join(dir, "lgsm", "config-lgsm", "vhserver-2")); !os.IsNotExist(err) {
		t.Error("config-lgsm/vhserver-2 was created, LinuxGSM never reads it")
	}

	// Clearing a value removes the override again.
	if err := im.SaveStopModeSettings("vhserver-2", "default"); err != nil {
		t.Fatal(err)
	}
	if inst, _ := os.ReadFile(filepath.Join(cfgDir, "vhserver-2.cfg")); strings.Contains(string(inst), "stopmode") {
		t.Errorf("stopmode not removed:\n%s", inst)
	}

	if got := customBackupDir(srv); got != filepath.Join(dir, "lgsm", "backup") {
		t.Errorf("customBackupDir = %q", got)
	}

	files, err := im.GetConfigFiles("vhserver-2")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name+":"+f.Layer)
	}
	want := "_default.cfg:default common.cfg:common vhserver-2.cfg:instance secrets-vhserver-2.cfg:secrets"
	if strings.Join(names, " ") != want {
		t.Errorf("config files = %v, want %s", names, want)
	}
}
