package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yourdawi/linuxgsm-dashboard/backend"
)

// Version represents the current version of the dashboard
var Version = "1.0.5"

//go:embed ui/*
var uiFS embed.FS

// Helper to validate username (3-16 chars, lowercase alphanumeric or underscore)
func isValidUsername(u string) bool {
	if len(u) < 3 || len(u) > 16 {
		return false
	}
	for _, r := range u {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// Helper to validate gameCmd (ends with server, lowercase alphanumeric or dash)
func isValidGameCmd(c string) bool {
	if !strings.HasSuffix(c, "server") || len(c) < 7 {
		return false
	}
	for _, r := range c {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// Helper to validate action against whitelist
func isValidAction(a string) bool {
	switch a {
	case "start", "stop", "restart", "update", "details", "backup", "validate", "update-lgsm", "force-update", "test-alert", "map-wipe", "full-wipe", "change-password", "check-update", "mods-install", "mods-update", "mods-remove", "fastdl", "map-compressor", "postdetails", "skeleton", "debug", "install-default-resources":
		return true
	}
	return false
}

// Helper to get logged in user details from request session
func getLoggedInUser(r *http.Request, authMgr *backend.AuthManager) (backend.User, error) {
	cookie, err := r.Cookie("session_id")
	if err != nil {
		return backend.User{}, errors.New("unauthorized")
	}
	session, ok := authMgr.GetSession(cookie.Value)
	if !ok {
		return backend.User{}, errors.New("unauthorized")
	}
	user, ok := authMgr.GetUser(session.Username)
	if !ok {
		return backend.User{}, errors.New("unauthorized")
	}
	return user, nil
}

// Helper to check if user has access to a specific server
func isUserAllowedServer(user backend.User, serverID string) bool {
	if user.Role == "admin" {
		return true
	}
	for _, srv := range user.Servers {
		if srv == serverID {
			return true
		}
	}
	return false
}

// Helper to check if user has specific permission scope
func hasUserPermission(user backend.User, perm string) bool {
	if user.Role == "admin" {
		return true
	}
	for _, p := range user.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// Helper to query the latest release from GitHub
func fetchLatestRelease() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/yourdawi/linuxgsm-dashboard/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "linuxgsm-dashboard")
	
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned status %d", resp.StatusCode)
	}
	
	var release struct {
		TagName string `json:"tag_name"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	
	return release.TagName, nil
}

// Helper to update by pulling the git tag and compiling locally
func triggerSelfUpdateByGit(tagName string) error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return err
	}
	execDir := filepath.Dir(execPath)

	// Check if this is a git repository
	if _, err := os.Stat(filepath.Join(execDir, ".git")); os.IsNotExist(err) {
		return errors.New("cannot update from source: installation directory is not a git repository")
	}

	// 1. Run git fetch --tags
	cmdFetch := exec.Command("git", "fetch", "--tags")
	cmdFetch.Dir = execDir
	if err := cmdFetch.Run(); err != nil {
		return fmt.Errorf("failed to fetch git tags: %v", err)
	}

	// 2. Run git checkout <tagName>
	cmdCheckout := exec.Command("git", "checkout", tagName)
	cmdCheckout.Dir = execDir
	if err := cmdCheckout.Run(); err != nil {
		return fmt.Errorf("failed to checkout tag %s: %v", tagName, err)
	}

	// 3. Run go build -o lgsm-dashboard.new main.go
	cmdBuild := exec.Command("go", "build", "-o", "lgsm-dashboard.new", "main.go")
	cmdBuild.Dir = execDir
	if err := cmdBuild.Run(); err != nil {
		return fmt.Errorf("failed to compile new version: %v", err)
	}

	// 4. Safe swap
	newPath := filepath.Join(execDir, "lgsm-dashboard.new")
	oldPath := execPath + ".old"
	_ = os.Remove(oldPath)

	err = os.Rename(execPath, oldPath)
	if err != nil {
		return fmt.Errorf("failed to backup current binary: %v", err)
	}

	err = os.Rename(newPath, execPath)
	if err != nil {
		_ = os.Rename(oldPath, execPath) // try to rollback
		return fmt.Errorf("failed to swap binary: %v", err)
	}

	return nil
}

// Helper to migrate systemd service file from PrivateTmp=true to PrivateTmp=false retroactively
func migrateSystemdService() {
	if runtime.GOOS == "windows" {
		return
	}

	servicePath := "/etc/systemd/system/lgsm-dashboard.service"
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		return
	}

	contentBytes, err := os.ReadFile(servicePath)
	if err != nil {
		fmt.Printf("[SYS WARNING] Failed to read systemd service file for migration: %v\n", err)
		return
	}

	content := string(contentBytes)
	if strings.Contains(content, "PrivateTmp=true") {
		fmt.Println("[SYS] Found legacy PrivateTmp=true in systemd service file. Migrating to PrivateTmp=false...")

		// Replace PrivateTmp=true with PrivateTmp=false and comment
		replacement := "# PrivateTmp must be false so tmux sockets can be shared between the daemon and gameserver users\nPrivateTmp=false"
		newContent := strings.Replace(content, "PrivateTmp=true", replacement, 1)

		err = os.WriteFile(servicePath, []byte(newContent), 0644)
		if err != nil {
			fmt.Printf("[SYS ERROR] Failed to write updated systemd service file: %v\n", err)
			return
		}

		// Reload systemd daemon
		cmdReload := exec.Command("systemctl", "daemon-reload")
		if err := cmdReload.Run(); err != nil {
			fmt.Printf("[SYS ERROR] Failed to run systemctl daemon-reload: %v\n", err)
			return
		}

		fmt.Println("[SYS] systemd service migrated successfully. Restarting service to apply changes...")

		// Restart service asynchronously to load the new namespace
		cmdRestart := exec.Command("systemctl", "restart", "lgsm-dashboard")
		_ = cmdRestart.Start()

		// Exit immediately to allow systemd to restart us in the new mount namespace
		os.Exit(0)
	}
}

func main() {
	// CLI Flags
	portFlag := flag.Int("port", 0, "Port to run the dashboard on (overrides config)")
	configDirFlag := flag.String("config-dir", "./config", "Directory to store configuration files")
	flag.Parse()

	// Retroactive migration
	migrateSystemdService()

	// Initialize Auth Manager
	authMgr, err := backend.NewAuthManager(*configDirFlag)
	if err != nil {
		fmt.Printf("[FATAL] Failed to initialize Auth Manager: %v\n", err)
		os.Exit(1)
	}

	// Determine port: CLI flag overrides config
	port := *portFlag
	if port == 0 {
		port = authMgr.GetPort()
	}

	// Initialize Instance Manager
	instMgr := backend.NewInstanceManager()

	// Initialize System Metrics Collector
	metricsCollector := backend.NewSystemMetricsCollector()

	// Initialize Game Crawler and start background sync on startup
	gameCrawler := backend.NewGameCrawler(*configDirFlag)
	gameCrawler.StartSync()

	// Serve Static UI Assets from the embedded 'ui' directory
	subFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		fmt.Printf("[FATAL] Failed to create sub-filesystem: %v\n", err)
		os.Exit(1)
	}
	fileServer := http.FileServer(http.FS(subFS))
	http.Handle("/", fileServer)

	// -------------------------------------------------------------
	// API HTTP Handlers
	// -------------------------------------------------------------

	// POST /api/auth/login
	http.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var creds struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}

		err := json.NewDecoder(r.Body).Decode(&creds)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request payload"})
			return
		}

		sessionID, err := authMgr.Login(creds.Username, creds.Password)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid username or password"})
			return
		}

		// Set cookie
		http.SetCookie(w, &http.Cookie{
			Name:     "session_id",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400 * 7,
		})

		authMgr.LogAudit(creds.Username, r.RemoteAddr, "LOGIN", "User logged in successfully", "")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// POST /api/auth/logout
	http.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		cookie, err := r.Cookie("session_id")
		if err == nil {
			authMgr.Logout(cookie.Value)
		}

		// Clear cookie
		http.SetCookie(w, &http.Cookie{
			Name:     "session_id",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// GET /api/servers
	http.HandleFunc("/api/servers", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		servers := instMgr.GetInstances()
		if user.Role != "admin" {
			var filtered []backend.GameServerInstance
			for _, srv := range servers {
				if isUserAllowedServer(user, srv.ID) {
					filtered = append(filtered, srv)
				}
			}
			servers = filtered
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(servers)
	}))

	// GET /api/system/stats
	http.HandleFunc("/api/system/stats", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		servers := instMgr.GetInstances()
		if user.Role != "admin" {
			var filtered []backend.GameServerInstance
			for _, srv := range servers {
				if isUserAllowedServer(user, srv.ID) {
					filtered = append(filtered, srv)
				}
			}
			servers = filtered
		}

		stats, err := metricsCollector.GetStats(servers)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	}))

	// GET /api/system/info
	http.HandleFunc("/api/system/info", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		var userClean interface{}
		if err == nil {
			userClean = map[string]interface{}{
				"username":    user.Username,
				"role":        user.Role,
				"servers":     user.Servers,
				"permissions": user.Permissions,
			}
		}

		info := map[string]interface{}{
			"os":      fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH),
			"pid":     os.Getpid(),
			"version": Version,
			"user":    userClean,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	}))

	// GET /api/system/firewall
	http.HandleFunc("/api/system/firewall", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		status := backend.GetFirewallStatus()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}))

	// POST /api/system/firewall/open
	http.HandleFunc("/api/system/firewall/open", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden: Admin role required to modify firewall rules", http.StatusForbidden)
			return
		}

		var payload struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Port <= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid port number"})
			return
		}

		err = backend.OpenFirewallPort(payload.Port, payload.Protocol)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": fmt.Sprintf("Port %d/%s rule added successfully", payload.Port, payload.Protocol)})
	}))

	// POST /api/servers/bulk
	http.HandleFunc("/api/servers/bulk", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var payload struct {
			Action    string   `json:"action"`
			ServerIDs []string `json:"serverIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.ServerIDs) == 0 {
			http.Error(w, "Invalid bulk payload", http.StatusBadRequest)
			return
		}

		res := instMgr.RunBulkAction(payload.Action, payload.ServerIDs)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	}))

	// POST /api/settings/password
	http.HandleFunc("/api/settings/password", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var payload struct {
			OldPassword string `json:"oldPassword"`
			NewPassword string `json:"newPassword"`
		}

		err = json.NewDecoder(r.Body).Decode(&payload)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
			return
		}

		err = authMgr.ChangeUserPassword(user.Username, payload.OldPassword, payload.NewPassword)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))

	// GET /api/system/audit
	http.HandleFunc("/api/system/audit", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authMgr.GetAuditLogs())
	}))

	// GET /api/games/install
	http.HandleFunc("/api/games/install", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
			return
		}

		gameCmd := r.URL.Query().Get("game")
		username := r.URL.Query().Get("user")
		password := r.URL.Query().Get("password")
		lang := r.URL.Query().Get("lang")

		if gameCmd == "" || username == "" {
			http.Error(w, "Missing game or user parameter", http.StatusBadRequest)
			return
		}

		if !isValidGameCmd(gameCmd) || !isValidUsername(username) {
			http.Error(w, "Invalid game command or username characters", http.StatusBadRequest)
			return
		}

		instMgr.InstallGame(w, r, gameCmd, username, password, lang)
	}))

	// GET /api/games/list
	http.HandleFunc("/api/games/list", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		games := gameCrawler.GetGames()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(games)
	}))

	// GET /api/games/sync
	http.HandleFunc("/api/games/sync", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
			return
		}

		gameCrawler.StartSync()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "syncing"})
	}))

	// GET /api/games/sync/status
	http.HandleFunc("/api/games/sync/status", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
			return
		}

		status, progress, lastErr := gameCrawler.GetStatus()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":   status,
			"progress": progress,
			"error":    lastErr,
		})
	}))

	// GET /api/games/icon/{scriptName}
	http.HandleFunc("/api/games/icon/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 5 {
			http.NotFound(w, r)
			return
		}
		scriptName := parts[4]

		iconsDir := filepath.Join(*configDirFlag, "icons")
		files, err := os.ReadDir(iconsDir)
		var iconFile string
		if err == nil {
			for _, f := range files {
				nameWithoutExt := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))
				if nameWithoutExt == scriptName {
					iconFile = filepath.Join(iconsDir, f.Name())
					break
				}
			}
		}

		if iconFile != "" {
			ext := strings.ToLower(filepath.Ext(iconFile))
			contentType := "image/png"
			if ext == ".jpg" || ext == ".jpeg" {
				contentType = "image/jpeg"
			} else if ext == ".webp" {
				contentType = "image/webp"
			}
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, iconFile)
			return
		}

		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#7c4dff" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" style="width:100%;height:100%;"><rect x="2" y="2" width="20" height="20" rx="2" ry="2"></rect><path d="M6 12h12M12 6v12"></path></svg>`))
	})

	// User CRUD Endpoint
	http.HandleFunc("/api/admin/users", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
			return
		}

		if r.Method == http.MethodGet {
			users := authMgr.GetUsers()
			for i := range users {
				users[i].PasswordHash = "" // sanitise
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(users)
			return
		}

		if r.Method == http.MethodPost {
			var payload struct {
				Username    string   `json:"username"`
				Password    string   `json:"password"`
				Role        string   `json:"role"`
				Servers     []string `json:"servers"`
				Permissions []string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Username == "" || payload.Password == "" {
				http.Error(w, "Invalid payload", http.StatusBadRequest)
				return
			}
			if !isValidUsername(payload.Username) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "Invalid username format"})
				return
			}
			if payload.Role != "admin" && payload.Role != "user" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "Invalid role"})
				return
			}
			err := authMgr.CreateUser(payload.Username, payload.Password, payload.Role, payload.Servers, payload.Permissions)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		if r.Method == http.MethodPut {
			var payload struct {
				Username    string   `json:"username"`
				Password    string   `json:"password"`
				Role        string   `json:"role"`
				Servers     []string `json:"servers"`
				Permissions []string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Username == "" {
				http.Error(w, "Invalid payload", http.StatusBadRequest)
				return
			}
			if payload.Role != "admin" && payload.Role != "user" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "Invalid role"})
				return
			}
			err := authMgr.UpdateUser(payload.Username, payload.Password, payload.Role, payload.Servers, payload.Permissions)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		if r.Method == http.MethodDelete {
			username := r.URL.Query().Get("username")
			if username == "" {
				http.Error(w, "Missing username parameter", http.StatusBadRequest)
				return
			}
			err := authMgr.DeleteUser(username)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}))

	// Dashboard check updates
	http.HandleFunc("/api/admin/update/check", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
			return
		}

		latestTag, err := fetchLatestRelease()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		currentVer := strings.TrimPrefix(Version, "v")
		latestVer := strings.TrimPrefix(latestTag, "v")
		hasUpdate := latestVer != currentVer

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"current_version": Version,
			"latest_version":  latestTag,
			"has_update":      hasUpdate,
		})
	}))

	// Dashboard trigger updates
	http.HandleFunc("/api/admin/update/trigger", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user, err := getLoggedInUser(r, authMgr)
		if err != nil || user.Role != "admin" {
			http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.TagName == "" {
			http.Error(w, "Invalid payload", http.StatusBadRequest)
			return
		}

		err = triggerSelfUpdateByGit(payload.TagName)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Dashboard updated successfully. Restarting..."})

		go func() {
			time.Sleep(1 * time.Second)
			fmt.Println("[SYS] Exiting for restart after self-update...")
			os.Exit(0)
		}()
	}))

	// Dynamic routing for /api/servers/{id}/...
	http.HandleFunc("/api/servers/", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 4 {
			http.NotFound(w, r)
			return
		}

		user, err := getLoggedInUser(r, authMgr)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
			return
		}

		serverID := parts[3]
		if !isUserAllowedServer(user, serverID) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "Forbidden - Server access denied"})
			return
		}

		subRoute := ""
		if len(parts) >= 5 {
			subRoute = parts[4]
		}

		switch subRoute {
		case "delete":
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if user.Role != "admin" {
				http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
				return
			}
			instMgr.DeleteServer(w, r, serverID)

		case "portcheck":
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			instMgr.CheckServerPorts(w, r, serverID)

		case "action":
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			action := r.URL.Query().Get("action")
			if action == "" {
				http.Error(w, "Missing action query parameter", http.StatusBadRequest)
				return
			}
			if !isValidAction(action) {
				http.Error(w, "Invalid action name", http.StatusBadRequest)
				return
			}

			// Validate user permission scopes
			switch action {
			case "start":
				if !hasUserPermission(user, "start") {
					http.Error(w, "Forbidden - Missing start permission", http.StatusForbidden)
					return
				}
			case "stop":
				if !hasUserPermission(user, "stop") {
					http.Error(w, "Forbidden - Missing stop permission", http.StatusForbidden)
					return
				}
			case "restart":
				if !hasUserPermission(user, "restart") {
					http.Error(w, "Forbidden - Missing restart permission", http.StatusForbidden)
					return
				}
			case "update", "validate", "update-lgsm", "force-update", "test-alert", "map-wipe", "full-wipe", "change-password", "check-update", "mods-install", "mods-update", "mods-remove", "fastdl", "map-compressor", "skeleton", "debug":
				if user.Role != "admin" {
					http.Error(w, "Forbidden - Administrator action", http.StatusForbidden)
					return
				}
			case "backup", "details", "postdetails":
				if !hasUserPermission(user, "backup") && user.Role != "admin" {
					http.Error(w, "Forbidden - Missing permission", http.StatusForbidden)
					return
				}
			}

			authMgr.LogAudit(user.Username, r.RemoteAddr, "ACTION_"+strings.ToUpper(action), fmt.Sprintf("Executed action %s", action), serverID)
			instMgr.RunAction(w, r, serverID, action, r.URL.Query().Get("lang"))

		case "console":
			if !hasUserPermission(user, "console") {
				http.Error(w, "Forbidden - Missing console permission", http.StatusForbidden)
				return
			}

			if len(parts) >= 6 && parts[5] == "send" {
				if r.Method != http.MethodPost {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}

				var payload struct {
					Command string `json:"command"`
				}
				err := json.NewDecoder(r.Body).Decode(&payload)
				if err != nil || payload.Command == "" {
					http.Error(w, "Invalid command payload", http.StatusBadRequest)
					return
				}

				err = instMgr.SendConsoleCommand(serverID, payload.Command)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}

			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			mode := r.URL.Query().Get("mode")
			if mode == "" {
				mode = "tmux"
			}
			lines, err := instMgr.GetConsole(serverID, mode, r.URL.Query().Get("lang"))
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"lines": lines})

		case "configs":
			if !hasUserPermission(user, "config") {
				http.Error(w, "Forbidden - Missing config permission", http.StatusForbidden)
				return
			}

			if len(parts) == 5 {
				if r.Method != http.MethodGet {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				configs, err := instMgr.GetConfigFiles(serverID)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(configs)
				return
			}

			if len(parts) >= 6 && parts[5] == "file" {
				if r.Method == http.MethodGet {
					filePath := r.URL.Query().Get("path")
					if filePath == "" {
						http.Error(w, "Missing path query parameter", http.StatusBadRequest)
						return
					}
					content, err := instMgr.GetConfigFileContent(serverID, filePath)
					if err != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
						return
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]string{"content": content})

				} else if r.Method == http.MethodPost {
					var payload struct {
						Path    string `json:"path"`
						Content string `json:"content"`
					}
					err := json.NewDecoder(r.Body).Decode(&payload)
					if err != nil || payload.Path == "" {
						http.Error(w, "Invalid payload", http.StatusBadRequest)
						return
					}

					err = instMgr.SaveConfigFileContent(serverID, payload.Path, payload.Content)
					if err != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
						return
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
				return
			}
			http.NotFound(w, r)

		case "tag":
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload struct {
				Tag string `json:"tag"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "Invalid payload", http.StatusBadRequest)
				return
			}
			err := instMgr.SetServerTag(serverID, payload.Tag)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		case "cron":
			if len(parts) >= 6 && parts[5] == "install" && r.Method == http.MethodPost {
				if user.Role != "admin" {
					http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
					return
				}
				err := instMgr.InstallCronjobs(serverID)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}

			if r.Method == http.MethodGet {
				sched, err := instMgr.GetCronSchedule(serverID)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(sched)
				return
			} else if r.Method == http.MethodPost {
				var sched backend.CronSchedule
				if err := json.NewDecoder(r.Body).Decode(&sched); err != nil {
					http.Error(w, "Invalid payload", http.StatusBadRequest)
					return
				}
				err := instMgr.SaveCronSchedule(serverID, sched)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}

		case "rcon":
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload struct {
				Command string `json:"command"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Command == "" {
				http.Error(w, "Invalid RCON command", http.StatusBadRequest)
				return
			}
			err := instMgr.SendConsoleCommand(serverID, payload.Command)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		case "backups":
			if !hasUserPermission(user, "backup") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "Forbidden - Missing backup permission"})
				return
			}

			actionType := ""
			if len(parts) >= 6 {
				actionType = parts[5]
			}

			switch actionType {
			case "": // GET /api/servers/{id}/backups -> list backups
				if r.Method != http.MethodGet {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				backups, err := instMgr.ListBackups(serverID)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(backups)
				return

			case "delete": // POST /api/servers/{id}/backups/delete?file=xxx
				if r.Method != http.MethodPost {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				fileName := r.URL.Query().Get("file")
				if fileName == "" {
					http.Error(w, "Missing file parameter", http.StatusBadRequest)
					return
				}
				err := instMgr.DeleteBackup(serverID, fileName)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return

			case "upload": // POST /api/servers/{id}/backups/upload
				if r.Method != http.MethodPost {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				// Allow large uploads up to 32MB in memory, rest goes to disk automatically
				err := r.ParseMultipartForm(32 << 20)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse multipart form: " + err.Error()})
					return
				}
				file, header, err := r.FormFile("backup")
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]string{"error": "Missing backup file parameter: " + err.Error()})
					return
				}
				defer file.Close()

				err = instMgr.UploadBackup(serverID, header.Filename, file)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return

			case "download": // GET /api/servers/{id}/backups/download?file=xxx
				if r.Method != http.MethodGet {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				fileName := r.URL.Query().Get("file")
				if fileName == "" {
					http.Error(w, "Missing file parameter", http.StatusBadRequest)
					return
				}
				filePath, err := instMgr.GetBackupPath(serverID, fileName)
				if err != nil {
					http.Error(w, err.Error(), http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
				http.ServeFile(w, r, filePath)
				return

			case "restore": // GET /api/servers/{id}/backups/restore?file=xxx -> SSE Stream
				if r.Method != http.MethodGet {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				fileName := r.URL.Query().Get("file")
				if fileName == "" {
					http.Error(w, "Missing file parameter", http.StatusBadRequest)
					return
				}
				instMgr.RestoreBackup(w, r, serverID, fileName, r.URL.Query().Get("lang"))
				return

			case "settings":
				if r.Method == http.MethodGet {
					settings, err := instMgr.GetBackupSettings(serverID)
					if err != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
						return
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(settings)
					return
				} else if r.Method == http.MethodPost {
					var payload backend.BackupSettings
					err := json.NewDecoder(r.Body).Decode(&payload)
					if err != nil {
						http.Error(w, "Invalid payload", http.StatusBadRequest)
						return
					}
					err = instMgr.SaveBackupSettings(serverID, payload)
					if err != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
						return
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
					return
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}

			default:
				http.NotFound(w, r)
				return
			}

		case "alerts":
			if r.Method == http.MethodGet {
				settings, err := instMgr.GetAlertSettings(serverID)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(settings)
				return
			} else if r.Method == http.MethodPost {
				var payload backend.AlertSettings
				err := json.NewDecoder(r.Body).Decode(&payload)
				if err != nil {
					http.Error(w, "Invalid payload", http.StatusBadRequest)
					return
				}
				err = instMgr.SaveAlertSettings(serverID, payload)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

		case "systemd":
			if len(parts) >= 6 && parts[5] == "install" && r.Method == http.MethodPost {
				if user.Role != "admin" {
					http.Error(w, "Forbidden - Admins only", http.StatusForbidden)
					return
				}
				err := instMgr.InstallSystemdService(serverID)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}
			http.NotFound(w, r)
			return

		case "files":
			if !hasUserPermission(user, "files") && !hasUserPermission(user, "config") {
				http.Error(w, "Forbidden - Missing files permission", http.StatusForbidden)
				return
			}

			if r.Method == http.MethodGet {
				if len(parts) >= 6 && parts[5] == "download" {
					relPath := r.URL.Query().Get("path")
					if relPath == "" {
						http.Error(w, "Missing file path", http.StatusBadRequest)
						return
					}
					content, err := instMgr.GetServerFileContent(serverID, relPath)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					filename := filepath.Base(relPath)
					w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Write([]byte(content))
					return
				}
				if len(parts) >= 6 && parts[5] == "content" {
					relPath := r.URL.Query().Get("path")
					if relPath == "" {
						http.Error(w, "Missing file path", http.StatusBadRequest)
						return
					}
					content, err := instMgr.GetServerFileContent(serverID, relPath)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.Write([]byte(content))
					return
				}
				subPath := r.URL.Query().Get("path")
				nodes, err := instMgr.GetServerFiles(serverID, subPath)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(nodes)
				return
			} else if r.Method == http.MethodPost {
				if user.Role != "admin" && !hasUserPermission(user, "config") {
					http.Error(w, "Forbidden - Missing permission", http.StatusForbidden)
					return
				}
				if len(parts) >= 6 && parts[5] == "upload" {
					file, header, err := r.FormFile("file")
					if err != nil {
						http.Error(w, "Failed to read uploaded file", http.StatusBadRequest)
						return
					}
					defer file.Close()

					fileBytes, err := io.ReadAll(file)
					if err != nil {
						http.Error(w, "Failed to read file bytes", http.StatusInternalServerError)
						return
					}

					subPath := r.FormValue("path")
					err = instMgr.UploadServerFile(serverID, subPath, header.Filename, fileBytes)
					if err != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
						return
					}

					authMgr.LogAudit(user.Username, r.RemoteAddr, "FILE_UPLOAD", fmt.Sprintf("Uploaded file %s to %s", header.Filename, subPath), serverID)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
					return
				}

				var payload struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Path == "" {
					http.Error(w, "Invalid payload", http.StatusBadRequest)
					return
				}
				err := instMgr.SaveServerFileContent(serverID, payload.Path, payload.Content)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				authMgr.LogAudit(user.Username, r.RemoteAddr, "FILE_EDIT", fmt.Sprintf("Saved file %s", payload.Path), serverID)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}

		case "mods":
			if r.Method == http.MethodGet {
				installedMods := []string{}
				srv := instMgr.GetInstances()
				var targetSrv *backend.GameServerInstance
				for _, s := range srv {
					if s.ID == serverID {
						targetSrv = &s
						break
					}
				}
				if targetSrv != nil {
					modsFile := filepath.Join("/home", targetSrv.User, "lgsm", "mods", "installed-mods.txt")
					if file, err := os.Open(modsFile); err == nil {
						scanner := bufio.NewScanner(file)
						for scanner.Scan() {
							line := strings.TrimSpace(scanner.Text())
							if line != "" && !strings.HasPrefix(line, "#") {
								installedMods = append(installedMods, line)
							}
						}
						file.Close()
					}
				}

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":    "ok",
					"installed": installedMods,
				})
				return
			}



		default:
			http.NotFound(w, r)
		}
	}))

	// Start server
	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("[SYS] LinuxGSM Web Dashboard running on http://localhost%s\n", addr)
	fmt.Println("[SYS] Production mode enabled. Live LinuxGSM instances and CPU/RAM process tracking are active.")

	err = http.ListenAndServe(addr, nil)
	if err != nil && err != http.ErrServerClosed {
		fmt.Printf("[FATAL] Server failed: %v\n", err)
		os.Exit(1)
	}
}
