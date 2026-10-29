package backend

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type User struct {
	Username     string   `json:"username"`
	PasswordHash string   `json:"password_hash"`
	Role         string   `json:"role"`        // "admin" or "user"
	Servers      []string `json:"servers"`     // list of ServerIDs (e.g. ["arkserver"])
	Permissions  []string `json:"permissions"` // e.g. ["start", "stop", "restart", "console", "config"]

	// Two-factor authentication (see twofactor.go). Each user has an independent secret and
	// independent recovery codes (stored as hashes bound to the username).
	TOTPEnabled   bool     `json:"totp_enabled,omitempty"`
	TOTPSecret    string   `json:"totp_secret,omitempty"`
	TOTPLastStep  int64    `json:"totp_last_step,omitempty"`
	RecoveryCodes []string `json:"recovery_codes,omitempty"`
}

// Sanitized returns a copy without password hash and 2FA secrets (for API responses).
func (u User) Sanitized() User {
	u.PasswordHash = ""
	u.TOTPSecret = ""
	u.TOTPLastStep = 0
	u.RecoveryCodes = nil
	return u
}

type Config struct {
	Users []User `json:"users"`
	Port  int    `json:"port"`
}

type Session struct {
	Username  string    `json:"username"`
	Expiry    time.Time `json:"expiry"`
	Created   time.Time `json:"created"`
	CSRFToken string    `json:"-"`
}

// Session lifetimes: sliding idle timeout and an absolute maximum lifetime.
const (
	SessionIdleTimeout = 24 * time.Hour
	SessionMaxLifetime = 7 * 24 * time.Hour
)

// SessionCookieName is the name of the HttpOnly session cookie.
const SessionCookieName = "session_id"

// CSRFCookieName is readable by the frontend, which echoes it in the X-CSRF-Token header.
const CSRFCookieName = "csrf_token"

type AuthManager struct {
	configPath string
	config     Config
	sessions   map[string]Session
	challenges map[string]loginChallenge // pending second login steps (2FA)
	setups     map[string]pendingSetup   // 2FA setups waiting for confirmation, by username
	mu         sync.RWMutex
	auditMu    sync.Mutex
	now        func() time.Time
}

func NewAuthManager(configDir string) (*AuthManager, error) {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, err
	}
	// The directory contains password hashes and the audit log.
	_ = os.Chmod(configDir, 0700)

	am := &AuthManager{
		configPath: filepath.Join(configDir, "config.json"),
		sessions:   make(map[string]Session),
		challenges: make(map[string]loginChallenge),
		setups:     make(map[string]pendingSetup),
		now:        time.Now,
	}

	if err := am.loadOrCreateConfig(); err != nil {
		return nil, err
	}

	// Tighten permissions of files created by older versions.
	_ = os.Chmod(am.configPath, 0600)
	_ = os.Chmod(am.auditPath(), 0600)

	// Start session cleanup goroutine
	go am.cleanupSessionsLoop()

	return am, nil
}

// NewAuditOnlyAuthManager is used by agents: they have no users and no logins, but keep their
// own audit log (audit_log.json in configDir) of what the controller did on the node.
func NewAuditOnlyAuthManager(configDir string) (*AuthManager, error) {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(configDir, 0700)
	return &AuthManager{
		configPath: filepath.Join(configDir, "config.json"),
		sessions:   make(map[string]Session),
		challenges: make(map[string]loginChallenge),
		setups:     make(map[string]pendingSetup),
		now:        time.Now,
	}, nil
}

func (am *AuthManager) GetPort() int {
	am.mu.RLock()
	defer am.mu.RUnlock()
	if am.config.Port == 0 {
		return 8080 // default port
	}
	return am.config.Port
}

// ConfigDir returns the directory holding the dashboard configuration.
func (am *AuthManager) ConfigDir() string {
	return filepath.Dir(am.configPath)
}

// Temporary struct to detect and parse legacy config structures
type legacyConfig struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Port         int    `json:"port"`
	Users        []User `json:"users"`
}

func defaultAdminPermissions() []string {
	perms := make([]string, len(AssignablePermissions))
	copy(perms, AssignablePermissions)
	return perms
}

