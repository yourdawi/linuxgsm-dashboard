package backend

import (
	"crypto/rand"
	"crypto/sha256"
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
}

type Config struct {
	Users []User `json:"users"`
	Port  int    `json:"port"`
}

type Session struct {
	Username string    `json:"username"`
	Expiry   time.Time `json:"expiry"`
}

type AuthManager struct {
	configPath string
	config     Config
	sessions   map[string]Session
	mu         sync.RWMutex
}

func NewAuthManager(configDir string) (*AuthManager, error) {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}
	
	am := &AuthManager{
		configPath: filepath.Join(configDir, "config.json"),
		sessions:   make(map[string]Session),
	}
	
	if err := am.loadOrCreateConfig(); err != nil {
		return nil, err
	}
	
	// Start session cleanup goroutine
	go am.cleanupSessionsLoop()
	
	return am, nil
}

func (am *AuthManager) GetPort() int {
	am.mu.RLock()
	defer am.mu.RUnlock()
	if am.config.Port == 0 {
		return 8080 // default port
	}
	return am.config.Port
}

// Temporary struct to detect and parse legacy config structures
type legacyConfig struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Port         int    `json:"port"`
	Users        []User `json:"users"`
}

func (am *AuthManager) loadOrCreateConfig() error {
	am.mu.Lock()
	defer am.mu.Unlock()

	if _, err := os.Stat(am.configPath); os.IsNotExist(err) {
		// Generate random 12-character password
		rawPassword, err := generateRandomPassword(12)
		if err != nil {
			return err
		}
		
		am.config = Config{
			Users: []User{
				{
					Username:     "admin",
					PasswordHash: hashPassword(rawPassword),
					Role:         "admin",
					Servers:      []string{}, // Admins bypass server checks
					Permissions:  []string{"start", "stop", "restart", "console", "config"},
				},
			},
			Port: 8080,
		}
		
		file, err := os.Create(am.configPath)
		if err != nil {
			return err
		}
		defer file.Close()
		
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(am.config); err != nil {
			return err
		}
		
		fmt.Println("==================================================================")
		fmt.Println("   FIRST LAUNCH: LinuxGSM WebAdmin Config Generated!")
		fmt.Println("   Username: admin")
		fmt.Printf("   Password: %s\n", rawPassword)
		fmt.Println("   Configuration saved to: " + am.configPath)
		fmt.Println("==================================================================")
	} else {
		file, err := os.Open(am.configPath)
		if err != nil {
			return err
		}
		defer file.Close()

		var raw legacyConfig
		if err := json.NewDecoder(file).Decode(&raw); err != nil {
			return err
		}

		// Handle legacy single-user configuration migration
		if len(raw.Users) == 0 {
			adminUser := User{
				Username:     raw.Username,
				PasswordHash: raw.PasswordHash,
				Role:         "admin",
				Servers:      []string{},
				Permissions:  []string{"start", "stop", "restart", "console", "config"},
			}
			if adminUser.Username == "" {
				adminUser.Username = "admin"
			}
			if adminUser.PasswordHash == "" {
				rawPassword, _ := generateRandomPassword(12)
				adminUser.PasswordHash = hashPassword(rawPassword)
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
			fileToSave, err := os.Create(am.configPath)
			if err != nil {
				return err
			}
			defer fileToSave.Close()
			encoder := json.NewEncoder(fileToSave)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(am.config); err != nil {
				return err
			}
		}
	}
	
	return nil
}

func (am *AuthManager) Login(username, password string) (string, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	expectedHash := hashPassword(password)
	
	var authenticatedUser *User
	for _, u := range am.config.Users {
		if u.Username == username && u.PasswordHash == expectedHash {
			authenticatedUser = &u
			break
		}
	}

	if authenticatedUser == nil {
		return "", errors.New("invalid credentials")
	}

	sessionID, err := generateRandomHex(32)
	if err != nil {
		return "", err
	}

	am.mu.RUnlock()
	am.mu.Lock()
	am.sessions[sessionID] = Session{
		Username: username,
		Expiry:   time.Now().Add(24 * time.Hour), // 1 day session
	}
	am.mu.Unlock()
	am.mu.RLock()

	return sessionID, nil
}

func (am *AuthManager) Logout(sessionID string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	delete(am.sessions, sessionID)
}

func (am *AuthManager) IsValidSession(sessionID string) bool {
	_, ok := am.GetSession(sessionID)
	return ok
}

func (am *AuthManager) GetSession(sessionID string) (Session, bool) {
	am.mu.RLock()
	session, exists := am.sessions[sessionID]
	am.mu.RUnlock()

	if !exists {
		return Session{}, false
	}

	if time.Now().After(session.Expiry) {
		am.mu.Lock()
		delete(am.sessions, sessionID)
		am.mu.Unlock()
		return Session{}, false
	}

	// Extend session duration on active use
	am.mu.Lock()
	session.Expiry = time.Now().Add(2 * time.Hour)
	am.sessions[sessionID] = session
	am.mu.Unlock()

	return session, true
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

func (am *AuthManager) CreateUser(username, password, role string, servers, permissions []string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	for _, u := range am.config.Users {
		if u.Username == username {
			return errors.New("user already exists")
		}
	}
	
	newUser := User{
		Username:     username,
		PasswordHash: hashPassword(password),
		Role:         role,
		Servers:      servers,
		Permissions:  permissions,
	}
	
	am.config.Users = append(am.config.Users, newUser)
	return am.saveConfigLocked()
}

func (am *AuthManager) UpdateUser(username, password, role string, servers, permissions []string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	found := false
	for i, u := range am.config.Users {
		if u.Username == username {
			if password != "" {
				am.config.Users[i].PasswordHash = hashPassword(password)
			}
			am.config.Users[i].Role = role
			am.config.Users[i].Servers = servers
			am.config.Users[i].Permissions = permissions
			found = true
			break
		}
	}
	
	if !found {
		return errors.New("user not found")
	}
	return am.saveConfigLocked()
}

func (am *AuthManager) DeleteUser(username string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	adminCount := 0
	for _, u := range am.config.Users {
		if u.Role == "admin" {
			adminCount++
		}
	}
	
	foundIdx := -1
	for i, u := range am.config.Users {
		if u.Username == username {
			if u.Role == "admin" && adminCount <= 1 {
				return errors.New("cannot delete the only remaining administrator")
			}
			foundIdx = i
			break
		}
	}
	
	if foundIdx == -1 {
		return errors.New("user not found")
	}
	
	am.config.Users = append(am.config.Users[:foundIdx], am.config.Users[foundIdx+1:]...)
	return am.saveConfigLocked()
}

func (am *AuthManager) ChangeUserPassword(username, oldPassword, newPassword string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	for i, u := range am.config.Users {
		if u.Username == username {
			if hashPassword(oldPassword) != u.PasswordHash {
				return errors.New("current password incorrect")
			}
			am.config.Users[i].PasswordHash = hashPassword(newPassword)
			return am.saveConfigLocked()
		}
	}
	return errors.New("user not found")
}

func (am *AuthManager) ChangePassword(oldPassword, newPassword string) error {
	// For backwards compatibility, update the first admin
	am.mu.RLock()
	var adminUsername string
	for _, u := range am.config.Users {
		if u.Role == "admin" {
			adminUsername = u.Username
			break
		}
	}
	am.mu.RUnlock()

	if adminUsername == "" {
		return errors.New("no administrator user found")
	}
	return am.ChangeUserPassword(adminUsername, oldPassword, newPassword)
}

func (am *AuthManager) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_id")
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
	file, err := os.Create(am.configPath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(am.config)
}

func (am *AuthManager) cleanupSessionsLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	for range ticker.C {
		am.mu.Lock()
		now := time.Now()
		for id, session := range am.sessions {
			if now.After(session.Expiry) {
				delete(am.sessions, id)
			}
		}
		am.mu.Unlock()
	}
}

// Helpers
func hashPassword(password string) string {
	hasher := sha256.New()
	hasher.Write([]byte(password))
	return hex.EncodeToString(hasher.Sum(nil))
}

func generateRandomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func generateRandomPassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b), nil
}
