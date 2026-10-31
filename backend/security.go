package backend

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/argon2"
)

// -------------------------------------------------------------
// Password hashing (Argon2id with transparent legacy SHA-256 migration)
// -------------------------------------------------------------

// Argon2id parameters (OWASP recommendation: m=46 MiB, t=1, p=1).
const (
	argonMemory  uint32 = 46 * 1024
	argonTime    uint32 = 1
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// MinPasswordLength is enforced for every newly set dashboard password.
const MinPasswordLength = 8

// hashSemaphore bounds concurrent Argon2 computations so parallel login
// attempts cannot exhaust host memory (each hash needs ~46 MiB).
var hashSemaphore = make(chan struct{}, 4)

// HashPassword returns a PHC-formatted Argon2id hash with a random salt.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSemaphore <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSemaphore
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// IsLegacyPasswordHash reports whether the stored hash is the old unsalted SHA-256 format.
func IsLegacyPasswordHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

// VerifyPassword checks a password against an Argon2id or legacy SHA-256 hash in constant time.
func VerifyPassword(password, stored string) bool {
	if IsLegacyPasswordHash(stored) {
		sum := sha256.Sum256([]byte(password))
		return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(stored))) == 1
	}

	parts := strings.Split(stored, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}
	// Refuse absurd parameters from a tampered config file.
	if memory == 0 || memory > 1024*1024 || timeCost == 0 || timeCost > 16 || threads == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) == 0 || len(expected) > 128 {
		return false
	}
	hashSemaphore <- struct{}{}
	actual := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(expected)))
	<-hashSemaphore
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// dummyHash is used to equalise timing for logins with unknown usernames.
var dummyHash = func() string {
	h, err := HashPassword("lgsm-dashboard-dummy-password")
	if err != nil {
		return ""
	}
	return h
}()

// -------------------------------------------------------------
// File helpers
// -------------------------------------------------------------

// WriteFileAtomic writes data to a temporary file in the same directory, fsyncs it,
// applies perm explicitly (independent of the process umask) and atomically renames it.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// -------------------------------------------------------------
// Input validation helpers
// -------------------------------------------------------------

var (
	reLinuxUsername = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	reScriptName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	reServerID      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)
)

// IsValidLinuxUsername validates usernames taken from /etc/passwd before they are
// passed to runuser/crontab/chown.
func IsValidLinuxUsername(u string) bool {
	return reLinuxUsername.MatchString(u)
}

// IsValidScriptName validates LinuxGSM script names (e.g. "vhserver", "mcserver-2").
func IsValidScriptName(s string) bool {
	return reScriptName.MatchString(s) && !strings.Contains(s, "..")
}

// IsValidServerID validates server IDs coming from URLs or JSON bodies. Servers on a node have
// the global ID "<node>~<local ID>"; '~' never occurs in local IDs.
func IsValidServerID(id string) bool {
	if node, local, ok := strings.Cut(id, "~"); ok {
		return IsValidNodeID(node) && IsValidLocalServerID(local)
	}
	return IsValidLocalServerID(id)
}

// IsValidLocalServerID validates an ID of a server on this machine (no node prefix).
func IsValidLocalServerID(id string) bool {
	return reServerID.MatchString(id) && !strings.Contains(id, "..")
}

// SplitServerID splits a global server ID into node and local ID. node is "" for servers of
// this machine.
func SplitServerID(id string) (node, local string) {
	if n, l, ok := strings.Cut(id, "~"); ok {
		return n, l
	}
	return "", id
}

// GlobalServerID returns the ID under which the controller shows a server of a node.
func GlobalServerID(node, local string) string {
	return node + "~" + local
}

// IsSafeCfgValue reports whether a value can be written as key="value" into a
// LinuxGSM .cfg file. These files are sourced by bash, so quotes, command
// substitution, escapes and line breaks would allow code execution.
func IsSafeCfgValue(v string) bool {
	if len(v) > 2048 {
		return false
	}
	return !strings.ContainsAny(v, "\"`$\\\n\r\x00")
}

// ShellQuote quotes s for safe use as a single POSIX shell word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ErrInvalidInput is returned for rejected user input.
var ErrInvalidInput = errors.New("invalid input")