func (am *AuthManager) loadOrCreateConfig() error {
	am.mu.Lock()
	defer am.mu.Unlock()

	if _, err := os.Stat(am.configPath); os.IsNotExist(err) {
		rawPassword, err := generateRandomPassword(16)
		if err != nil {
			return err
		}
		hash, err := HashPassword(rawPassword)
		if err != nil {
			return err
		}

		am.config = Config{
			Users: []User{
				{
					Username:     "admin",
					PasswordHash: hash,
					Role:         RoleAdmin,
					Servers:      []string{}, // Admins bypass server checks
					Permissions:  defaultAdminPermissions(),
				},
			},
			Port: 8080,
		}

		if err := am.saveConfigLocked(); err != nil {
			return err
		}

		fmt.Println("==================================================================")
		fmt.Println("   FIRST LAUNCH: LinuxGSM WebAdmin Config Generated!")
		fmt.Println("   Username: admin")
		fmt.Printf("   Password: %s\n", rawPassword)
		fmt.Println("   Configuration saved to: " + am.configPath)
		fmt.Println("==================================================================")
		return nil
	}

	data, err := os.ReadFile(am.configPath)
	if err != nil {
		return err
	}

	var raw legacyConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Handle legacy single-user configuration migration
	if len(raw.Users) == 0 {
		adminUser := User{
			Username:     raw.Username,
			PasswordHash: raw.PasswordHash,
			Role:         RoleAdmin,
			Servers:      []string{},
			Permissions:  defaultAdminPermissions(),
		}
		if adminUser.Username == "" {
			adminUser.Username = "admin"
		}
		if adminUser.PasswordHash == "" {
			rawPassword, err := generateRandomPassword(16)
			if err != nil {
				return err
			}
			hash, err := HashPassword(rawPassword)
			if err != nil {
				return err
			}
			adminUser.PasswordHash = hash
			fmt.Println("==================================================================")
			fmt.Println("   LEGACY CONFIG MIGRATION: Password generated!")
			fmt.Println("   Username: admin")
			fmt.Printf("   Password: %s\n", rawPassword)
			fmt.Println("==================================================================")
		}
		am.config.Users = []User{adminUser}
	} else {
		am.config.Users = raw.Users
	}
	am.config.Port = raw.Port

	// Save the migrated config if it was in legacy format
	if len(raw.Users) == 0 {
		if err := am.saveConfigLocked(); err != nil {
			return err
		}
	}

	return nil
}

// Login verifies the credentials and creates a new session. Accounts with two-factor
// authentication return ErrTwoFactorRequired; use BeginLogin/CompleteLogin for those.
func (am *AuthManager) Login(username, password string) (string, error) {
	res, err := am.BeginLogin(username, password)
	if err != nil {
		return "", err
	}
	if res.Challenge != "" {
		return "", ErrTwoFactorRequired
	}
	return res.SessionID, nil
}

// verifyCredentials checks username and password. Legacy SHA-256 hashes are transparently
// upgraded to Argon2id on the first successful check.
func (am *AuthManager) verifyCredentials(username, password string) error {
	am.mu.RLock()
	var storedHash string
	found := false
	for _, u := range am.config.Users {
		if u.Username == username {
			storedHash = u.PasswordHash
			found = true
			break
		}
	}
	am.mu.RUnlock()

	if !found {
		// Burn comparable CPU time so response timing does not reveal valid usernames.
		VerifyPassword(password, dummyHash)
		return errors.New("invalid credentials")
	}
	if !VerifyPassword(password, storedHash) {
		return errors.New("invalid credentials")
	}

	if IsLegacyPasswordHash(storedHash) {
		if newHash, err := HashPassword(password); err == nil {
			am.mu.Lock()
			for i := range am.config.Users {
				// Only replace if nobody changed the hash concurrently; never downgrade.
				if am.config.Users[i].Username == username && am.config.Users[i].PasswordHash == storedHash {
					am.config.Users[i].PasswordHash = newHash
					if err := am.saveConfigLocked(); err != nil {
						am.config.Users[i].PasswordHash = storedHash
						fmt.Printf("[SECURITY] Failed to persist migrated password hash for %s: %v\n", username, err)
					}
					break
				}
			}
			am.mu.Unlock()
		}
	}
	return nil
}

func (am *AuthManager) createSession(username string) (string, error) {
	sessionID, err := generateRandomHex(32)
	if err != nil {
		return "", err
	}
	csrfToken, err := generateRandomHex(32)
	if err != nil {
		return "", err
	}
	now := am.now()
	am.mu.Lock()
	am.sessions[sessionID] = Session{
		Username:  username,
		Expiry:    now.Add(SessionIdleTimeout),
		Created:   now,
		CSRFToken: csrfToken,
	}
	am.mu.Unlock()
	return sessionID, nil
}

func (am *AuthManager) Logout(sessionID string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	delete(am.sessions, sessionID)
}

// InvalidateUserSessions removes all sessions of username except exceptSessionID (may be empty).
func (am *AuthManager) InvalidateUserSessions(username, exceptSessionID string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.invalidateUserSessionsLocked(username, exceptSessionID)
}

func (am *AuthManager) invalidateUserSessionsLocked(username, exceptSessionID string) {
	for id, s := range am.sessions {
		if s.Username == username && id != exceptSessionID {
			delete(am.sessions, id)
		}
	}
}

