//go:build linux

package backend

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestNoSymlinkHelpers(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	// Plain paths work.
	if err := mkdirAllNoSymlinks(filepath.Join(root, "a", "b"), 0755, -1, -1); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "a", "b", "f.txt")
	write := func(path string, data string) error {
		return writeFileNoSymlinks(path, 0640, true, -1, -1, func(f *os.File) error {
			_, err := io.WriteString(f, data)
			return err
		})
	}
	if err := write(target, "one"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	if err := write(target, "two"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0600 {
		t.Errorf("mode not kept: %v", info.Mode())
	}
	f, err := openNoFollow(target, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "two" {
		t.Errorf("read %q", data)
	}
	if entries, err := readDirNoSymlinks(filepath.Join(root, "a", "b")); err != nil || len(entries) != 1 {
		t.Errorf("readdir: %v %v", entries, err)
	}
	if err := removeNoSymlinks(target); err != nil {
		t.Fatal(err)
	}
	if _, err := openNoFollow(target, os.O_RDONLY, 0); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}

	// A symlink in any component is refused.
	viaLink := filepath.Join(root, "link", "secret")
	if _, err := openNoFollow(viaLink, os.O_RDONLY, 0); !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("open through symlink: %v", err)
	}
	if err := write(filepath.Join(root, "link", "new"), "x"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("write through symlink: %v", err)
	}
	if err := mkdirAllNoSymlinks(filepath.Join(root, "link", "d"), 0755, -1, -1); !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("mkdir through symlink: %v", err)
	}
	if err := removeNoSymlinks(viaLink); !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("remove through symlink: %v", err)
	}
	if _, err := readDirNoSymlinks(filepath.Join(root, "link")); !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("readdir of symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Error("file created outside")
	}

	// A symlink as the target itself is replaced, not followed.
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "a", "planted")); err != nil {
		t.Fatal(err)
	}
	if err := write(filepath.Join(root, "a", "planted"), "replaced"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "secret")); string(got) != "secret" {
		t.Errorf("outside file changed: %q", got)
	}
}

func TestOpenNoFollowDoesNotBlockOnFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skip("mkfifo unavailable")
	}
	done := make(chan error, 1)
	go func() {
		f, err := openNoFollow(fifo, os.O_RDONLY, 0)
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("open of a FIFO blocked")
	}
}

// TestNoSymlinkRace swaps a directory inside the root for a symlink to an outside directory in a
// loop while files are read and written through it (the check-then-open race of the review).
func TestNoSymlinkRace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "a")
	parked := filepath.Join(root, "parked")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for !stop.Load() {
			_ = os.Rename(dir, parked)
			_ = os.Symlink(outside, dir)
			_ = os.Remove(dir)
			_ = os.Rename(parked, dir)
		}
	}()
	defer func() { stop.Store(true); <-swapped }()

	deadline := time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		// The path was "checked" before the loop; every attempt races the swap.
		if f, err := openNoFollow(filepath.Join(dir, "x"), os.O_RDONLY, 0); err == nil {
			data, _ := io.ReadAll(f)
			f.Close()
			if string(data) == "outside" {
				t.Fatalf("read a file outside the root after %d attempts", i)
			}
		}
		_ = writeFileNoSymlinks(filepath.Join(dir, "y"), 0644, false, -1, -1, func(f *os.File) error {
			_, err := io.WriteString(f, "w")
			return err
		})
		if _, err := os.Lstat(filepath.Join(outside, "y")); err == nil {
			t.Fatalf("wrote a file outside the root after %d attempts", i)
		}
	}
}

func TestCheckTrustedAncestors(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create directories of other users")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	// t.TempDir lives below /tmp (1777) unless TMPDIR says otherwise.
	if err := checkTrustedAncestors(filepath.Dir(base)); err != nil {
		t.Skipf("temp dir parent not trusted: %v", err)
	}
	if err := checkTrustedAncestors(base); err != nil {
		t.Fatalf("root-owned 0755: %v", err)
	}
	foreign := filepath.Join(base, "foreign")
	if err := os.Mkdir(foreign, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(foreign, 1234, 1234); err != nil {
		t.Fatal(err)
	}
	if err := checkTrustedAncestors(filepath.Join(foreign)); err == nil {
		t.Error("directory of another user accepted")
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0777); err != nil {
		t.Fatal(err)
	}
	if err := checkTrustedAncestors(open); err == nil {
		t.Error("world-writable directory accepted")
	}
}
