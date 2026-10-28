package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestAuth(t *testing.T) *AuthManager {
	t.Helper()
	am, err := NewAuthManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return am
}

func TestLoginValidAndInvalid(t *testing.T) {
	am := newTestAuth(t)
	if err := am.CreateUser("alice", "alice-password", RoleUser, nil, []string{PermStart}); err != nil {
		t.Fatal(err)
	}
	sid, err := am.Login("alice", "alice-password")
	if err != nil || sid == "" {
		t.Fatalf("valid login failed: %v", err)
	}
	s, ok := am.GetSession(sid)
	if !ok || s.Username != "alice" || len(s.CSRFToken) != 64 {
		t.Fatalf("unexpected session %+v", s)
	}
	if _, err := am.Login("alice", "wrong-password"); err == nil {
		t.Fatal("invalid password accepted")
	}
	if _, err := am.Login("nobody", "alice-password"); err == nil {
		t.Fatal("unknown user accepted")
	}
}

func TestCreateUserValidation(t *testing.T) {
	am := newTestAuth(t)
	if err := am.CreateUser("bob", "short", RoleUser, nil, nil); err == nil {
		t.Fatal("short password accepted")
	}
	if err := am.CreateUser("bob", "long-enough", "superuser", nil, nil); err == nil {
		t.Fatal("invalid role accepted")
	}
	if err := am.CreateUser("bob", "long-enough", RoleUser, nil, []string{"root"}); err == nil {
		t.Fatal("invalid permission accepted")
	}
}

func TestLegacyPasswordMigration(t *testing.T) {
	dir := t.TempDir()
	sum := sha256.Sum256([]byte("old-password"))
	legacy := hex.EncodeToString(sum[:])
	cfg := Config{Users: []User{{Username: "admin", PasswordHash: legacy, Role: RoleAdmin}}, Port: 8080}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	am, err := NewAuthManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := am.Login("admin", "old-password"); err != nil {
		t.Fatalf("legacy login failed: %v", err)
	}
	u, _ := am.GetUser("admin")
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("hash not migrated: %q", u.PasswordHash)
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if strings.Contains(string(onDisk), legacy) || !strings.Contains(string(onDisk), "$argon2id$") {
		t.Fatal("migrated hash not persisted")
	}
	// The migrated hash keeps working and is never downgraded.
	if _, err := am.Login("admin", "old-password"); err != nil {
		t.Fatalf("login after migration failed: %v", err)
	}
	u2, _ := am.GetUser("admin")
	if IsLegacyPasswordHash(u2.PasswordHash) {
		t.Fatal("hash downgraded")
	}
}

func TestSessionExpiryAndLifetime(t *testing.T) {
	am := newTestAuth(t)
	_ = am.CreateUser("alice", "alice-password", RoleUser, nil, nil)
	now := time.Now()
	am.now = func() time.Time { return now }

	sid, _ := am.Login("alice", "alice-password")
	now = now.Add(SessionIdleTimeout + time.Minute)
	if _, ok := am.GetSession(sid); ok {
		t.Fatal("idle session not expired")
	}

	sid, _ = am.Login("alice", "alice-password")
	for i := 0; i < 10; i++ {
		now = now.Add(SessionIdleTimeout - time.Hour)
		am.GetSession(sid)
	}
	if _, ok := am.GetSession(sid); ok {
		t.Fatal("absolute session lifetime not enforced")
	}
}

func TestSessionsInvalidatedOnPasswordChangeAndDelete(t *testing.T) {
	am := newTestAuth(t)
	_ = am.CreateUser("alice", "alice-password", RoleUser, nil, nil)
	current, _ := am.Login("alice", "alice-password")
	other, _ := am.Login("alice", "alice-password")

	if err := am.ChangeUserPassword("alice", "wrong", "new-password-1", current); err == nil {
		t.Fatal("wrong old password accepted")
	}
	if err := am.ChangeUserPassword("alice", "alice-password", "new-password-1", current); err != nil {
		t.Fatal(err)
	}
	if !am.IsValidSession(current) {
		t.Fatal("current session should survive own password change")
	}
	if am.IsValidSession(other) {
		t.Fatal("other sessions must be invalidated after password change")
	}

	// admin reset logs out everything
	if err := am.UpdateUser("alice", "reset-password", RoleUser, nil, nil); err != nil {
		t.Fatal(err)
	}
	if am.IsValidSession(current) {
		t.Fatal("sessions must be invalidated after password reset")
	}

	sid, _ := am.Login("alice", "reset-password")
	if err := am.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	if am.IsValidSession(sid) {
		t.Fatal("sessions of deleted users must be invalidated")
	}
}

func TestLastAdminProtection(t *testing.T) {
	am := newTestAuth(t)
	if err := am.UpdateUser("admin", "", RoleUser, nil, nil); err == nil {
		t.Fatal("demoting the last admin must fail")
	}
	if err := am.DeleteUser("admin"); err == nil {
		t.Fatal("deleting the last admin must fail")
	}
}

func TestCSRFValidation(t *testing.T) {
	am := newTestAuth(t)
	_ = am.CreateUser("alice", "alice-password", RoleUser, nil, nil)
	sid, _ := am.Login("alice", "alice-password")
	s, _ := am.GetSession(sid)
	if !am.ValidateCSRF(sid, s.CSRFToken) {
		t.Fatal("valid token rejected")
	}
	for _, tok := range []string{"", "x", strings.Repeat("0", 64)} {
		if am.ValidateCSRF(sid, tok) {
			t.Fatalf("invalid token accepted: %q", tok)
		}
	}
	other, _ := am.Login("alice", "alice-password")
	if am.ValidateCSRF(other, s.CSRFToken) {
		t.Fatal("token of another session accepted")
	}
}

func TestConfigAndAuditFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	am, err := NewAuthManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	am.LogAudit("admin", "127.0.0.1", "TEST", "details", "")
	for _, name := range []string{"config.json", "audit_log.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("%s has mode %o, want 600", name, perm)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.json")
	if err := WriteFileAtomic(p, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "two" {
		t.Fatalf("got %q", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatal("temporary files left behind")
	}
}

func TestRemoveServerFromUsers(t *testing.T) {
	am := newTestAuth(t)
	if err := am.CreateUser("viewer", "Viewer-Pass-123", RoleUser, []string{"ts3server", "vhserver"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := am.RemoveServerFromUsers("ts3server"); err != nil {
		t.Fatal(err)
	}
	u, _ := am.GetUser("viewer")
	if len(u.Servers) != 1 || u.Servers[0] != "vhserver" {
		t.Fatalf("servers = %v", u.Servers)
	}
}
