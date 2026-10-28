package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// Deleting one instance keeps the configs the other instances of the game server still use.
func TestRemoveInstanceFilesKeepsSharedConfigs(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "lgsm", "config-lgsm", "vhserver")
	for _, f := range []string{"_default.cfg", "common.cfg", "vhserver.cfg", "secrets-vhserver.cfg", "vhserver-2.cfg"} {
		mustWrite(t, filepath.Join(cfgDir, f), "x=1\n")
	}
	mustWrite(t, filepath.Join(dir, "vhserver"), fakeLGSMScript)
	mustWrite(t, filepath.Join(dir, "vhserver-2"), fakeLGSMScript)
	mustWrite(t, filepath.Join(dir, "serverfiles", "world.db"), "w")
	mustWrite(t, filepath.Join(dir, "log", "script", "vhserver-script.log"), "")

	if err := removeInstanceFiles(GameServerInstance{Script: "vhserver", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "vhserver")) || exists(filepath.Join(cfgDir, "vhserver.cfg")) || exists(filepath.Join(cfgDir, "secrets-vhserver.cfg")) {
		t.Error("files of the deleted instance are left")
	}
	for _, keep := range []string{filepath.Join(cfgDir, "_default.cfg"), filepath.Join(cfgDir, "common.cfg"), filepath.Join(cfgDir, "vhserver-2.cfg"), filepath.Join(dir, "vhserver-2"), filepath.Join(dir, "serverfiles", "world.db")} {
		if !exists(keep) {
			t.Errorf("%s was removed although vhserver-2 still uses it", keep)
		}
	}

	// The last instance takes the shared LinuxGSM files with it, other files stay.
	mustWrite(t, filepath.Join(dir, "notes.txt"), "mine")
	if err := removeInstanceFiles(GameServerInstance{Script: "vhserver-2", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"vhserver-2", "serverfiles", "lgsm", "log"} {
		if exists(filepath.Join(dir, gone)) {
			t.Errorf("%s left after deleting the last instance", gone)
		}
	}
	if !exists(filepath.Join(dir, "notes.txt")) {
		t.Error("an unrelated file was removed")
	}
}

func TestRemoveInstanceFilesDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra privileges on Windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "keep.db"), "k")
	mustWrite(t, filepath.Join(dir, "vhserver"), fakeLGSMScript)
	if err := os.Symlink(outside, filepath.Join(dir, "serverfiles")); err != nil {
		t.Fatal(err)
	}
	if err := removeInstanceFiles(GameServerInstance{Script: "vhserver", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(outside, "keep.db")) {
		t.Error("the symlink target was deleted")
	}
}

func TestIsDedicatedServerUser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /etc/passwd style home directories")
	}
	base := t.TempDir()
	home := func(name string) string { return filepath.Join(base, name) }
	passwd := filepath.Join(base, "passwd")
	mustWrite(t, passwd, fmt.Sprintf("vh:x:1001:1001::%s:/bin/bash\nperson:x:1002:1002::%s:/bin/bash\ncustom:x:1003:1003::%s:/bin/bash\nsys:x:999:999::%s:/bin/bash\n",
		home("vh"), home("person"), home("custom"), home("sys")))
	oldPasswd := passwdPath
	passwdPath = passwd
	t.Cleanup(func() { passwdPath = oldPasswd })

	for _, h := range []string{"vh", "person", "sys"} {
		mustWrite(t, filepath.Join(home(h), "vhserver"), fakeLGSMScript)
		mustWrite(t, filepath.Join(home(h), "lgsm", "x"), "")
		mustWrite(t, filepath.Join(home(h), ".bashrc"), "")
	}
	mustWrite(t, filepath.Join(home("person"), "Documents", "cv.pdf"), "private")
	mustWrite(t, filepath.Join(base, "games", "vhserver"), fakeLGSMScript)

	cases := []struct {
		srv  GameServerInstance
		want bool
	}{
		{GameServerInstance{User: "vh", UID: 1001, Dir: home("vh")}, true},
		{GameServerInstance{User: "person", UID: 1002, Dir: home("person")}, false},               // personal files
		{GameServerInstance{User: "custom", UID: 1003, Dir: filepath.Join(base, "games")}, false}, // server outside the home
		{GameServerInstance{User: "sys", UID: 999, Dir: home("sys")}, false},                      // system account
		{GameServerInstance{User: "unknown", UID: 1004, Dir: home("vh")}, false},
	}
	for _, c := range cases {
		if got := isDedicatedServerUser(c.srv); got != c.want {
			t.Errorf("isDedicatedServerUser(%s) = %v, want %v", c.srv.User, got, c.want)
		}
	}
}
