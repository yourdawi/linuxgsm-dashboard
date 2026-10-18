package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRemoveUserShm(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getuid() != 0 {
		t.Skip("needs Linux and root to create files of other users")
	}
	dir := t.TempDir()
	old := shmDir
	t.Cleanup(func() { shmDir = old })
	shmDir = dir

	const user, other = 4242, 4343
	mk := func(name string, uid int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(p, uid, uid); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ts3 := mk("7gbhujb54g8z9hu43jre8", user) // TeamSpeak 3 shared memory of the deleted user
	foreign := mk("other-server", other)     // another user's server keeps running
	rootFile := mk("system", 0)              // never touched
	sub := filepath.Join(dir, "subdir")      // directories are not removed
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.Lchown(sub, user, user)
	link := filepath.Join(dir, "link") // symlinks are not followed or removed
	if err := os.Symlink(foreign, link); err != nil {
		t.Fatal(err)
	}
	_ = os.Lchown(link, user, user)

	removeUserShm(0) // root is never cleaned up
	if _, err := os.Stat(rootFile); err != nil {
		t.Fatal("uid 0 must be ignored")
	}

	removeUserShm(user)
	if _, err := os.Lstat(ts3); !os.IsNotExist(err) {
		t.Fatal("the user's shared memory file must be removed")
	}
	for _, keep := range []string{foreign, rootFile, sub, link} {
		if _, err := os.Lstat(keep); err != nil {
			t.Fatalf("%s must be kept: %v", keep, err)
		}
	}
}
