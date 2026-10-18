package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadTimeZone(t *testing.T) {
	for _, ok := range []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+2"} {
		if _, err := LoadTimeZone(ok); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus", "../../etc/passwd", "Europe/Berlin\n", "/etc/localtime", strings.Repeat("A", 70)} {
		if _, err := LoadTimeZone(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestTimeZoneSettingPersists(t *testing.T) {
	t.Setenv("TZ", "")
	dir := t.TempDir()
	s := newTimeZoneSetting(dir)
	if info := s.info(); info.TimeZone != "" || info.Effective == "" {
		t.Fatalf("unexpected default %+v", info)
	}
	if err := s.set("Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	if err := s.set("Mars/Olympus"); err == nil {
		t.Fatal("invalid zone must be rejected")
	}
	if got := s.location().String(); got != "Europe/Berlin" {
		t.Fatalf("location after a rejected change = %s", got)
	}

	reloaded := newTimeZoneSetting(dir) // e.g. after a container update
	if info := reloaded.info(); info.TimeZone != "Europe/Berlin" || info.Effective != "Europe/Berlin" {
		t.Fatalf("setting not persisted: %+v", info)
	}
	if _, err := time.Parse(time.RFC3339, reloaded.info().Now); err != nil {
		t.Fatalf("now is not RFC 3339: %v", err)
	}
	if err := reloaded.set(""); err != nil {
		t.Fatal(err)
	}
	if info := newTimeZoneSetting(dir).info(); info.TimeZone != "" {
		t.Fatalf("reset not persisted: %+v", info)
	}
	if info, err := os.Stat(filepath.Join(dir, "schedule_settings.json")); err != nil || (info.Mode().Perm()&0077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("settings file must exist and be private: %v", err)
	}
}

func TestTimeZoneDefaultFollowsTZ(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	s := newTimeZoneSetting(t.TempDir())
	if info := s.info(); info.Default != "Asia/Tokyo" || info.Effective != "Asia/Tokyo" {
		t.Fatalf("TZ not used as default: %+v", info)
	}
	if err := s.set("Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	if info := s.info(); info.Effective != "Europe/Berlin" || info.Default != "Asia/Tokyo" {
		t.Fatalf("the setting must win over TZ: %+v", info)
	}
}

func TestScheduleTimeZoneOnlyInDockerMode(t *testing.T) {
	im := NewStaticInstanceManager(t.TempDir())
	if _, ok := im.ScheduleTimeZone(); ok {
		t.Fatal("the host runtime uses the system crontab and has no time zone setting")
	}
	if err := im.SetScheduleTimeZone("UTC"); err == nil {
		t.Fatal("setting a time zone on a host must fail")
	}
	if (HostRuntime{}).Capabilities().ScheduleTimeZone || !(&DockerRuntime{}).Capabilities().ScheduleTimeZone {
		t.Fatal("capability must only be reported in Docker mode")
	}
}

func TestSchedulerUsesConfiguredTimeZone(t *testing.T) {
	e := newExecEngine(t, func(execCall) (string, string, int) { return "", "", 0 })
	labels := map[string]string{dockerLabelManaged: "true", dockerLabelGameServer: "vhserver", dockerLabelDir: "/srv/lgsm/vh1"}
	e.server.Config.Handler = wrapContainerList(e, []DockerContainer{{Names: []string{"/vh1"}, State: "running", Labels: labels}})
	cfg := t.TempDir()
	d := &DockerRuntime{client: e.client(t), dataDir: "/srv/lgsm", scheduleDir: filepath.Join(cfg, "schedules"), timeZone: newTimeZoneSetting(cfg)}
	if err := d.WriteCrontab(GameServerInstance{ID: "vh1"}, "30 4 * * * /app/vhserver force-update > /dev/null 2>&1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTimeZone("Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}

	// 04:30 in UTC is 13:30 in Tokyo: nothing is due.
	d.runDue(time.Date(2026, 10, 18, 4, 30, 0, 0, time.UTC))
	time.Sleep(100 * time.Millisecond)
	if len(e.calls) != 0 {
		t.Fatalf("job ran at 04:30 UTC: %v", e.calls)
	}
	// 19:30 UTC is 04:30 the next day in Tokyo (UTC+9).
	d.runDue(time.Date(2026, 10, 18, 19, 30, 0, 0, time.UTC))
	waitFor(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return len(e.calls) == 1 })
	if strings.Join(e.calls[0].Cmd, " ") != "./vhserver force-update" {
		t.Fatalf("unexpected command %v", e.calls[0].Cmd)
	}
	if info := d.TimeZoneInfo(); info.Effective != "Asia/Tokyo" {
		t.Fatalf("unexpected info %+v", info)
	}
}