func (am *AuthManager) IsValidSession(sessionID string) bool {
	_, ok := am.GetSession(sessionID)
	return ok
}

func (am *AuthManager) GetSession(sessionID string) (Session, bool) {
	if sessionID == "" {
		return Session{}, false
	}
	am.mu.Lock()
	defer am.mu.Unlock()

	session, exists := am.sessions[sessionID]
	if !exists {
		return Session{}, false
	}

	now := am.now()
	if now.After(session.Expiry) || now.Sub(session.Created) > SessionMaxLifetime {
		delete(am.sessions, sessionID)
		return Session{}, false
	}

	// Sliding idle timeout on active use
	session.Expiry = now.Add(SessionIdleTimeout)
	am.sessions[sessionID] = session
	return session, true
}

// ValidateCSRF checks the double-submit token against the token bound to the session.
func (am *AuthManager) ValidateCSRF(sessionID, token string) bool {
	if token == "" {
		return false
	}
	am.mu.RLock()
	session, ok := am.sessions[sessionID]
	am.mu.RUnlock()
	if !ok || session.CSRFToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(session.CSRFToken), []byte(token)) == 1
}

func (am *AuthManager) GetUser(username string) (User, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	for _, u := range am.config.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

func (am *AuthManager) GetUsers() []User {
	am.mu.RLock()
	defer am.mu.RUnlock()
	users := make([]User, len(am.config.Users))
	copy(users, am.config.Users)
	return users
}

func validateRoleAndPermissions(role string, permissions []string) error {
	if role != RoleAdmin && role != RoleUser {
		return errors.New("invalid role")
	}
	for _, p := range permissions {
		if !IsAssignablePermission(p) {
			return fmt.Errorf("invalid permission: %s", p)
		}
	}
	return nil
}

func (am *AuthManager) CreateUser(username, password, role string, servers, permissions []string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if err := validateRoleAndPermissions(role, permissions); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	for _, u := range am.config.Users {
		if u.Username == username {
			return errors.New("user already exists")
		}
	}

	if servers == nil {
		servers = []string{}
	}
	if permissions == nil {
		permissions = []string{}
	}

	newUser := User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Servers:      servers,
		Permissions:  permissions,
	}

	am.config.Users = append(am.config.Users, newUser)
	if err := am.saveConfigLocked(); err != nil {
		am.config.Users = am.config.Users[:len(am.config.Users)-1]
		return err
	}
	return nil
}

func (am *AuthManager) UpdateUser(username, password, role string, servers, permissions []string) error {
	if password != "" && len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if err := validateRoleAndPermissions(role, permissions); err != nil {
		return err
	}
	var newHash string
	if password != "" {
		h, err := HashPassword(password)
		if err != nil {
			return err
		}
		newHash = h
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	adminCount := 0
	for _, u := range am.config.Users {
		if u.Role == RoleAdmin {
			adminCount++
		}
	}

	for i, u := range am.config.Users {
		if u.Username != username {
			continue
		}
		if u.Role == RoleAdmin && role != RoleAdmin && adminCount <= 1 {
			return errors.New("cannot demote the only remaining administrator")
		}
		previous := am.config.Users[i]
		if newHash != "" {
			am.config.Users[i].PasswordHash = newHash
		}
		if servers == nil {
			servers = []string{}
		}
		if permissions == nil {
			permissions = []string{}
		}
		am.config.Users[i].Role = role
		am.config.Users[i].Servers = servers
		am.config.Users[i].Permissions = permissions
		if err := am.saveConfigLocked(); err != nil {
			am.config.Users[i] = previous
			return err
		}
		if newHash != "" {
			// A password reset must log out every existing session of that user.
			am.invalidateUserSessionsLocked(username, "")
		}
		return nil
	}
	return errors.New("user not found")
}

// RemoveServerFromUsers removes a deleted server from the server lists of all users.
func (am *AuthManager) RemoveServerFromUsers(serverID string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	changed := false
	for i, u := range am.config.Users {
		kept := make([]string, 0, len(u.Servers))
		for _, s := range u.Servers {
			if s != serverID {
				kept = append(kept, s)
			}
		}
		if len(kept) != len(u.Servers) {
			am.config.Users[i].Servers = kept
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return am.saveConfigLocked()
}

func (am *AuthManager) DeleteUser(username string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	adminCount := 0
	for _, u := range am.config.Users {
		if u.Role == RoleAdmin {
			adminCount++
		}
	}

	foundIdx := -1
	for i, u := range am.config.Users {
		if u.Username == username {
			if u.Role == RoleAdmin && adminCount <= 1 {
				return errors.New("cannot delete the only remaining administrator")
			}
			foundIdx = i
			break
		}
	}

	if foundIdx == -1 {
		return errors.New("user not found")
	}

	previous := make([]User, len(am.config.Users))
	copy(previous, am.config.Users)
	am.config.Users = append(am.config.Users[:foundIdx], am.config.Users[foundIdx+1:]...)
	if err := am.saveConfigLocked(); err != nil {
		am.config.Users = previous
		return err
	}
	am.invalidateUserSessionsLocked(username, "")
	delete(am.setups, username)
	for token, c := range am.challenges {
		if c.username == username {
			delete(am.challenges, token)
		}
	}
	return nil
}

// ChangeUserPassword changes the password after verifying the old one. All other sessions of
// the user are invalidated; keepSessionID (the caller's session) stays valid.
func (am *AuthManager) ChangeUserPassword(username, oldPassword, newPassword, keepSessionID string) error {
	if len(newPassword) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}

	am.mu.RLock()
	var stored string
	found := false
	for _, u := range am.config.Users {
		if u.Username == username {
			stored = u.PasswordHash
			found = true
			break
		}
	}
	am.mu.RUnlock()
	if !found {
		return errors.New("user not found")
	}
	if !VerifyPassword(oldPassword, stored) {
		return errors.New("current password incorrect")
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	am.mu.Lock()
	defer am.mu.Unlock()
	for i, u := range am.config.Users {
		if u.Username == username {
			if u.PasswordHash != stored {
				return errors.New("password was changed concurrently, please retry")
			}
			am.config.Users[i].PasswordHash = newHash
			if err := am.saveConfigLocked(); err != nil {
				am.config.Users[i].PasswordHash = stored
				return err
			}
			am.invalidateUserSessionsLocked(username, keepSessionID)
			return nil
		}
	}
	return errors.New("user not found")
}

func (am *AuthManager) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || !am.IsValidSession(cookie.Value) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
			return
		}
		next(w, r)
	}
}

