//go:build !linux

package backend

import "os"

// Ownership checks are only meaningful on Linux, where the dashboard manages LinuxGSM users.
func fileOwnerUID(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return -1, err
	}
	return -1, nil
}

func lfileOwnerUID(path string) (int, error) {
	if _, err := os.Lstat(path); err != nil {
		return -1, err
	}
	return -1, nil
}

func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrPathOutsideRoot
	}
	return os.OpenFile(path, flag, perm)
}

func diskFreeBytes(path string) int64 {
	return -1
}

type fileID struct{}

func hardlinkID(info os.FileInfo) (fileID, bool) { return fileID{}, false }

func allocatedSize(info os.FileInfo) int64 {
	if info.IsDir() {
		return 0
	}
	return info.Size()
}

func filesystemStats(path string) (total, free int64) { return 0, 0 }
