package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseCronMatches(t *testing.T) {
	cases := []struct {
		expr  string
		time  string // 2026-10-18 is a Sunday
		match bool
	}{
		{"*/5 * * * *", "2026-10-18 10:15", true},
		{"*/5 * * * *", "2026-10-18 10:16", false},
		{"30 4 * * *", "2026-10-18 04:30", true},
		{"30 4 * * *", "2026-10-18 04:31", false},
		{"0 0 * * 0", "2026-10-18 00:00", true},
		{"0 0 * * 7", "2026-10-18 00:00", true}, // 7 is Sunday too
		{"0 0 * * 1", "2026-10-18 00:00", false},
		{"0 0 * * 1-5", "2026-10-19 00:00", true},
		{"15,45 8-10 * * *", "2026-10-18 09:45", true},
		{"15,45 8-10 * * *", "2026-10-18 11:45", false},
		{"0 12 1 * 1", "2026-10-19 12:00", true}, // day of month OR day of week
		{"0 12 1 * 1", "2026-11-01 12:00", true},
		{"0 12 1 * 1", "2026-10-20 12:00", false},
		{"0 */6 * * *", "2026-10-18 18:00", true},
		{"5/20 * * * *", "2026-10-18 10:45", true},
		{"0 0 1 1 *", "2027-01-01 00:00", true},
		{"@daily", "2026-10-18 00:00", true},
		{"@hourly", "2026-10-18 13:00", true},
		{"@hourly", "2026-10-18 13:01", false},
		{"@weekly", "2026-10-18 00:00", true},
	}
	for _, c := range cases {
		spec, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := spec.Matches(at(c.time)); got != c.match {
			t.Errorf("%q at %s = %v, want %v", c.expr, c.time, got, c.match)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestDockerScheduleStorage(t *testing.T) {
	dir := t.TempDir()
	d := &DockerRuntime{scheduleDir: filepath.Join(dir, "schedules")}
	srv := GameServerInstance{ID: "vh1", Script: "vhserver", Layout: LayoutData}

	if got, err := d.ReadCrontab(srv); err != nil || got != "" {
		t.Fatalf("missing schedule: %q %v", got, err)
	}
	if err := d.WriteCrontab(srv, "*/5 * * * * /app/vhserver monitor\n\n"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "schedules", "vh1.cron")
	if got, _ := d.ReadCrontab(srv); got != "*/5 * * * * /app/vhserver monitor\n" {
		t.Fatalf("unexpected content %q", got)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("schedule file must be private: %v %v", info.Mode(), err)
		}
	}
	if err := d.WriteCrontab(srv, "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("empty schedule must remove the file")
	}
	if _, err := d.ReadCrontab(GameServerInstance{ID: "../x"}); err == nil {
		t.Fatal("invalid server ids must be rejected")
	}
}

func TestDueJobs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("vh1.cron", strings.Join([]string{
		"# LinuxGSM Valheim Geplante Wartungsaufgaben",
		"*/5 * * * * /app/vhserver monitor > /dev/null 2>&1",
		"*/5 * * * * /app/vhserver monitor > /dev/null 2>&1", // duplicate
		"30 4 * * * /app/vhserver force-update > /dev/null 2>&1",
		"*/5 * * * * /app/vhserver details",            // not schedulable
		"*/5 * * * * /home/vh/vhserver update",         // host path
		"*/5 * * * * rm -rf / ; /app/vhserver backup",  // not a cron expression
		"@daily /app/vhserver backup > /dev/null 2>&1", // macro
	}, "\n"))
	write("bad name.cron", "*/5 * * * * /app/vhserver update\n")
	write("other.txt", "*/5 * * * * /app/vhserver update\n")

	got := func(tm string) string {
		var parts []string
		for _, j := range dueJobs(dir, at(tm)) {
			parts = append(parts, j.serverID+":"+j.command)
		}
		return strings.Join(parts, ",")
	}
	if g := got("2026-10-18 10:15"); g != "vh1:monitor" {
		t.Fatalf("10:15 -> %q", g)
	}
	if g := got("2026-10-18 04:30"); g != "vh1:monitor,vh1:force-update" {
		t.Fatalf("04:30 -> %q", g)
	}
	if g := got("2026-10-18 00:00"); g != "vh1:monitor,vh1:backup" {
		t.Fatalf("00:00 -> %q", g)
	}
	if g := got("2026-10-18 10:16"); g != "" {
		t.Fatalf("10:16 -> %q", g)
	}
}

func TestDockerSchedulerRunsDueCommands(t *testing.T) {
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	labels := map[string]string{dockerLabelManaged: "true", dockerLabelGameServer: "vhserver", dockerLabelDir: "/srv/lgsm/vh1"}
	list := []DockerContainer{{Names: []string{"/vh1"}, State: "running", Labels: labels}}
	e.server.Config.Handler = wrapContainerList(e, list)
	dir := t.TempDir()
	d := &DockerRuntime{client: e.client(t), dataDir: "/srv/lgsm", scheduleDir: dir}

	// The dashboard's own scheduler code writes the schedule through the runtime.
	srv := GameServerInstance{ID: "vh1", Script: "vhserver", Dir: "/srv/lgsm/vh1", Layout: LayoutData, Name: "Valheim"}
	im := NewStaticInstanceManager(t.TempDir(), srv)
	im.rt = d
	if err := im.SaveCronSchedule("vh1", CronSchedule{Jobs: map[string]string{"update": "*/15 * * * *", "restart": "30 4 * * *"}}); err != nil {
		t.Fatal(err)
	}
	if sched, err := im.GetCronSchedule("vh1"); err != nil || sched.Jobs["update"] != "*/15 * * * *" || sched.Jobs["restart"] != "30 4 * * *" || sched.Jobs["monitor"] != "" {
		t.Fatalf("schedule not read back: %+v %v", sched, err)
	}
	content, _ := d.ReadCrontab(srv)
	if !strings.Contains(content, "*/15 * * * * /app/vhserver update") || !strings.Contains(content, "30 4 * * * /app/vhserver restart") {
		t.Fatalf("unexpected schedule file:\n%s", content)
	}

	d.runDue(at("2026-10-18 04:30"))
	waitFor(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return len(e.calls) == 2 })
	var cmds []string
	for _, c := range e.calls {
		cmds = append(cmds, c.Container+" "+c.User+" "+c.WorkingDir+" "+strings.Join(c.Cmd, " "))
	}
	got := strings.Join(cmds, ",")
	if got != "vh1 linuxgsm /app ./vhserver update,vh1 linuxgsm /app ./vhserver restart" {
		t.Fatalf("unexpected scheduled commands: %s", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
