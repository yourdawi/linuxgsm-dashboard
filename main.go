package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
var Version = "1.0.0"

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
	case "start", "stop", "restart", "update", "details", "backup", "validate":
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

func main() {
	// CLI Flags
	portFlag := flag.Int("port", 0, "Port to run the dashboard on (overrides config)")
	mockFlag := flag.Bool("mock", false, "Force mock mode (automatically true on Windows)")
	configDirFlag := flag.String("config-dir", "./config", "Directory to store configuration files")
	flag.Parse()

	// Detect OS and warn if running on Windows without mock mode
	isMock := *mockFlag
	if runtime.GOOS == "windows" {
		if !isMock {
			fmt.Println("[WARNING] Application running on Windows with mock mode disabled. LinuxGSM commands will fail.")
		} else {
			fmt.Println("[SYS] Mock mode active (Windows test mode).")
		}
	}

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
	instMgr := backend.NewInstanceManager(isMock)

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
			Expires:  time.Now().Add(24 * time.Hour),
		})

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

		stats, err := metricsCollector.GetStats(servers, isMock)
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
			"mock":    isMock,
			"version": Version,
			"user":    userClean,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
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
			case "update", "validate":
				if user.Role != "admin" {
					http.Error(w, "Forbidden - Administrator action", http.StatusForbidden)
					return
				}
			case "backup", "details":
				if !hasUserPermission(user, "backup") {
					http.Error(w, "Forbidden - Missing backup permission", http.StatusForbidden)
					return
				}
			}

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

		default:
			http.NotFound(w, r)
		}
	}))

	// Start server
	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("[SYS] LinuxGSM Web Dashboard running on http://localhost%s\n", addr)
	if isMock {
		fmt.Println("[SYS] Local test mode (Mock Mode) enabled. Game commands and resource values will be simulated.")
	} else {
		fmt.Println("[SYS] Production mode enabled. Live LinuxGSM instances and CPU/RAM process tracking are active.")
	}

	err = http.ListenAndServe(addr, nil)
	if err != nil && err != http.ErrServerClosed {
		fmt.Printf("[FATAL] Server failed: %v\n", err)
		os.Exit(1)
	}
}
