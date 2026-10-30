//go:build !linux

package backend

import (
	"fmt"
	"os"
	"path/filepath"
)

// Without openat the helpers only refuse a symlink as the last component. The dashboard manages
// servers on Linux only; these exist for development and tests on other systems.

func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrPathOutsideRoot
	}
	return os.OpenFile(path, flag, perm)
}

func readDirNoSymlinks(dir string) ([]os.DirEntry, error) { return os.ReadDir(dir) }

func removeNoSymlinks(path string) error { return os.Remove(path) }

func mkdirAllNoSymlinks(dir string, perm os.FileMode, uid, gid int) error {
	return os.MkdirAll(dir, perm)
}

func writeFileNoSymlinks(target string, perm os.FileMode, keepPerm bool, uid, gid int, fill func(*os.File) error) error {
	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			return fmt.Errorf("target is a directory")
		}
		if keepPerm && info.Mode().IsRegular() {
			perm = info.Mode().Perm()
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".lgsm-dashboard-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	err = fill(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = os.Chmod(tmpName, perm)
		err = os.Rename(tmpName, target)
	}
	if err != nil {
		os.Remove(tmpName)
	}
	return err
}

func checkTrustedAncestors(dir string) error { return nil }