func (am *AuthManager) saveConfigLocked() error {
	data, err := json.MarshalIndent(am.config, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(am.configPath, append(data, '\n'), 0600)
}

func (am *AuthManager) cleanupSessionsLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	for range ticker.C {
		am.mu.Lock()
		now := am.now()
		for id, session := range am.sessions {
			if now.After(session.Expiry) || now.Sub(session.Created) > SessionMaxLifetime {
				delete(am.sessions, id)
			}
		}
		for token, c := range am.challenges {
			if now.After(c.expiry) {
				delete(am.challenges, token)
			}
		}
		for user, st := range am.setups {
			if now.After(st.expiry) {
				delete(am.setups, user)
			}
		}
		am.mu.Unlock()
	}
}

// Helpers

func generateRandomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func generateRandomPassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, 0, length)
	buf := make([]byte, 1)
	// Rejection sampling avoids the modulo bias of byte % len(charset).
	limit := 256 - (256 % len(charset))
	for len(result) < length {
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return "", err
		}
		if int(buf[0]) >= limit {
			continue
		}
		result = append(result, charset[int(buf[0])%len(charset)])
	}
	return string(result), nil
}

type AuditEntry struct {
	Timestamp string `json:"timestamp"`
	Username  string `json:"username"`
	IP        string `json:"ip"`
	Action    string `json:"action"`
	Details   string `json:"details"`
	ServerID  string `json:"server_id,omitempty"`
}

func (am *AuthManager) auditPath() string {
	return filepath.Join(filepath.Dir(am.configPath), "audit_log.json")
}

func (am *AuthManager) LogAudit(username, ip, action, details, serverID string) {
	am.auditMu.Lock()
	defer am.auditMu.Unlock()

	auditFile := am.auditPath()
	var entries []AuditEntry

	if data, err := os.ReadFile(auditFile); err == nil {
		json.Unmarshal(data, &entries)
	}

	newEntry := AuditEntry{
		Timestamp: am.now().Format("2006-01-02 15:04:05"),
		Username:  username,
		IP:        ip,
		Action:    action,
		Details:   details,
		ServerID:  serverID,
	}

	entries = append([]AuditEntry{newEntry}, entries...)
	if len(entries) > 1000 {
		entries = entries[:1000]
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err == nil {
		if err := WriteFileAtomic(auditFile, data, 0600); err != nil {
			fmt.Printf("[WARNING] Failed to write audit log: %v\n", err)
		}
	}
}

func (am *AuthManager) GetAuditLogs() []AuditEntry {
	am.auditMu.Lock()
	defer am.auditMu.Unlock()

	var entries []AuditEntry

	if data, err := os.ReadFile(am.auditPath()); err == nil {
		json.Unmarshal(data, &entries)
	}

	if entries == nil {
		entries = []AuditEntry{}
	}
	return entries
}
