package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExistingInstallUser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on Linux home directories and file ownership")
	}
	uid := os.Getuid()
	base := t.TempDir()
	if uid < 1000 || !IsSafeServerDir(base) || IsForbiddenServerDir(base) {
		t.Skip("needs a regular user (UID >= 1000) and TMPDIR outside /tmp")
	}
	home := func(name string) string { return filepath.Join(base, name) }
	passwd := filepath.Join(base, "passwd")
	lines := []string{
		"daemon:x:1:1::/usr/sbin:/usr/sbin/nologin",
		fmt.Sprintf("missing:x:%d:%d::%s:/bin/bash", uid, uid, home("missing")),
		fmt.Sprintf("fresh:x:%d:%d::%s:/bin/bash", uid, uid, home("fresh")),
		fmt.Sprintf("oldlgsm:x:%d:%d::%s:/bin/bash", uid, uid, home("oldlgsm")),
		fmt.Sprintf("person:x:%d:%d::%s:/bin/bash", uid, uid, home("person")),
		fmt.Sprintf("othergame:x:%d:%d::%s:/bin/bash", uid, uid, home("othergame")),
		fmt.Sprintf("foreign:x:%d:%d::%s:/bin/bash", uid, uid, home("foreign")),
	}
	mustWrite(t, passwd, strings.Join(lines, "\n")+"\n")
	oldPasswd := passwdPath
	passwdPath = passwd
	t.Cleanup(func() { passwdPath = oldPasswd })

	mustWrite(t, filepath.Join(home("fresh"), ".bashrc"), "")
	mustWrite(t, filepath.Join(home("oldlgsm"), "cssserver"), fakeLGSMScript)
	mustWrite(t, filepath.Join(home("oldlgsm"), "lgsm", "config-lgsm", "cssserver", "_default.cfg"), "")
	mustWrite(t, filepath.Join(home("person"), "Documents", "notes.txt"), "private")
	mustWrite(t, filepath.Join(home("othergame"), "vhserver"), fakeLGSMScript)
	mustWrite(t, filepath.Join(home("othergame"), "linuxgsm.sh"), fakeLGSMScript)
	mustWrite(t, filepath.Join(home("foreign"), "cssserver"), "#!/bin/sh\necho some other tool\n")
	mustWrite(t, filepath.Join(home("foreign"), "lgsm", "readme"), "")

	reuse := map[string]InstallRequest{
		"new user":       {GameCmd: "cssserver", Username: "brandnew"},
		"home missing":   {GameCmd: "cssserver", Username: "missing"},
		"only dotfiles":  {GameCmd: "cssserver", Username: "fresh"},
		"old LinuxGSM":   {GameCmd: "cssserver", Username: "oldlgsm"},
		"same dir given": {GameCmd: "cssserver", Username: "fresh", Dir: home("fresh")},
	}
	for name, req := range reuse {
		entry, err := existingInstallUser(req)
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		} else if (entry == nil) != (req.Username == "brandnew") {
			t.Errorf("%s: entry = %+v", name, entry)
		}
	}

	refuse := map[string]InstallRequest{
		"system account": {GameCmd: "cssserver", Username: "daemon"},
		"other dir":      {GameCmd: "cssserver", Username: "fresh", Dir: home("elsewhere")},
		"personal files": {GameCmd: "cssserver", Username: "person"},
		"other game":     {GameCmd: "cssserver", Username: "othergame"},
		"foreign script": {GameCmd: "cssserver", Username: "foreign"},
	}
	for name, req := range refuse {
		if entry, err := existingInstallUser(req); err == nil {
			t.Errorf("%s: accepted (%+v)", name, entry)
		}
	}

	// ValidateInstall must not reject the existing home directory of a reused user.
	if err := (HostRuntime{}).ValidateInstall(InstallRequest{GameCmd: "cssserver", Username: "fresh", Dir: home("fresh")}); err != nil {
		t.Errorf("ValidateInstall rejected a reusable user: %v", err)
	}
}

func TestOutsideServiceSandbox(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	if name, args := outsideServiceSandbox("runuser", "-l", "vh"); name != "runuser" || len(args) != 2 {
		t.Errorf("not a service: %s %v", name, args)
	}
	if runtime.GOOS != "linux" {
		t.Skip("needs a POSIX executable named systemd-run")
	}
	bin := filepath.Join(t.TempDir(), "systemd-run")
	mustWrite(t, bin, "#!/bin/sh\n")
	if err := os.Chmod(bin, 0755); err != nil {
		t.Fatal(err)
	}
	old := systemdRunPath
	systemdRunPath = bin
	t.Cleanup(func() { systemdRunPath = old })
	t.Setenv("INVOCATION_ID", "abc")
	name, args := outsideServiceSandbox("runuser", "-l", "vh", "-c", "cd '/home/vh' && ./vhserver auto-install")
	got := name + " " + strings.Join(args, " ")
	want := bin + " --quiet --wait --pipe --collect --service-type=exec --description=LinuxGSM Dashboard: game server installation -- runuser -l vh -c cd '/home/vh' && ./vhserver auto-install"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
