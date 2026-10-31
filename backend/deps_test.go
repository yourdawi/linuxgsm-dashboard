package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDepsCSVName(t *testing.T) {
	for _, c := range []struct{ id, ver, want string }{
		{"ubuntu", "24.04", "ubuntu-24.04.csv"},
		{"debian", "12", "debian-12.csv"},
		{"rocky", "9.3", "rocky-9.csv"},
		{"almalinux", "8.10", "almalinux-8.csv"},
		{"arch", "", ""},
		{"ubuntu", "24.04; rm", ""},
	} {
		if got := depsCSVName(c.id, c.ver); got != c.want {
			t.Errorf("%s %s: %q want %q", c.id, c.ver, got, c.want)
		}
	}
}

const testDepsCSV = "all,bc,tmux,curl\nsteamcmd,lib32gcc-s1,libsdl2-2.0-0:i386,steamcmd\nvh,libc6-dev,libatomic1\nts3\nmc,openjdk-25-jre,bad;name\n"

func TestParseDepsCSV(t *testing.T) {
	base, steam := parseDepsCSV(testDepsCSV, "vh")
	if strings.Join(base, " ") != "bc tmux curl libc6-dev libatomic1" || strings.Join(steam, " ") != "lib32gcc-s1 libsdl2-2.0-0:i386 steamcmd" {
		t.Fatalf("base %v steam %v", base, steam)
	}
	if base, _ := parseDepsCSV(testDepsCSV, "mc"); strings.Join(base, " ") != "bc tmux curl openjdk-25-jre" {
		t.Fatalf("invalid package name not dropped: %v", base)
	}
	if shortnameOf("vhserver") != "vh" || shortnameOf("ts3server") != "ts3" {
		t.Fatal("shortname")
	}
}

func TestInstallDependencies(t *testing.T) {
	if id, _ := osRelease("/etc/os-release"); id != "ubuntu" && id != "debian" {
		t.Skip("needs a Debian or Ubuntu test image")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testDepsCSV))
	}))
	defer srv.Close()
	oldURL, oldRun, oldInst := lgsmRawURL, depsRunner, packageInstalled
	defer func() { lgsmRawURL, depsRunner, packageInstalled = oldURL, oldRun, oldInst }()
	lgsmRawURL = srv.URL + "/"
	var calls []string
	depsRunner = func(ctx context.Context, stdin string, logf func(string), name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	packageInstalled = func(_, pkg string) bool { return pkg == "bc" || pkg == "tmux" }

	installDependencies(context.Background(), "vhserver", true, "en", func(string) {})
	got := strings.Join(calls, "|")
	for _, want := range []string{"dpkg --add-architecture i386", "apt-get update",
		"apt-get -y install -- curl libc6-dev libatomic1", "apt-get -y install -- lib32gcc-s1 libsdl2-2.0-0:i386 steamcmd"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}

	// No Steam app ID: no SteamCMD packages, no i386.
	calls = nil
	installDependencies(context.Background(), "ts3server", false, "en", func(string) {})
	if got := strings.Join(calls, "|"); strings.Contains(got, "steamcmd ") || strings.Contains(got, "i386") || !strings.Contains(got, "apt-get -y install -- curl") {
		t.Errorf("ts3 calls: %s", got)
	}

	// Nothing missing: no package manager at all.
	calls = nil
	packageInstalled = func(_, _ string) bool { return true }
	installDependencies(context.Background(), "vhserver", true, "en", func(string) {})
	if len(calls) != 0 {
		t.Errorf("calls although nothing is missing: %v", calls)
	}
}

func TestUsesSteamCMD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lgsm/config-default/config-lgsm/vhserver/_default.cfg":
			_, _ = w.Write([]byte("## SteamCMD\nappid=\"896660\"\n"))
		case "/lgsm/config-default/config-lgsm/ts3server/_default.cfg":
			_, _ = w.Write([]byte("# appid in a comment\nport=\"9987\"\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := lgsmRawURL
	defer func() { lgsmRawURL = old }()
	lgsmRawURL = srv.URL + "/"
	ctx := context.Background()
	if !usesSteamCMD(ctx, "vhserver") {
		t.Error("vh uses SteamCMD")
	}
	if usesSteamCMD(ctx, "ts3server") {
		t.Error("ts3 has no app ID")
	}
	if !usesSteamCMD(ctx, "unknownserver") {
		t.Error("unknown games keep SteamCMD")
	}
}

func TestCreatedUsersAndAdminAccounts(t *testing.T) {
	old, oldGroup := hostConfigDir, groupPath
	defer func() { hostConfigDir, groupPath = old, oldGroup }()
	hostConfigDir = t.TempDir()
	groupPath = filepath.Join(t.TempDir(), "group")
	if err := os.WriteFile(groupPath, []byte("sudo:x:27:ubuntu,alice\nusers:x:100:vh\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if wasCreatedByDashboard("vh", 1001) {
		t.Fatal("unknown user reported as created")
	}
	recordCreatedUser("vh")
	if !wasCreatedByDashboard("vh", 1001) {
		t.Fatal("created user not recorded")
	}
	forgetCreatedUser("vh")
	if wasCreatedByDashboard("vh", 1001) {
		t.Fatal("deleted user still recorded")
	}

	home := t.TempDir()
	if !isAdminAccount("ubuntu", home) || isAdminAccount("vh", home) {
		t.Fatal("sudo group membership")
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAA x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !isAdminAccount("vh", home) {
		t.Fatal("user with SSH keys is a login account")
	}
}
