package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yourdawi/linuxgsm-dashboard/backend"
)

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

		servers := instMgr.GetInstances()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(servers)
	}))

	// GET /api/system/stats
	http.HandleFunc("/api/system/stats", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		servers := instMgr.GetInstances()
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

		info := map[string]interface{}{
			"os":   fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH),
			"pid":  os.Getpid(),
			"mock": isMock,
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

		var payload struct {
			OldPassword string `json:"oldPassword"`
			NewPassword string `json:"newPassword"`
		}

		err := json.NewDecoder(r.Body).Decode(&payload)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
			return
		}

		err = authMgr.ChangePassword(payload.OldPassword, payload.NewPassword)
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

	// Dynamic routing for /api/servers/{id}/...
	http.HandleFunc("/api/servers/", authMgr.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Path format: /api/servers/{id}/{subroute}
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 4 {
			http.NotFound(w, r)
			return
		}

		serverID := parts[3]
		subRoute := ""
		if len(parts) >= 5 {
			subRoute = parts[4]
		}

		switch subRoute {
		case "delete":
			// POST /api/servers/{id}/delete
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			instMgr.DeleteServer(w, r, serverID)

		case "portcheck":
			// GET /api/servers/{id}/portcheck
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			instMgr.CheckServerPorts(w, r, serverID)

		case "action":
			// GET /api/servers/{id}/action?action=start|stop|restart|update
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
			instMgr.RunAction(w, r, serverID, action, r.URL.Query().Get("lang"))

		case "console":
			// POST /api/servers/{id}/console/send
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

			// GET /api/servers/{id}/console?mode=tmux|log
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
			// GET /api/servers/{id}/configs
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

			// Subroute: /api/servers/{id}/configs/file
			if len(parts) >= 6 && parts[5] == "file" {
				if r.Method == http.MethodGet {
					// Read config file contents
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
					// Save config file contents
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
