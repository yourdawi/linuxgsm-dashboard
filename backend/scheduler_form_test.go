package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCronSchedule(t *testing.T) {
	if err := ValidateCronSchedule(CronSchedule{Jobs: map[string]string{"monitor": "*/5 * * * *", "update": "0 * * * *", "restart": "", "update-lgsm": "@weekly"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{
		{"monitor": "5 * * * * /bin/sh -c id #"},
		{"monitor": "*/0 * * * *"},
		{"update": "60 * * * *"},
		{"restart": "0 25 * * *"},
		{"restart": "30 4 * * *\n* * * * * id"},
		{"update-lgsm": "0 0 32 * *"},
		{"backup": "0 4 * * *"},
		{"monitor": "@reboot"},
	} {
		if ValidateCronSchedule(CronSchedule{Jobs: bad}) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if ValidateBackupSettings(BackupSettings{AutoBackupEnabled: true, AutoBackupCron: "0 25 * * *"}) == nil {
		t.Error("backup schedule with hour 25 accepted")
	}
}

// The form only changes what it sends; other jobs and their exact lines stay.
func TestSaveCronScheduleKeepsUntouchedJobs(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "lgsm", "config-lgsm", "vhserver", "_default.cfg"), "appid=\"896660\"\n")
	srv := GameServerInstance{ID: "vhserver", Script: "vhserver", User: "vh", Dir: dir}
	rt := &fakeRuntime{crontab: strings.Join([]string{
		"MAILTO=\"\"",
		"*/2 * * * * " + dir + "/vhserver monitor >> /tmp/monitor.log 2>&1",
		"0 0 * * 3 " + dir + "/vhserver update-lgsm > /dev/null 2>&1",
		"0 5 * * * " + dir + "/vhserver backup > /dev/null 2>&1",
	}, "\n")}
	im := NewStaticInstanceManager(t.TempDir(), srv)
	im.rt = rt

	sched, err := im.GetCronSchedule("vhserver")
	if err != nil || sched.Jobs["monitor"] != "*/2 * * * *" || sched.Jobs["update-lgsm"] != "0 0 * * 3" || sched.Jobs["force-update"] != "" {
		t.Fatalf("read %+v %v", sched, err)
	}
	if strings.Join(sched.Available, ",") != "monitor,update,force-update,restart,update-lgsm" {
		t.Fatalf("available %v", sched.Available)
	}
	if err := im.SaveCronSchedule("vhserver", CronSchedule{Jobs: map[string]string{"update": "0 * * * *"}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"*/2 * * * * " + dir + "/vhserver monitor >> /tmp/monitor.log 2>&1", "0 0 * * 3 " + dir + "/vhserver update-lgsm",
		"0 * * * * " + dir + "/vhserver update", "0 5 * * * " + dir + "/vhserver backup", "MAILTO"} {
		if !strings.Contains(rt.crontab, want) {
			t.Errorf("crontab misses %q:\n%s", want, rt.crontab)
		}
	}
	if strings.Contains(rt.crontab, "force-update") || strings.Contains(rt.crontab, "restart") {
		t.Errorf("job added that nobody set:\n%s", rt.crontab)
	}
	if err := im.SaveCronSchedule("vhserver", CronSchedule{Jobs: map[string]string{"monitor": ""}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rt.crontab, "monitor") {
		t.Errorf("monitor not removed:\n%s", rt.crontab)
	}
}

func TestAvailableScheduleCommands(t *testing.T) {
	dir := t.TempDir()
	for script, cfg := range map[string]string{"ts3server": "", "q3server": "engine=\"idtech3\"\n", "mtaserver": ""} {
		mustWrite(t, filepath.Join(dir, "lgsm", "config-lgsm", script, "_default.cfg"), cfg)
	}
	for script, want := range map[string]string{
		"ts3server": "monitor,update,restart,update-lgsm",
		"q3server":  "monitor,restart,update-lgsm",
		"mtaserver": "monitor,update,force-update,restart,update-lgsm",
	} {
		srv := GameServerInstance{Script: script, Dir: dir}
		if got := strings.Join(availableScheduleCommands(srv), ","); got != want {
			t.Errorf("%s: %s want %s", script, got, want)
		}
	}
	srv := GameServerInstance{ID: "ts3server", Script: "ts3server", Dir: dir}
	im := NewStaticInstanceManager(t.TempDir(), srv)
	im.rt = &fakeRuntime{}
	if err := im.SaveCronSchedule("ts3server", CronSchedule{Jobs: map[string]string{"force-update": "0 4 * * *"}}); err == nil {
		t.Error("force-update scheduled for a game without it")
	}
	_ = os.RemoveAll(dir)
}
