package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// trySymlink creates a symlink or skips the test when the platform does not allow it.
func trySymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
}

func TestIsWithinSiblingPrefix(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "base", "fastdl")
	if !IsWithin(base, filepath.Join(base, "maps", "a.bsp")) || !IsWithin(base, base) {
		t.Fatal("expected inside")
	}
	if IsWithin(base, filepath.Join(string(filepath.Separator), "base", "fastdl_evil", "x")) {
		t.Fatal("sibling prefix must not match")
	}
	if IsWithin(base, filepath.Join(string(filepath.Separator), "base")) {
		t.Fatal("parent must not match")
	}
}

func TestResolveInRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "serverfiles")
	mustWrite(t, filepath.Join(root, "cfg", "server.cfg"), "x")
	mustWrite(t, filepath.Join(tmp, "serverfiles_evil", "secret.txt"), "secret")
	mustWrite(t, filepath.Join(tmp, "outside.txt"), "secret")

	if p, err := ResolveInRoot(root, "cfg/server.cfg"); err != nil || filepath.Base(p) != "server.cfg" {
		t.Fatalf("normal path rejected: %v", err)
	}
	if p, err := ResolveInRoot(root, ""); err != nil || p == "" {
		t.Fatalf("root itself rejected: %v", err)
	}
	if _, err := ResolveInRoot(root, "new/file.txt"); err != nil {
		t.Fatalf("non-existing path inside root rejected: %v", err)
	}

	attacks := []string{
		"../outside.txt",
		"cfg/../../outside.txt",
		"../serverfiles_evil/secret.txt",
		"/etc/passwd",
		"\\etc\\passwd",
		"..\\outside.txt",
		"cfg/\x00/x",
	}
	for _, a := range attacks {
		if _, err := ResolveInRoot(root, a); err == nil {
			t.Errorf("attack path accepted: %q", a)
		}
	}
}

func TestResolveInRootSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	outside := filepath.Join(tmp, "outside")
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret")
	mustWrite(t, filepath.Join(root, "inside.txt"), "ok")

	trySymlink(t, outside, filepath.Join(root, "link"))
	if _, err := ResolveInRoot(root, "link/secret.txt"); err == nil {
		t.Fatal("symlinked directory escape accepted")
	}
	trySymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "file-link"))
	if _, err := ResolveInRoot(root, "file-link"); err == nil {
		t.Fatal("symlinked file escape accepted")
	}
	// Dangling link: creating a file through it would write outside.
	trySymlink(t, filepath.Join(outside, "new.txt"), filepath.Join(root, "dangling"))
	if _, err := ResolveInRoot(root, "dangling"); err == nil {
		t.Fatal("dangling symlink accepted")
	}
	// Links staying inside the root are fine.
	trySymlink(t, filepath.Join(root, "inside.txt"), filepath.Join(root, "inner-link"))
	if _, err := ResolveInRoot(root, "inner-link"); err != nil {
		t.Fatalf("internal symlink rejected: %v", err)
	}
	if _, err := OpenInRootForRead(filepath.Join(root, "file-link")); err == nil {
		t.Fatal("OpenInRootForRead followed a final symlink")
	}
}

func TestResolveAbsInRoots(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "lgsm", "config-lgsm", "vhserver")
	mustWrite(t, filepath.Join(cfg, "vhserver.cfg"), "x")
	mustWrite(t, filepath.Join(tmp, "lgsm", "config-lgsm", "vhserver_evil", "x.cfg"), "x")

	if _, err := ResolveAbsInRoots(filepath.Join(cfg, "vhserver.cfg"), cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, p := range []string{
		filepath.Join(cfg, "..", "vhserver_evil", "x.cfg"),
		filepath.Join(tmp, "lgsm", "config-lgsm", "vhserver_evil", "x.cfg"),
		"relative.cfg",
	} {
		if _, err := ResolveAbsInRoots(p, cfg); err == nil {
			t.Errorf("path outside roots accepted: %q", p)
		}
	}
}

func TestServerSubRootSymlinkedToSystemDir(t *testing.T) {
	tmp := t.TempDir()
	serverDir := filepath.Join(tmp, "srv")
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(tmp, "ssd", "serverfiles")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	// serverfiles moved to another disk via symlink (owner check skipped with ownerUID -1).
	trySymlink(t, other, filepath.Join(serverDir, "serverfiles"))
	if _, err := ServerSubRoot(serverDir, "serverfiles", -1); err != nil {
		t.Fatalf("symlinked serverfiles rejected: %v", err)
	}
	if IsForbiddenServerDir("/etc") == false || IsForbiddenServerDir("/mnt/ssd/x") {
		t.Fatal("IsForbiddenServerDir broken")
	}
}
