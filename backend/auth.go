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

type Config struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Port         int    `json:"port"`
}

type AuthManager struct {
	configPath string
	config     Config
	sessions   map[string]time.Time
	mu         sync.RWMutex
}

func NewAuthManager(configDir string) (*AuthManager, error) {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}
	
	am := &AuthManager{
		configPath: filepath.Join(configDir, "config.json"),
		sessions:   make(map[string]time.Time),
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
			Username:     "admin",
			PasswordHash: hashPassword(rawPassword),
			Port:         8080,
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
		
		if err := json.NewDecoder(file).Decode(&am.config); err != nil {
			return err
		}
	}
	
	return nil
}

func (am *AuthManager) Login(username, password string) (string, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	expectedHash := hashPassword(password)
	if username != am.config.Username || expectedHash != am.config.PasswordHash {
		return "", errors.New("invalid credentials")
	}

	sessionID, err := generateRandomHex(32)
	if err != nil {
		return "", err
	}

	am.mu.RUnlock()
	am.mu.Lock()
	am.sessions[sessionID] = time.Now().Add(24 * time.Hour) // 1 day session
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
	am.mu.RLock()
	expiry, exists := am.sessions[sessionID]
	am.mu.RUnlock()

	if !exists {
		return false
	}

	if time.Now().After(expiry) {
		am.mu.Lock()
		delete(am.sessions, sessionID)
		am.mu.Unlock()
		return false
	}

	// Extend session duration on active use
	am.mu.Lock()
	am.sessions[sessionID] = time.Now().Add(2 * time.Hour)
	am.mu.Unlock()

	return true
}

func (am *AuthManager) ChangePassword(oldPassword, newPassword string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	if hashPassword(oldPassword) != am.config.PasswordHash {
		return errors.New("old password incorrect")
	}

	am.config.PasswordHash = hashPassword(newPassword)

	file, err := os.Create(am.configPath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(am.config)
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

func (am *AuthManager) cleanupSessionsLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	for range ticker.C {
		am.mu.Lock()
		now := time.Now()
		for id, expiry := range am.sessions {
			if now.After(expiry) {
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
