//go:build linux

package backend

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The dashboard works as root in directories that belong to game server users. ResolveInRoot
// checks a path with every symlink resolved, but the server user can swap a directory for a
// symlink between that check and the open. These helpers therefore walk the resolved path from
// "/" one component at a time with O_NOFOLLOW and work relative to the directory handle: a
// symlink anywhere in the path makes the operation fail instead of leaving the checked tree.

// openDirNoSymlinks opens dir, an absolute path without symlinks, as a directory handle.
func openDirNoSymlinks(dir string) (int, error) {
	if !filepath.IsAbs(dir) {
		return -1, ErrPathOutsideRoot
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: "/", Err: err}
	}
	for _, part := range strings.Split(filepath.Clean(dir), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, noSymlinkErr("open", dir, err)
		}
		fd = next
	}
	return fd, nil
}

// noSymlinkErr reports a symlink (ELOOP, or ENOTDIR for a link to a file) as a path outside the
// root and keeps other errors testable with os.IsNotExist.
func noSymlinkErr(op, path string, err error) error {
	if err == unix.ELOOP || err == unix.ENOTDIR {
		return ErrPathOutsideRoot
	}
	return &os.PathError{Op: op, Path: path, Err: err}
}

// openNoFollow opens path (absolute, symlinks resolved) without following any symlink. Reads
// are opened non-blocking, so a FIFO planted by the server user cannot hang the request; regular
// files ignore the flag.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	dirfd, err := openDirNoSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirfd)
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		flag |= unix.O_NONBLOCK
	}
	fd, err := unix.Openat(dirfd, filepath.Base(path), flag|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err != nil {
		return nil, noSymlinkErr("open", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// readDirNoSymlinks lists dir (absolute, symlinks resolved).
func readDirNoSymlinks(dir string) ([]os.DirEntry, error) {
	fd, err := openDirNoSymlinks(dir)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	return f.ReadDir(-1)
}

// removeNoSymlinks deletes the file path (absolute, symlinks resolved).
func removeNoSymlinks(path string) error {
	dirfd, err := openDirNoSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	if err := unix.Unlinkat(dirfd, filepath.Base(path), 0); err != nil {
		return noSymlinkErr("remove", path, err)
	}
	return nil
}

// mkdirAllNoSymlinks creates dir (absolute, existing part without symlinks). Directories it
// creates are handed to uid:gid when uid > 0.
func mkdirAllNoSymlinks(dir string, perm os.FileMode, uid, gid int) error {
	if !filepath.IsAbs(dir) {
		return ErrPathOutsideRoot
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	for _, part := range strings.Split(filepath.Clean(dir), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, dirFlags, 0)
		if err == unix.ENOENT {
			if err := unix.Mkdirat(fd, part, uint32(perm.Perm())); err != nil && err != unix.EEXIST {
				unix.Close(fd)
				return noSymlinkErr("mkdir", dir, err)
			}
			next, err = unix.Openat(fd, part, dirFlags, 0)
			if err == nil && uid > 0 {
				_ = unix.Fchown(next, uid, gid)
			}
		}
		unix.Close(fd)
		if err != nil {
			return noSymlinkErr("mkdir", dir, err)
		}
		fd = next
	}
	unix.Close(fd)
	return nil
}

// writeFileNoSymlinks writes target (absolute, symlinks resolved) through a temporary file in
// the same directory and renames it into place, so a symlink at target is replaced, never
// followed. keepPerm keeps the mode of an existing file instead of perm. The file is handed to
// uid:gid when uid > 0.
func writeFileNoSymlinks(target string, perm os.FileMode, keepPerm bool, uid, gid int, fill func(*os.File) error) error {
	dirfd, err := openDirNoSymlinks(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	base := filepath.Base(target)
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			return fmt.Errorf("target is a directory")
		case unix.S_IFREG:
			if keepPerm {
				perm = os.FileMode(st.Mode & 0o777)
			}
		}
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmpName := ".lgsm-dashboard-" + hex.EncodeToString(rnd[:])
	fd, err := unix.Openat(dirfd, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return noSymlinkErr("create", target, err)
	}
	f := os.NewFile(uintptr(fd), tmpName)
	err = fill(f)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil && uid > 0 {
		err = f.Chown(uid, gid)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(dirfd, tmpName, dirfd, base)
	}
	if err != nil {
		_ = unix.Unlinkat(dirfd, tmpName, 0)
		return err
	}
	return nil
}

// checkTrustedAncestors requires dir and every directory above it to belong to root (or the
// dashboard's own user) and to be writable by nobody else, so no other user can replace a
// directory on the way to a new server home (like sshd's StrictModes).
func checkTrustedAncestors(dir string) error {
	self := os.Geteuid()
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("%s is not a directory", p)
		}
		if int(st.Uid) != 0 && int(st.Uid) != self {
			return fmt.Errorf("%s belongs to another user", p)
		}
		if st.Mode&0o022 != 0 {
			return fmt.Errorf("%s is writable by other users", p)
		}
		if p == "/" {
			return nil
		}
	}
}
