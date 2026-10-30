//go:build linux

package backend

import (
	"os"
	"syscall"
)

// fileOwnerUID returns the numeric owner of path (following symlinks).
func fileOwnerUID(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return -1, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), nil
	}
	return -1, nil
}

// fileOwnerIDs returns the numeric owner and group of path (following symlinks).
func fileOwnerIDs(path string) (uid, gid int, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return -1, -1, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), nil
	}
	return -1, -1, nil
}

// lfileOwnerUID returns the numeric owner of path without following a final symlink.
func lfileOwnerUID(path string) (int, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return -1, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), nil
	}
	return -1, nil
}

// diskFreeBytes returns the bytes available to unprivileged users on the filesystem of path.
func diskFreeBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// fileID identifies a file for hard link detection.
type fileID struct {
	dev uint64
	ino uint64
}

// hardlinkID returns the identity of files with more than one hard link.
func hardlinkID(info os.FileInfo) (fileID, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.IsDir() || st.Nlink <= 1 {
		return fileID{}, false
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}

// allocatedSize returns the bytes actually allocated on disk (sparse files count less).
func allocatedSize(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return info.Size()
}

// filesystemStats returns total and available bytes of the filesystem containing path.
func filesystemStats(path string) (total, free int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
}
