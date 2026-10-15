package backend

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathOutsideRoot is returned when a user supplied path would escape its intended root.
var ErrPathOutsideRoot = errors.New("access denied: path outside allowed directory")

// IsWithin reports whether path equals root or lies below it. Both must be clean absolute paths.
// It uses filepath.Rel instead of a string prefix check so "/base_evil" never matches "/base".
func IsWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// cleanRelative validates a user supplied relative path. It rejects absolute paths,
// NUL bytes and any path that escapes upwards after cleaning.
func cleanRelative(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", ErrPathOutsideRoot
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", ErrPathOutsideRoot
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", ErrPathOutsideRoot
	}
	if cleaned == "." {
		return "", nil
	}
	return cleaned, nil
}

// resolveExisting resolves all symlinks of the deepest existing ancestor of p and appends the
// non-existing remainder. A dangling symlink anywhere in the existing part is rejected, because
// creating a file through it would write to the (attacker chosen) link target.
func resolveExisting(p string) (string, error) {
	suffix := ""
	cur := p
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if suffix == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, suffix), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if _, lerr := os.Lstat(cur); lerr == nil {
			// Entry exists but cannot be resolved: dangling or looping symlink.
			return "", ErrPathOutsideRoot
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		if suffix == "" {
			suffix = filepath.Base(cur)
		} else {
			suffix = filepath.Join(filepath.Base(cur), suffix)
		}
		cur = parent
	}
}

// ResolveInRoot safely resolves the user supplied relative path rel against root.
// Symlinks are resolved and the final target must stay inside the (resolved) root.
// The returned path is absolute and may not exist yet (e.g. for uploads).
func ResolveInRoot(root, rel string) (string, error) {
	cleaned, err := cleanRelative(rel)
	if err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rootResolved, err = filepath.Abs(rootResolved)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(rootResolved, cleaned)
	resolved, err := resolveExisting(candidate)
	if err != nil {
		return "", err
	}
	if !IsWithin(rootResolved, resolved) {
		return "", ErrPathOutsideRoot
	}
	return resolved, nil
}

// ResolveAbsInRoots checks that an absolute path (e.g. from the config file list) resolves to a
// location inside one of the allowed roots. Roots that do not exist are ignored.
func ResolveAbsInRoots(path string, roots ...string) (string, error) {
	if strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
		return "", ErrPathOutsideRoot
	}
	resolved, err := resolveExisting(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		rootResolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if IsWithin(rootResolved, resolved) {
			return resolved, nil
		}
	}
	return "", ErrPathOutsideRoot
}

// systemDirs may never be used as (or contain) a gameserver directory.
var systemDirs = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc",
	"/root", "/run", "/sbin", "/sys", "/usr", "/tmp", "/var/tmp", "/var/lib", "/var/log",
	"/var/spool", "/var/cache", "/var/run", "/var/mail", "/snap",
}

// containerDirs are fine as parents but must not be used directly as a server directory.
var containerDirs = []string{"/", "/home", "/var", "/opt", "/mnt", "/srv", "/media", "/data"}

// IsForbiddenServerDir reports whether dir is a system location that must never be treated as a
// LinuxGSM server root (where the dashboard would read/write files as root).
func IsForbiddenServerDir(dir string) bool {
	clean := filepath.ToSlash(filepath.Clean(dir))
	for _, c := range containerDirs {
		if clean == c {
			return true
		}
	}
	for _, s := range systemDirs {
		if clean == s || strings.HasPrefix(clean, s+"/") {
			return true
		}
	}
	return false
}

// ServerSubRoot resolves a well-known sub directory of a server (e.g. "serverfiles") so that it can
// be used as root for file operations. LinuxGSM setups often symlink serverfiles to another disk; this
// is allowed as long as the target is owned by the server's Linux user and is not a system directory.
func ServerSubRoot(serverDir, sub string, ownerUID int) (string, error) {
	base := serverDir
	if sub != "" {
		base = filepath.Join(serverDir, sub)
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	if dirResolved, err := filepath.EvalSymlinks(serverDir); err == nil && IsWithin(dirResolved, resolved) {
		return resolved, nil
	}
	if IsForbiddenServerDir(resolved) {
		return "", ErrPathOutsideRoot
	}
	if ownerUID >= 0 {
		uid, err := fileOwnerUID(resolved)
		if err != nil {
			return "", err
		}
		if uid >= 0 && uid != ownerUID {
			return "", ErrPathOutsideRoot
		}
	}
	return resolved, nil
}

// OpenInRootForRead opens a resolved file without following a symlink in the final component.
func OpenInRootForRead(resolved string) (*os.File, error) {
	return openNoFollow(resolved, os.O_RDONLY, 0)
}

// CreateInRoot creates/truncates a resolved file without following a symlink in the final component.
func CreateInRoot(resolved string, perm os.FileMode) (*os.File, error) {
	return openNoFollow(resolved, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
}
