package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeLGSMScript = "#!/bin/bash\n# LinuxGSM fake script for tests\nlgsmversion=\"v24\"\n"

func TestScanServerDirDetectsScriptsAndDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("discovery relies on the POSIX executable bit")
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "vhserver"), fakeLGSMScript)
	mustWrite(t, filepath.Join(dir, "notes.txt"), "not a script")
	mustWrite(t, filepath.Join(dir, "x';id;'"), fakeLGSMScript) // invalid name must be ignored
	_ = os.Chmod(filepath.Join(dir, "vhserver"), 0755)
	_ = os.Chmod(filepath.Join(dir, "x';id;'"), 0755)

	found := scanServerDir(dir, passwdEntry{Name: "vhserver", UID: 1001, GID: 1001, Home: dir}, false)
	if len(found) != 1 {
		t.Fatalf("expected exactly one server, got %d", len(found))
	}
	srv := found[0]
	if srv.Script != "vhserver" || srv.Dir != dir || srv.User != "vhserver" || srv.UID != 1001 {
		t.Fatalf("unexpected instance %+v", srv)
	}
}

func TestIsDiscoverableHome(t *testing.T) {
	cases := []struct {
		e    passwdEntry
		want bool
	}{
		{passwdEntry{Name: "vhserver", UID: 1001, Home: "/home/vhserver"}, true},
		{passwdEntry{Name: "arkserver", UID: 1002, Home: "/mnt/ssd/ark"}, true}, // custom home on another disk
		{passwdEntry{Name: "www-data", UID: 33, Home: "/var/www"}, false},       // system user outside /home
		{passwdEntry{Name: "root", UID: 0, Home: "/root"}, false},
		{passwdEntry{Name: "evil", UID: 1003, Home: "/etc"}, false},
		{passwdEntry{Name: "Bad Name", UID: 1004, Home: "/home/bad"}, false},
		{passwdEntry{Name: "nobody", UID: 65534, Home: "/nonexistent"}, false},
	}
	for _, c := range cases {
		if got := isDiscoverableHome(c.e); got != c.want {
			t.Errorf("isDiscoverableHome(%+v) = %v, want %v", c.e, got, c.want)
		}
	}
}

func TestValidateCustomServerPathRejectsSystemDirs(t *testing.T) {
	for _, p := range []string{"/", "/etc", "/root", "/usr/lib", "relative", "/mnt/a b", "/home"} {
		if _, err := ValidateCustomServerPath(p); err == nil {
			t.Errorf("unsafe custom path accepted: %q", p)
		}
	}
}

func TestSetCustomServerPathsPersists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("custom server paths are POSIX paths")
	}
	// Temporary directories live below /tmp which is (deliberately) not allowed, so only the
	// persistence of an empty list and the rejection path can be exercised portably here.
	cfgDir := t.TempDir()
	im := NewStaticInstanceManager(cfgDir)
	if _, err := im.SetCustomServerPaths([]string{"/tmp"}); err == nil {
		t.Fatal("/tmp must be rejected")
	}
	paths, err := im.SetCustomServerPaths(nil)
	if err != nil || len(paths) != 0 {
		t.Fatalf("unexpected result %v %v", paths, err)
	}
	info, err := os.Stat(filepath.Join(cfgDir, customPathsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("server_paths.json mode %o", info.Mode().Perm())
	}
}

func TestLgsmShellCommandQuoting(t *testing.T) {
	srv := GameServerInstance{Dir: "/mnt/ssd/game's", Script: "vhserver"}
	cmd := lgsmShellCommand(srv, "start")
	if cmd != `cd '/mnt/ssd/game'\''s' && ./'vhserver' 'start'` {
		t.Fatalf("unexpected command %q", cmd)
	}
	if strings.Contains(lgsmShellCommand(GameServerInstance{Dir: "/x", Script: "a"}, "$(id)"), "'$(id)'") == false {
		t.Fatal("argument not quoted")
	}
}
