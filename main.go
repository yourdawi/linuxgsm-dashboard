package main

// NOTE: all package main code lives in this single file on purpose. Dashboard versions up to
// 1.0.7 self-update with `go build main.go`, which only compiles this file.

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/yourdawi/linuxgsm-dashboard/backend"
)

// Version is injected by the release workflow (-ldflags "-X main.Version=1.2.3").
// Builds from source report "dev".
var Version = "dev"

//go:embed ui/*
var uiFS embed.FS

// releaseRepo is the only repository the dashboard installs updates from.
const releaseRepo = "yourdawi/linuxgsm-dashboard"

// releasePublicKey verifies the signature of release checksums (base64 Ed25519 public key,
// created with `go run ./tools/releasekey generate`). Self-updates are disabled while it is empty.
const releasePublicKey = "ySlp/QcA19j/qQXcyHmC+Ki1NvV7XSEYNl7z2z9c7P8="

// updateCheckInterval controls how often the dashboard looks for new releases.
const updateCheckInterval = 6 * time.Hour

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

		if err := backend.WriteFileAtomic(servicePath, []byte(newContent), 0644); err != nil {
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
	// `lgsm-dashboard agent ...` runs the agent of a node instead of the dashboard.
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		os.Exit(runAgentCommand(os.Args[2:]))
	}

	// CLI Flags
	portFlag := flag.Int("port", 0, "Port to run the dashboard on (overrides config)")
	agentPortFlag := flag.Int("agent-port", 0, "Port for direct agent connections (0: agents connect via WebSocket on the web port only)")
	configDirFlag := flag.String("config-dir", "./config", "Directory to store configuration files")
	secureCookiesFlag := flag.Bool("secure-cookies", false, "Always set the Secure flag on cookies (enable when the dashboard is only reachable via HTTPS)")
	trustProxyFlag := flag.Bool("trust-proxy", false, "Trust X-Forwarded-For/X-Forwarded-Proto headers from a reverse proxy")
	maxUploadMBFlag := flag.Int64("max-upload-mb", 4096, "Maximum size of backup and file manager uploads in MiB")
	maxArchiveGBFlag := flag.Int64("max-archive-gb", 1024, "Maximum uncompressed size of a backup archive that may be restored in GiB")
	versionFlag := flag.Bool("version", false, "Print the version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(Version)
		return
	}

	// Runtime that controls the game servers (LGSM_RUNTIME, default: host)
	rt, err := backend.SelectRuntime(*configDirFlag)
	if err != nil {
		fmt.Printf("[FATAL] %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[SYS] Runtime: %s\n", rt.Name())

	caps := rt.Capabilities()

	// Retroactive migration of the dashboard's own systemd unit
	if caps.SelfUpdate {
		migrateSystemdService()
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

	if *maxUploadMBFlag <= 0 {
		*maxUploadMBFlag = 4096
	}
	if *maxArchiveGBFlag > 0 {
		backend.DefaultArchiveLimits.MaxTotalSize = *maxArchiveGBFlag << 30
	}

	// Initialize Game Crawler and start background sync on startup
	gameCrawler := backend.NewGameCrawler(*configDirFlag)
	gameCrawler.StartSync()

	// Serve Static UI Assets from the embedded 'ui' directory
	subFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		fmt.Printf("[FATAL] Failed to create sub-filesystem: %v\n", err)
		os.Exit(1)
	}

	// In-place updates replace the binary; a container is updated by pulling a new image.
	var updater *backend.Updater
	if caps.SelfUpdate {
		updater = &backend.Updater{
			Repo:           releaseRepo,
			PublicKey:      releasePublicKey,
			CurrentVersion: Version,
		}
		updater.StartBackgroundCheck(updateCheckInterval)
	}

	instances := backend.NewInstanceManager(*configDirFlag, rt)
	metrics := backend.NewSystemMetricsCollector()
	metrics.ProcessStats = instances.ProcessStats

	agents, err := backend.NewAgentController(*configDirFlag, Version, func(format string, args ...interface{}) {
		fmt.Printf(format+"\n", args...)
	})
	if err != nil {
		fmt.Printf("[FATAL] Failed to load nodes: %v\n", err)
		os.Exit(1)
	}
	if *agentPortFlag > 0 {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", *agentPortFlag))
		if err != nil {
			fmt.Printf("[FATAL] Agent port: %v\n", err)
			os.Exit(1)
		}
		go func() {
			if err := agents.ServeDirect(l); err != nil {
				fmt.Printf("[AGENT] Direct agent port stopped: %v\n", err)
			}
		}()
		fmt.Printf("[SYS] Accepting direct agent connections on port %d\n", *agentPortFlag)
	}

	application := &app{
		agents:         agents,
		agentPort:      *agentPortFlag,
		auth:           authMgr,
		inst:           instances,
		metrics:        metrics,
		crawler:        gameCrawler,
		limiter:        backend.NewLoginLimiter(),
		updater:        updater,
		configDir:      *configDirFlag,
		uiFS:           subFS,
		secureCookies:  *secureCookiesFlag,
		trustProxy:     *trustProxyFlag,
		maxUploadBytes: *maxUploadMBFlag << 20,
	}

	// Per-request read/write deadlines are applied by middleware so long running streams (SSE,
	// downloads, uploads) can lift them selectively.
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           application.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	fmt.Printf("[SYS] LinuxGSM Web Dashboard running on http://localhost%s\n", server.Addr)
	fmt.Println("[SYS] Production mode enabled. Live LinuxGSM instances and CPU/RAM process tracking are active.")

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Printf("[FATAL] Server failed: %v\n", err)
		os.Exit(1)
	}
}

// Request body limits.
const (
	maxJSONBody      = 1 << 20  // 1 MiB for regular API requests
	maxEditorBody    = 12 << 20 // config/file editor saves (5 MiB content + JSON escaping)
	defaultReadLimit = 60 * time.Second
	defaultWriteTime = 120 * time.Second
)

// app bundles the dependencies of the HTTP handlers.
type app struct {
	auth      *backend.AuthManager
	inst      *backend.InstanceManager
	metrics   *backend.SystemMetricsCollector
	crawler   *backend.GameCrawler
	limiter   *backend.LoginLimiter
	updater   *backend.Updater
	configDir string
	uiFS      fs.FS

	agents    *backend.AgentController // nodes (multi-node)
	agentPort int                      // direct agent port, 0 when disabled

	// Agent mode: the app serves a controller through the tunnel instead of users.
	agentMode   bool
	agentPolicy atomic.Pointer[backend.AgentPolicy]
	fastdlURL   string // agent: base URL of the node's own FastDL (agent run --fastdl-url)

	secureCookies  bool  // always set the Secure flag on cookies
	trustProxy     bool  // honour X-Forwarded-For / X-Forwarded-Proto from a reverse proxy
	maxUploadBytes int64 // upload limit for backups and file manager uploads

	nodeRequests nodeRequestLimiter // concurrent forwarded requests per user and node
}

// -------------------------------------------------------------
// Validation helpers
// -------------------------------------------------------------

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
	if !strings.HasSuffix(c, "server") || len(c) < 7 || len(c) > 64 {
		return false
	}
	for _, r := range c {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// -------------------------------------------------------------
// Response helpers
// -------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// decodeJSON decodes a size limited JSON body. It writes the error response itself.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst interface{}, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "Request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "Invalid request payload")
		}
		return false
	}
	return true
}

func writeAuthzError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backend.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "Unauthorized")
	case errors.Is(err, backend.ErrServerNotFound):
		writeError(w, http.StatusNotFound, "Server not found")
	default:
		writeError(w, http.StatusForbidden, "Forbidden - missing permission")
	}
}

// clientIP returns the address used for rate limiting and audit logs.
func (a *app) clientIP(r *http.Request) string {
	if a.agentMode {
		if ip := net.ParseIP(r.Header.Get(backend.AgentActorIPHeader)); ip != nil {
			return ip.String()
		}
		return "controller"
	}
	if a.trustProxy {
		// The rightmost entry is the address the trusted proxy saw; anything left of it was sent
		// by the client (nginx appends to the header it received).
		if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
			parts := strings.Split(values[len(values)-1], ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return net.ParseIP(last).String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *app) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return a.trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// currentUser resolves the logged in user from the session cookie. On an agent every request
// comes from the authenticated controller through the tunnel; the user it acts for is only
// recorded for the audit log, the controller has already checked that user's permissions.
func (a *app) currentUser(r *http.Request) (backend.User, string, error) {
	if a.agentMode {
		actor := r.Header.Get(backend.AgentActorHeader)
		if !isValidUsername(actor) {
			actor = "unknown"
		}
		return backend.User{Username: "controller:" + actor, Role: backend.RoleAdmin}, "", nil
	}
	cookie, err := r.Cookie(backend.SessionCookieName)
	if err != nil {
		return backend.User{}, "", backend.ErrUnauthenticated
	}
	session, ok := a.auth.GetSession(cookie.Value)
	if !ok {
		return backend.User{}, "", backend.ErrUnauthenticated
	}
	user, ok := a.auth.GetUser(session.Username)
	if !ok {
		return backend.User{}, "", backend.ErrUnauthenticated
	}
	return user, cookie.Value, nil
}

// -------------------------------------------------------------
// Middleware
// -------------------------------------------------------------

// contentSecurityPolicy is compatible with the existing frontend, which uses inline event
// handlers and styles plus Google Fonts.
const contentSecurityPolicy = "default-src 'self'; script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' data: https://fonts.gstatic.com; " +
	"img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// sandboxCSP is used for user controlled downloads (FastDL, file manager, backups) so such
// content can never run scripts in the dashboard origin.
const sandboxCSP = "sandbox; default-src 'none'"

func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/fastdl/") {
			h.Set("Content-Security-Policy", sandboxCSP)
		} else {
			h.Set("Content-Security-Policy", contentSecurityPolicy)
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		} else if !strings.HasPrefix(r.URL.Path, "/fastdl/") {
			// The embedded UI must be revalidated, otherwise browsers combine a cached app.js with
			// a newer backend after an update.
			h.Set("Cache-Control", "no-cache")
		}
		if a.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// deadlines applies default per-request read/write deadlines. Streaming endpoints lift them
// explicitly (backend.DisableWriteDeadline / DisableReadDeadline).
func deadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.SetRequestDeadlines(w, defaultReadLimit, defaultWriteTime)
		next.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// sameOrigin validates Origin (or Referer) against the Host header when the browser sent one.
func sameOrigin(r *http.Request) bool {
	check := func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin == "null" {
			return false
		}
		return check(origin)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return check(ref)
	}
	return true
}

// csrfProtect enforces Origin checks and a per-session double-submit token on every
// state-changing API request. SameSite cookies are only an additional layer.
func (a *app) csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUnsafeMethod(r.Method) || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "Forbidden - cross-origin request")
			return
		}
		if r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/login/2fa" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(backend.SessionCookieName)
		if err != nil {
			// Unauthenticated: the handler answers 401.
			next.ServeHTTP(w, r)
			return
		}
		if !a.auth.ValidateCSRF(cookie.Value, r.Header.Get("X-CSRF-Token")) {
			if !a.auth.IsValidSession(cookie.Value) {
				next.ServeHTTP(w, r) // expired session -> 401 from handler
				return
			}
			writeError(w, http.StatusForbidden, "Forbidden - invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth wraps handlers that need a logged in user.
func (a *app) requireAuth(next func(http.ResponseWriter, *http.Request, backend.User, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, sessionID, err := a.currentUser(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		next(w, r, user, sessionID)
	}
}

// requireAdmin wraps handlers restricted to administrators.
func (a *app) requireAdmin(next func(http.ResponseWriter, *http.Request, backend.User, string)) http.HandlerFunc {
	return a.requireAuth(func(w http.ResponseWriter, r *http.Request, user backend.User, sid string) {
		if !user.IsAdmin() {
			writeError(w, http.StatusForbidden, "Forbidden - Admins only")
			return
		}
		next(w, r, user, sid)
	})
}

func methodOnly(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// -------------------------------------------------------------
// Router
// -------------------------------------------------------------

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	if a.uiFS != nil {
		mux.Handle("/", http.FileServer(http.FS(a.uiFS)))
	}

	mux.HandleFunc("/api/auth/login", methodOnly(http.MethodPost, a.handleLogin))
	mux.HandleFunc("/api/auth/login/2fa", methodOnly(http.MethodPost, a.handleLoginSecondFactor))
	mux.HandleFunc("/api/account/2fa", methodOnly(http.MethodGet, a.requireAuth(a.handleTwoFactorStatus)))
	mux.HandleFunc("/api/account/2fa/setup", methodOnly(http.MethodPost, a.requireAuth(a.handleTwoFactorSetup)))
	mux.HandleFunc("/api/account/2fa/enable", methodOnly(http.MethodPost, a.requireAuth(a.handleTwoFactorEnable)))
	mux.HandleFunc("/api/account/2fa/disable", methodOnly(http.MethodPost, a.requireAuth(a.handleTwoFactorDisable)))
	mux.HandleFunc("/api/account/2fa/recovery-codes", methodOnly(http.MethodPost, a.requireAuth(a.handleTwoFactorRecoveryCodes)))
	mux.HandleFunc("/api/admin/users/2fa-reset", methodOnly(http.MethodPost, a.requireAdmin(a.handleTwoFactorReset)))
	mux.HandleFunc("/api/auth/logout", methodOnly(http.MethodPost, a.handleLogout))

	mux.HandleFunc("/api/servers", methodOnly(http.MethodGet, a.requireAuth(a.handleListServers)))
	mux.HandleFunc("/api/servers/bulk", methodOnly(http.MethodPost, a.requireAuth(a.handleBulk)))
	mux.HandleFunc("/api/servers/", a.requireAuth(a.handleServer))

	mux.HandleFunc("/api/system/stats", methodOnly(http.MethodGet, a.requireAuth(a.handleStats)))
	mux.HandleFunc("/api/system/info", methodOnly(http.MethodGet, a.requireAuth(a.handleInfo)))
	mux.HandleFunc("/api/system/firewall", methodOnly(http.MethodGet, a.requireAdmin(a.handleFirewallStatus)))
	mux.HandleFunc("/api/system/firewall/open", methodOnly(http.MethodPost, a.requireAdmin(a.handleFirewallOpen)))
	mux.HandleFunc("/api/system/audit", methodOnly(http.MethodGet, a.requireAdmin(a.handleAudit)))
	mux.HandleFunc("/api/settings/password", methodOnly(http.MethodPost, a.requireAuth(a.handleChangePassword)))

	mux.HandleFunc("/api/games/install", methodOnly(http.MethodPost, a.requireAdmin(a.handleInstall)))
	mux.HandleFunc("/api/games/list", methodOnly(http.MethodGet, a.requireAuth(a.handleGamesList)))
	mux.HandleFunc("/api/games/sync", methodOnly(http.MethodPost, a.requireAdmin(a.handleGamesSync)))
	mux.HandleFunc("/api/games/sync/status", methodOnly(http.MethodGet, a.requireAdmin(a.handleGamesSyncStatus)))
	mux.HandleFunc("/api/games/icon/", a.handleGameIcon)

	mux.HandleFunc("/api/admin/users", a.requireAdmin(a.handleUsers))
	mux.HandleFunc("/api/admin/server-paths", a.requireAdmin(a.handleServerPaths))
	mux.HandleFunc("/api/admin/timezone", a.requireAdmin(a.handleTimeZone))
	mux.HandleFunc("/api/admin/update/check", methodOnly(http.MethodGet, a.requireAdmin(a.handleUpdateCheck)))
	mux.HandleFunc("/api/admin/update/trigger", methodOnly(http.MethodPost, a.requireAdmin(a.handleUpdateTrigger)))

	mux.HandleFunc("/api/admin/nodes", a.requireAdmin(a.handleNodes))
	mux.HandleFunc("/api/admin/nodes/update", methodOnly(http.MethodPost, a.requireAdmin(a.handleNodeUpdate)))
	mux.HandleFunc("/api/admin/nodes/delete", methodOnly(http.MethodPost, a.requireAdmin(a.handleNodeDelete)))
	mux.HandleFunc("/api/admin/nodes/upgrade", methodOnly(http.MethodPost, a.requireAdmin(a.handleNodeUpgrade)))
	// Agents authenticate inside the connection (mutual TLS), not with a session.
	mux.HandleFunc(backend.AgentConnectPath, methodOnly(http.MethodGet, a.handleAgentConnect))

	mux.HandleFunc("/fastdl/", a.handleFastDL)

	return a.securityHeaders(deadlines(a.csrfProtect(mux)))
}

// -------------------------------------------------------------
// Auth handlers
// -------------------------------------------------------------

func (a *app) setSessionCookies(w http.ResponseWriter, r *http.Request, sessionID, csrfToken string, maxAge int) {
	secure := a.secureCookies || a.isHTTPS(r)
	http.SetCookie(w, &http.Cookie{
		Name:     backend.SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
	// Readable by the frontend so it can echo the value in the X-CSRF-Token header.
	http.SetCookie(w, &http.Cookie{
		Name:     backend.CSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &creds, 16*1024) {
		return
	}
	if creds.Username == "" || len(creds.Username) > 64 || len(creds.Password) > 1024 {
		writeError(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}

	ip := a.clientIP(r)
	keys := []string{"ip:" + ip, "user:" + strings.ToLower(creds.Username)}
	ok, wait, done := a.limiter.Reserve(keys...)
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Too many failed login attempts. Please try again later.")
		return
	}

	result, err := a.auth.BeginLogin(creds.Username, creds.Password)
	done(err == nil)
	if err != nil {
		a.auth.LogAudit(creds.Username, ip, "LOGIN_FAILED", "Invalid username or password", "")
		writeError(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}

	if result.Challenge != "" {
		// Password correct, the account uses 2FA: the browser gets a short-lived challenge that is
		// bound to this account, no session yet.
		writeJSON(w, http.StatusOK, map[string]string{"status": "2fa_required", "challenge": result.Challenge})
		return
	}
	a.finishLogin(w, r, creds.Username, result.SessionID, "User logged in successfully")
}

// finishLogin sets the session cookies after a successful login.
func (a *app) finishLogin(w http.ResponseWriter, r *http.Request, username, sessionID, auditDetails string) {
	// Session fixation: discard any session the browser presented before logging in.
	if old, err := r.Cookie(backend.SessionCookieName); err == nil && old.Value != sessionID {
		a.auth.Logout(old.Value)
	}

	session, _ := a.auth.GetSession(sessionID)
	a.setSessionCookies(w, r, sessionID, session.CSRFToken, int(backend.SessionMaxLifetime.Seconds()))

	a.auth.LogAudit(username, a.clientIP(r), "LOGIN", auditDetails, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "csrf_token": session.CSRFToken})
}

// handleLoginSecondFactor completes a login with a TOTP or recovery code. The request contains no
// username: the challenge determines the account, so codes are only checked against that account.
func (a *app) handleLoginSecondFactor(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	username, ok := a.auth.ChallengeUser(payload.Challenge)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Login expired, please sign in again")
		return
	}
	ip := a.clientIP(r)
	keys := []string{"ip:" + ip, "2fa:" + strings.ToLower(username)}
	ok, wait, done := a.limiter.Reserve(keys...)
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Please try again later.")
		return
	}
	sessionID, usedRecovery, err := a.auth.CompleteLogin(payload.Challenge, payload.Code)
	done(err == nil)
	if err != nil {
		a.auth.LogAudit(username, ip, "LOGIN_2FA_FAILED", "Invalid two-factor code", "")
		if errors.Is(err, backend.ErrChallengeExpired) {
			writeError(w, http.StatusUnauthorized, "Login expired, please sign in again")
			return
		}
		writeError(w, http.StatusUnauthorized, "Invalid code")
		return
	}
	details := "User logged in with two-factor authentication"
	if usedRecovery {
		details = "User logged in with a recovery code"
	}
	a.finishLogin(w, r, username, sessionID, details)
}

// -------------------------------------------------------------
// Two-factor authentication (own account)
// -------------------------------------------------------------

func (a *app) handleTwoFactorStatus(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	writeJSON(w, http.StatusOK, a.auth.TwoFactorStatusFor(user.Username))
}

func requestHostName(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// limitReverify guards the password and code checks of a logged-in user (2FA setup, password
// change) with the login limiter, so a stolen session cannot be used to guess the password.
func (a *app) limitReverify(w http.ResponseWriter, username string) (func(bool), bool) {
	ok, wait, done := a.limiter.Reserve("user:" + strings.ToLower(username))
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Please try again later.")
		return nil, false
	}
	return done, true
}

func (a *app) handleTwoFactorSetup(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	var payload struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	done, ok := a.limitReverify(w, user.Username)
	if !ok {
		return
	}
	setup, err := a.auth.StartTwoFactorSetup(user.Username, payload.Password, requestHostName(r))
	done(err == nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	png, err := qrcode.Encode(setup.URI, qrcode.Medium, 240)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create QR code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": setup.Secret,
		"uri":    setup.URI,
		"qr":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

func (a *app) handleTwoFactorEnable(w http.ResponseWriter, r *http.Request, user backend.User, sessionID string) {
	var payload struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	codes, err := a.auth.ConfirmTwoFactorSetup(user.Username, payload.Code, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.auth.LogAudit(user.Username, a.clientIP(r), "2FA_ENABLED", "Enabled two-factor authentication", "")
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "recovery_codes": codes})
}

func (a *app) handleTwoFactorDisable(w http.ResponseWriter, r *http.Request, user backend.User, sessionID string) {
	var payload struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	done, ok := a.limitReverify(w, user.Username)
	if !ok {
		return
	}
	err := a.auth.DisableTwoFactor(user.Username, payload.Password, payload.Code, sessionID)
	done(err == nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.auth.LogAudit(user.Username, a.clientIP(r), "2FA_DISABLED", "Disabled two-factor authentication", "")
	writeOK(w)
}

func (a *app) handleTwoFactorRecoveryCodes(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	var payload struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	done, ok := a.limitReverify(w, user.Username)
	if !ok {
		return
	}
	codes, err := a.auth.RegenerateRecoveryCodes(user.Username, payload.Password, payload.Code)
	done(err == nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.auth.LogAudit(user.Username, a.clientIP(r), "2FA_RECOVERY_CODES", "Generated new recovery codes", "")
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "recovery_codes": codes})
}

// handleTwoFactorReset lets an administrator remove 2FA from an account (lost device).
func (a *app) handleTwoFactorReset(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	var payload struct {
		Username string `json:"username"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	if err := a.auth.ResetTwoFactor(payload.Username); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.auth.LogAudit(admin.Username, a.clientIP(r), "2FA_RESET", "Reset two-factor authentication of "+payload.Username, "")
	writeOK(w)
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(backend.SessionCookieName); err == nil {
		a.auth.Logout(cookie.Value)
	}
	a.setSessionCookies(w, r, "", "", -1)
	writeOK(w)
}

func (a *app) handleChangePassword(w http.ResponseWriter, r *http.Request, user backend.User, sessionID string) {
	var payload struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &payload, 16*1024) {
		return
	}
	if len(payload.NewPassword) > 1024 {
		writeError(w, http.StatusBadRequest, "Password too long")
		return
	}
	done, ok := a.limitReverify(w, user.Username)
	if !ok {
		return
	}
	err := a.auth.ChangeUserPassword(user.Username, payload.OldPassword, payload.NewPassword, sessionID)
	done(err == nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.auth.LogAudit(user.Username, a.clientIP(r), "PASSWORD_CHANGE", "Changed own password", "")
	writeOK(w)
}

// -------------------------------------------------------------
// Server list, stats & system
// -------------------------------------------------------------

func (a *app) visibleServers(user backend.User) []backend.GameServerInstance {
	servers := a.inst.GetInstances()
	if a.agentMode {
		if allowed := a.policy().Servers; len(allowed) > 0 {
			filtered := []backend.GameServerInstance{}
			for _, srv := range servers {
				if slices.Contains(allowed, srv.ID) {
					filtered = append(filtered, srv)
				}
			}
			servers = filtered
		}
	}
	servers = append(servers, a.nodeServers()...)
	if user.IsAdmin() {
		return servers
	}
	filtered := []backend.GameServerInstance{}
	for _, srv := range servers {
		if user.CanAccessServer(srv.ID) {
			filtered = append(filtered, srv)
		}
	}
	return filtered
}

func (a *app) handleListServers(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	writeJSON(w, http.StatusOK, a.visibleServers(user))
}

func (a *app) handleStats(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	if a.metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "Metrics unavailable")
		return
	}
	stats, err := a.metrics.GetStats(a.visibleServers(user))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to collect metrics")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (a *app) handleInfo(w http.ResponseWriter, r *http.Request, user backend.User, sessionID string) {
	session, _ := a.auth.GetSession(sessionID)
	var update interface{}
	if user.IsAdmin() && a.updater != nil {
		update = a.updater.Status()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"update":  update,
		"os":      fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH),
		"pid":     os.Getpid(),
		"version": Version,
		"user": map[string]interface{}{
			"username":     user.Username,
			"role":         user.Role,
			"servers":      user.Servers,
			"permissions":  user.Permissions,
			"totp_enabled": user.TOTPEnabled,
		},
		"csrf_token":   session.CSRFToken,
		"capabilities": a.inst.Capabilities(),
	})
}

func (a *app) handleFirewallStatus(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	if !a.inst.Capabilities().Firewall {
		writeError(w, http.StatusNotImplemented, "Firewall management is not available in this runtime")
		return
	}
	writeJSON(w, http.StatusOK, backend.GetFirewallStatus())
}

func (a *app) handleFirewallOpen(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	if !a.inst.Capabilities().Firewall {
		writeError(w, http.StatusNotImplemented, "Firewall management is not available in this runtime")
		return
	}
	var payload struct {
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
	}
	if !decodeJSON(w, r, &payload, maxJSONBody) {
		return
	}
	proto := strings.ToLower(payload.Protocol)
	if payload.Port <= 0 || payload.Port > 65535 {
		writeError(w, http.StatusBadRequest, "Invalid port number")
		return
	}
	if proto != "tcp" && proto != "udp" {
		writeError(w, http.StatusBadRequest, "Invalid protocol")
		return
	}
	if err := backend.OpenFirewallPort(payload.Port, proto); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.auth.LogAudit(user.Username, a.clientIP(r), "FIREWALL_OPEN", fmt.Sprintf("Opened port %d/%s", payload.Port, proto), "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": fmt.Sprintf("Port %d/%s rule added successfully", payload.Port, proto)})
}

func (a *app) handleAudit(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	writeJSON(w, http.StatusOK, a.auth.GetAuditLogs())
}

// -------------------------------------------------------------
// Bulk actions
// -------------------------------------------------------------

// maxBulkServers bounds the number of servers in a single bulk request.
const maxBulkServers = 100

func (a *app) handleBulk(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	var payload struct {
		Action    string   `json:"action"`
		ServerIDs []string `json:"serverIds"`
	}
	if !decodeJSON(w, r, &payload, 64*1024) {
		return
	}
	perm, ok := backend.BulkActionPermission(payload.Action)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid bulk action")
		return
	}
	if len(payload.ServerIDs) == 0 || len(payload.ServerIDs) > maxBulkServers {
		writeError(w, http.StatusBadRequest, "Invalid bulk payload")
		return
	}
	if a.agentMode {
		if !a.agentAllows(w, backend.ActionAccess(payload.Action)) {
			return
		}
		for _, id := range payload.ServerIDs {
			if !a.agentAllowsServer(w, id) {
				return
			}
		}
	}

	// Authorize EVERY server before anything is executed; one denied server rejects the request.
	seen := make(map[string]bool)
	ids := make([]string, 0, len(payload.ServerIDs))
	for _, id := range payload.ServerIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := backend.AuthorizeServerAction(&user, id, perm, a.serverExists); err != nil {
			writeAuthzError(w, err)
			return
		}
		ids = append(ids, id)
	}

	ip := a.clientIP(r)
	for _, id := range ids {
		a.auth.LogAudit(user.Username, ip, "BULK_"+strings.ToUpper(payload.Action), fmt.Sprintf("Executed bulk action %s", payload.Action), id)
	}
	writeJSON(w, http.StatusOK, a.runBulkAcrossNodes(r, user, payload.Action, ids))
}

// -------------------------------------------------------------
// Server scoped routes: /api/servers/{id}/{route}[/{sub}]
// -------------------------------------------------------------

// permByAction marks routes whose permission depends on the requested LinuxGSM action.
const permByAction = "by-action"

// routePolicy is what a server scoped route requires: the user permission (checked by the
// dashboard the user is logged in to) and the access class (checked by an agent against its
// access mode, see backend.AgentModeAllows).
type routePolicy struct {
	perm   string
	access string
}

// serverRoutes is the central RBAC policy for server scoped endpoints:
// "<route>[/<sub>] <METHOD>" -> required permission and access class.
var serverRoutes = map[string]routePolicy{
	"delete POST":               {backend.PermAdmin, backend.AccessWrite},
	"portcheck GET":             {backend.PermView, backend.AccessRead},
	"action POST":               {permByAction, backend.AccessByAction},
	"console GET":               {backend.PermConsole, backend.AccessRead},
	"console/send POST":         {backend.PermConsole, backend.AccessWrite},
	"rcon POST":                 {backend.PermConsole, backend.AccessWrite},
	"configs GET":               {backend.PermConfig, backend.AccessRead},
	"configs/file GET":          {backend.PermConfig, backend.AccessRead},
	"configs/file POST":         {backend.PermConfig, backend.AccessWrite},
	"tag POST":                  {backend.PermAdmin, backend.AccessWrite},
	"cron GET":                  {backend.PermView, backend.AccessRead},
	"cron POST":                 {backend.PermSchedule, backend.AccessWrite},
	"cron/install POST":         {backend.PermAdmin, backend.AccessWrite},
	"backups GET":               {backend.PermBackup, backend.AccessRead},
	"backups/delete POST":       {backend.PermBackup, backend.AccessWrite},
	"backups/upload POST":       {backend.PermBackup, backend.AccessWrite},
	"backups/download GET":      {backend.PermBackup, backend.AccessRead},
	"backups/restore POST":      {backend.PermBackup, backend.AccessWrite},
	"backups/settings GET":      {backend.PermBackup, backend.AccessRead},
	"backups/settings POST":     {backend.PermBackup, backend.AccessWrite},
	"alerts GET":                {backend.PermAlerts, backend.AccessRead},
	"alerts POST":               {backend.PermAlerts, backend.AccessWrite},
	"systemd/install POST":      {backend.PermAdmin, backend.AccessWrite},
	"files GET":                 {backend.PermFiles, backend.AccessRead},
	"files POST":                {backend.PermFiles, backend.AccessWrite},
	"files/download GET":        {backend.PermFiles, backend.AccessRead},
	"files/content GET":         {backend.PermFiles, backend.AccessRead},
	"files/upload POST":         {backend.PermFiles, backend.AccessWrite},
	"mods GET":                  {backend.PermView, backend.AccessRead},
	"mods/available GET":        {backend.PermAdmin, backend.AccessRead},
	"stopmode GET":              {backend.PermView, backend.AccessRead},
	"stopmode POST":             {backend.PermAdmin, backend.AccessWrite},
	"clone POST":                {backend.PermAdmin, backend.AccessWrite},
	"cloudbackup/settings GET":  {backend.PermBackup, backend.AccessRead},
	"cloudbackup/settings POST": {backend.PermAdmin, backend.AccessWrite},
	"cloudbackup/sync POST":     {backend.PermBackup, backend.AccessWrite},
	"fastdl GET":                {backend.PermView, backend.AccessRead},
	"fastdl POST":               {backend.PermAdmin, backend.AccessWrite},
}

// serverRoutePermission looks up the policy. status is 404 for unknown routes and 405 when the
// route exists with a different method.
func serverRoutePermission(route, method string) (policy routePolicy, status int) {
	if p, ok := serverRoutes[route+" "+method]; ok {
		return p, http.StatusOK
	}
	for key := range serverRoutes {
		if strings.HasPrefix(key, route+" ") {
			return routePolicy{}, http.StatusMethodNotAllowed
		}
	}
	return routePolicy{}, http.StatusNotFound
}

func (a *app) handleServer(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// ["api", "servers", "<id>", "<route>", "<sub>"]
	if len(parts) < 4 || len(parts) > 5 {
		http.NotFound(w, r)
		return
	}
	serverID := parts[2]
	route := parts[3]
	if len(parts) == 5 {
		route += "/" + parts[4]
	}

	policy, status := serverRoutePermission(route, r.Method)
	if status != http.StatusOK {
		if status == http.StatusMethodNotAllowed {
			http.Error(w, "Method not allowed", status)
		} else {
			http.NotFound(w, r)
		}
		return
	}

	authPerm := policy.perm
	if policy.perm == permByAction {
		authPerm = backend.PermView // refined below once the action is known
	}
	if a.agentMode && !a.agentAllowsServer(w, serverID) {
		return
	}
	if node, _ := backend.SplitServerID(serverID); node != "" {
		if a.agentMode {
			http.NotFound(w, r)
			return
		}
		if err := backend.AuthorizeServerAction(&user, serverID, authPerm, a.serverExists); err != nil {
			writeAuthzError(w, err)
			return
		}
		a.handleNodeServer(w, r, user, serverID, route, policy)
		return
	}
	if err := backend.AuthorizeServerAction(&user, serverID, authPerm, a.inst.ServerExists); err != nil {
		writeAuthzError(w, err)
		return
	}
	// On an agent the access mode applies on top (the action is checked in runAction).
	if a.agentMode && policy.access != backend.AccessByAction && !a.agentAllows(w, policy.access) {
		return
	}

	ctx := serverCtx{app: a, w: w, r: r, user: user, serverID: serverID, ip: a.clientIP(r)}
	switch route {
	case "delete":
		a.auth.LogAudit(user.Username, ctx.ip, "SERVER_DELETE", "Deleted server", serverID)
		if a.inst.DeleteServer(w, r, serverID) && !a.agentMode {
			// A later server may get the same ID; it must not inherit these permissions.
			if err := a.auth.RemoveServerFromUsers(serverID); err != nil {
				fmt.Printf("[WARNING] Failed to remove %s from user permissions: %v\n", serverID, err)
			}
		}
	case "portcheck":
		a.inst.CheckServerPorts(w, r, serverID)
	case "action":
		ctx.runAction()
	case "console":
		ctx.getConsole()
	case "console/send", "rcon":
		ctx.sendConsole(route)
	case "configs":
		ctx.listConfigs()
	case "configs/file":
		ctx.configFile()
	case "tag":
		ctx.setTag()
	case "cron":
		ctx.cron()
	case "cron/install":
		ctx.simple("CRON_INSTALL", "Installed default cronjobs", func() error { return a.inst.InstallCronjobs(serverID) })
	case "systemd/install":
		ctx.simple("SYSTEMD_INSTALL", "Installed systemd service", func() error { return a.inst.InstallSystemdService(serverID) })
	case "backups":
		ctx.listBackups()
	case "backups/delete":
		ctx.deleteBackup()
	case "backups/upload":
		ctx.uploadBackup()
	case "backups/download":
		ctx.downloadBackup()
	case "backups/restore":
		ctx.restoreBackup()
	case "backups/settings":
		ctx.backupSettings()
	case "alerts":
		ctx.alerts()
	case "files", "files/download", "files/content", "files/upload":
		ctx.files(route)
	case "mods":
		mods, _ := a.inst.GetInstalledMods(serverID)
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "installed": mods})
	case "mods/available":
		mods, err := a.inst.AvailableMods(serverID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not list the mods")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "available": mods})
	case "stopmode":
		ctx.stopMode()
	case "clone":
		ctx.clone()
	case "cloudbackup/settings":
		ctx.cloudSettings()
	case "cloudbackup/sync":
		a.auth.LogAudit(user.Username, ctx.ip, "CLOUDBACKUP_SYNC", "Triggered cloud backup sync", serverID)
		a.inst.SyncCloudBackup(w, r, serverID, langOf(r, ""))
	case "fastdl":
		ctx.fastdlSettings()
	default:
		http.NotFound(w, r)
	}
}

// fastdlSettings reads or sets who serves the server's FastDL files. On a node, node_url is the
// address under which the node serves them itself (empty when it does not).
func (c serverCtx) fastdlSettings() {
	if c.r.Method == http.MethodGet {
		resp := map[string]string{"mode": c.app.inst.FastDLMode(c.serverID)}
		if c.app.agentMode && c.app.fastdlURL != "" {
			resp["node_url"] = strings.TrimRight(c.app.fastdlURL, "/") + "/" + url.PathEscape(c.serverID) + "/"
		}
		writeJSON(c.w, http.StatusOK, resp)
		return
	}
	var payload struct {
		Mode string `json:"mode"`
	}
	if !decodeJSON(c.w, c.r, &payload, 4*1024) {
		return
	}
	if payload.Mode == backend.FastDLNode && !c.app.agentMode {
		writeError(c.w, http.StatusBadRequest, "Only servers of nodes can be served by the node")
		return
	}
	if err := c.app.inst.SetFastDLMode(c.serverID, payload.Mode); err != nil {
		writeError(c.w, http.StatusBadRequest, err.Error())
		return
	}
	c.audit("FASTDL_MODE", "FastDL served by "+payload.Mode)
	writeOK(c.w)
}

// -------------------------------------------------------------
// Servers on nodes (multi-node)
// -------------------------------------------------------------

// serverExists knows the servers of this machine and the cached servers of the nodes.
func (a *app) serverExists(serverID string) bool {
	if node, _ := backend.SplitServerID(serverID); node == "" {
		return a.inst.ServerExists(serverID)
	}
	if a.agents == nil {
		return false
	}
	_, ok := a.agents.Hub.NodeServer(serverID)
	return ok
}

// nodeServers returns the servers of all nodes with the tags the dashboard keeps for them.
func (a *app) nodeServers() []backend.GameServerInstance {
	if a.agents == nil || a.agentMode {
		return nil
	}
	servers := a.agents.Hub.NodeServers()
	if len(servers) == 0 {
		return servers
	}
	tags := a.inst.ServerTags()
	for i := range servers {
		servers[i].Tag = tags[servers[i].ID]
	}
	return servers
}

// nodeTunnel returns the tunnel of a node that may receive forwarded requests, or answers the
// request with the reason why not.
func (a *app) nodeTunnel(w http.ResponseWriter, nodeID string) (*backend.AgentTunnelClient, backend.NodeStatus, bool) {
	st := a.agents.Hub.Status(nodeID)
	client, ok := a.agents.Hub.Tunnel(nodeID)
	switch {
	case !ok || !st.Online:
		writeError(w, http.StatusServiceUnavailable, "The node is offline")
		return nil, st, false
	case !st.Compatible:
		msg := "The node needs an update"
		if st.Problem != "" {
			msg = "The node cannot be used: " + st.Problem
		}
		writeError(w, http.StatusConflict, msg)
		return nil, st, false
	}
	return client, st, true
}

// installOnNode forwards a game installation to a node. The payload was validated like a local
// installation; the node validates it again for its own runtime. The output streams back, and
// the node keeps installing if the browser goes away.
func (a *app) installOnNode(w http.ResponseWriter, r *http.Request, user backend.User, nodeID string, payload map[string]string) {
	if a.agents == nil || !backend.IsValidNodeID(nodeID) {
		writeError(w, http.StatusBadRequest, "Invalid node")
		return
	}
	if _, ok := a.agents.Nodes.Node(nodeID); !ok {
		writeError(w, http.StatusNotFound, "Node not found")
		return
	}
	client, st, ok := a.nodeTunnel(w, nodeID)
	if !ok {
		return
	}
	if !backend.AgentModeAllows(st.EffectiveMode, backend.AccessWrite) {
		writeError(w, http.StatusForbidden, "This node is read-only (start, stop and restart are allowed)")
		return
	}
	body, _ := json.Marshal(payload)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")

	details := fmt.Sprintf("Installed %s as user %s on node %s", payload["game"], payload["user"], nodeID)
	if payload["install_dir"] != "" {
		details += " in " + payload["install_dir"]
	}
	a.auth.LogAudit(user.Username, a.clientIP(r), "SERVER_INSTALL", details, "")
	a.forwardToNode(w, r, client, user, "/api/games/install")
	_ = a.agents.Hub.RefreshServers(nodeID)
}

// Routes whose request body is an upload (no read deadline, upload size limit).
var nodeUploadRoutes = map[string]bool{"backups/upload": true, "files/upload": true}

// handleNodeServer handles a server scoped request for a server on a node. The user is already
// authorized for the route; the action of action requests, the node's access mode and the
// node's state are checked here before the request is forwarded through the tunnel.
func (a *app) handleNodeServer(w http.ResponseWriter, r *http.Request, user backend.User, serverID, route string, policy routePolicy) {
	nodeID, localID := backend.SplitServerID(serverID)
	ip := a.clientIP(r)

	// Tags are kept by the dashboard, not by the node.
	if route == "tag" {
		var payload struct {
			Tag string `json:"tag"`
		}
		if !decodeJSON(w, r, &payload, 16*1024) {
			return
		}
		if err := a.inst.SetNodeServerTag(serverID, payload.Tag); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeOK(w)
		return
	}

	client, st, ok := a.nodeTunnel(w, nodeID)
	if !ok {
		return
	}

	limit := int64(maxEditorBody)
	if nodeUploadRoutes[route] {
		limit = a.maxUploadBytes
		backend.DisableReadDeadline(w)
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	access := policy.access
	if access == backend.AccessByAction {
		// The action decides permission and access class; the body is read here and forwarded
		// unchanged.
		body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024+1))
		if err != nil || len(body) > 16*1024 {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		var payload struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(body, &payload) != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		perm, known := backend.ActionPermission(payload.Action)
		if !known {
			writeError(w, http.StatusBadRequest, "Invalid action name")
			return
		}
		if err := backend.AuthorizeServerAction(&user, serverID, perm, nil); err != nil {
			writeAuthzError(w, err)
			return
		}
		access = backend.ActionAccess(payload.Action)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	if !backend.AgentModeAllows(st.EffectiveMode, access) {
		writeError(w, http.StatusForbidden, "This node is read-only (start, stop and restart are allowed)")
		return
	}

	if r.Method != http.MethodGet {
		a.auth.LogAudit(user.Username, ip, "NODE_REQUEST", fmt.Sprintf("%s %s on node %s", r.Method, route, nodeID), serverID)
	}
	release, ok := a.nodeRequests.acquire(nodeID, user.Username)
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "Too many requests to this node at once, try again shortly")
		return
	}
	defer release()
	a.forwardToNode(w, r, client, user, "/api/servers/"+localID+"/"+route)

	switch route {
	case "delete", "clone":
		// The node's list changes; when the server is gone, its assignments go as well.
		_ = a.agents.Hub.RefreshServers(nodeID)
		if route == "delete" {
			if _, exists := a.agents.Hub.NodeServer(serverID); !exists {
				if err := a.auth.RemoveServerFromUsers(serverID); err != nil {
					fmt.Printf("[WARNING] Failed to remove %s from user permissions: %v\n", serverID, err)
				}
				_ = a.inst.SetNodeServerTag(serverID, "")
			}
		}
	}
}

// forwardToNode sends r to path on the node and streams the answer back. The request is rebuilt
// from an allowlist of headers, and the response is treated as untrusted content.
func (a *app) forwardToNode(w http.ResponseWriter, r *http.Request, client *backend.AgentTunnelClient, user backend.User, path string) {
	// Answers can stream for a long time (console output, downloads, actions).
	backend.DisableWriteDeadline(w)
	// Whatever the node sends must never run in the dashboard's origin. hardenNodeResponse puts
	// the security headers on the response itself; ReverseProxy adds response headers to these,
	// so the ones the middleware set are removed to avoid duplicates.
	for _, k := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		w.Header().Del(k)
	}

	ip := a.clientIP(r)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL = &url.URL{Scheme: "https", Host: "agent", Path: path, RawQuery: pr.In.URL.RawQuery}
			out.Host = "agent"
			h := http.Header{}
			// Range and If-Range let interrupted downloads continue.
			for _, k := range []string{"Content-Type", "Accept", "Accept-Language", "Range", "If-Range"} {
				if v := pr.In.Header.Get(k); v != "" {
					h.Set(k, v)
				}
			}
			h.Set(backend.AgentActorHeader, user.Username)
			h.Set(backend.AgentActorIPHeader, ip)
			if user.HasPermission(backend.PermSchedule) {
				h.Set(backend.AgentActorScheduleHeader, "1")
			}
			if user.Role == "admin" {
				h.Set(backend.AgentActorAdminHeader, "1")
			}
			out.Header = h
		},
		Transport:      tunnelTransport{client},
		FlushInterval:  -1,
		ModifyResponse: hardenNodeResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the browser went away
			}
			setNodeSecurityHeaders(w.Header())
			writeError(w, http.StatusBadGateway, "The node did not answer")
		},
	}
	proxy.ServeHTTP(w, r)
}

// runBulkAcrossNodes runs a bulk action on the servers of this machine and sends the servers of
// each node to that node in one request. Results are keyed by the IDs the browser sent.
func (a *app) runBulkAcrossNodes(r *http.Request, user backend.User, action string, ids []string) map[string]string {
	var local []string
	byNode := make(map[string][]string)
	for _, id := range ids {
		if node, localID := backend.SplitServerID(id); node == "" {
			local = append(local, id)
		} else {
			byNode[node] = append(byNode[node], localID)
		}
	}
	results := make(map[string]string, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for node, localIDs := range byNode {
		wg.Go(func() {
			res := a.nodeBulk(r, user, node, action, localIDs)
			mu.Lock()
			maps.Copy(results, res)
			mu.Unlock()
		})
	}
	if len(local) > 0 {
		res := a.inst.RunBulkAction(action, local)
		mu.Lock()
		maps.Copy(results, res)
		mu.Unlock()
	}
	wg.Wait()
	return results
}

// nodeBulk runs a bulk action on one node. Every requested server gets a result, also when the
// node cannot be reached.
func (a *app) nodeBulk(r *http.Request, user backend.User, nodeID, action string, localIDs []string) map[string]string {
	out := make(map[string]string, len(localIDs))
	fail := func(msg string) map[string]string {
		for _, l := range localIDs {
			out[backend.GlobalServerID(nodeID, l)] = "Error: " + msg
		}
		return out
	}
	client, online := a.agents.Hub.Tunnel(nodeID)
	st := a.agents.Hub.Status(nodeID)
	switch {
	case !online || !st.Online:
		return fail("the node is offline")
	case !st.Compatible:
		return fail("the node needs an update")
	case !backend.AgentModeAllows(st.EffectiveMode, backend.ActionAccess(action)):
		return fail("the node is read-only")
	}
	body, _ := json.Marshal(map[string]interface{}{"action": action, "serverIds": localIDs})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "/api/servers/bulk", bytes.NewReader(body))
	if err != nil {
		return fail(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(backend.AgentActorHeader, user.Username)
	req.Header.Set(backend.AgentActorIPHeader, a.clientIP(r))
	resp, err := client.Do(req)
	if err != nil {
		return fail("the node did not answer")
	}
	defer resp.Body.Close()
	var res map[string]string
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&res) != nil {
		return fail(fmt.Sprintf("the node refused the request (%d)", resp.StatusCode))
	}
	for _, l := range localIDs {
		msg := res[l]
		if msg == "" {
			msg = "Error: no result from the node"
		}
		out[backend.GlobalServerID(nodeID, l)] = plainText(msg, 256)
	}
	go func() { _ = a.agents.Hub.RefreshServers(nodeID) }()
	return out
}

// plainText removes control characters and limits text that came from another machine.
func plainText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > max {
		s = s[:max]
	}
	return strings.ToValidUTF8(s, "")
}

// maxNodeRequestsPerUser bounds the forwarded requests one user keeps open on one node, so a
// single account cannot occupy the node's tunnel (slow port checks, slow downloads).
const maxNodeRequestsPerUser = 24

type nodeRequestLimiter struct {
	mu     sync.Mutex
	active map[string]int
}

// acquire takes a slot for user on node; release must be called when the request ends.
func (l *nodeRequestLimiter) acquire(node, user string) (release func(), ok bool) {
	key := node + "\x00" + user
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == nil {
		l.active = make(map[string]int)
	}
	if l.active[key] >= maxNodeRequestsPerUser {
		return nil, false
	}
	l.active[key]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.active[key]--; l.active[key] <= 0 {
			delete(l.active, key)
		}
	}, true
}

// tunnelTransport sends proxied requests through an agent tunnel.
type tunnelTransport struct{ client *backend.AgentTunnelClient }

// errNode1xx aborts a forwarded request whose node answered with an informational status.
var errNode1xx = errors.New("informational response from node")

// RoundTrip refuses 1xx answers: ReverseProxy would pass the node's headers on and then clear
// the dashboard's security headers of the final response.
func (t tunnelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx := httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{
		Got1xxResponse: func(int, textproto.MIMEHeader) error { return errNode1xx },
	})
	return t.client.Do(r.WithContext(ctx))
}

// Patterns for size headers a node may send along with downloads.
var (
	reDigits       = regexp.MustCompile(`^[0-9]{1,19}$`)
	reContentRange = regexp.MustCompile(`^bytes ([0-9]{1,19}-[0-9]{1,19}|\*)/([0-9]{1,19}|\*)$`)
)

// Media types a node may answer with; anything else is delivered as a download.
var nodeContentTypes = map[string]bool{
	"application/json":         true,
	"text/event-stream":        true,
	"text/plain":               true,
	"application/octet-stream": true,
}

// hardenNodeResponse replaces the node's response headers with a checked minimum. Cookies,
// security headers, redirects and anything else a compromised node might send are dropped.
func hardenNodeResponse(resp *http.Response) error {
	in := resp.Header
	out := http.Header{}

	// A node never answers 401 (the browser would log out of the dashboard) or redirects.
	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		resp.Body.Close()
		resp.StatusCode, resp.Status = http.StatusBadGateway, "502 Bad Gateway"
		resp.Body = io.NopCloser(strings.NewReader(`{"error":"Unexpected answer from the node"}` + "\n"))
		resp.ContentLength = -1
		resp.Header = http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
		setNodeSecurityHeaders(resp.Header)
		resp.Trailer = nil
		return nil
	}

	mediaType, params, err := mime.ParseMediaType(in.Get("Content-Type"))
	if err != nil || !nodeContentTypes[mediaType] {
		mediaType, params = "application/octet-stream", nil
	}
	ctParams := map[string]string{}
	if cs := strings.ToLower(params["charset"]); cs == "utf-8" {
		ctParams["charset"] = cs
	}
	out.Set("Content-Type", mime.FormatMediaType(mediaType, ctParams))
	if cd := in.Get("Content-Disposition"); cd != "" {
		if _, p, err := mime.ParseMediaType(cd); err == nil && p["filename"] != "" {
			out.Set("Content-Disposition", attachmentDisposition(filepath.Base(p["filename"])))
		}
	}
	for _, k := range []string{"Last-Modified", "ETag"} {
		if v := in.Get(k); v != "" && len(v) < 128 {
			out.Set(k, v)
		}
	}
	// Sizes and ranges of downloads (progress, resuming); JSON answers are cut below instead.
	if mediaType != "application/json" {
		if v := in.Get("Content-Length"); reDigits.MatchString(v) {
			out.Set("Content-Length", v)
		}
		if v := in.Get("Content-Range"); reContentRange.MatchString(v) {
			out.Set("Content-Range", v)
		}
		if in.Get("Accept-Ranges") == "bytes" {
			out.Set("Accept-Ranges", "bytes")
		}
	}
	out.Set("Cache-Control", "no-store")
	setNodeSecurityHeaders(out)
	resp.Header = out
	resp.Trailer = nil
	if mediaType == "application/json" {
		resp.Body = limitedBody{Reader: io.LimitReader(resp.Body, 64<<20), Closer: resp.Body}
		resp.ContentLength = -1
	}
	return nil
}

// setNodeSecurityHeaders puts the headers that keep a node's answer from running in the
// dashboard origin on the response itself, so nothing on the way can drop them.
func setNodeSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", sandboxCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}

type limitedBody struct {
	io.Reader
	io.Closer
}

func langOf(r *http.Request, bodyLang string) string {
	if bodyLang == "de" || r.URL.Query().Get("lang") == "de" {
		return "de"
	}
	return "en"
}

type serverCtx struct {
	app      *app
	w        http.ResponseWriter
	r        *http.Request
	user     backend.User
	serverID string
	ip       string
}

func (c serverCtx) audit(action, details string) {
	c.app.auth.LogAudit(c.user.Username, c.ip, action, details, c.serverID)
}

// simple runs a POST endpoint without payload.
func (c serverCtx) simple(auditAction, details string, fn func() error) {
	if err := fn(); err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	c.audit(auditAction, details)
	writeOK(c.w)
}

func (c serverCtx) runAction() {
	var payload struct {
		Action   string `json:"action"`
		Mod      string `json:"mod"`
		Password string `json:"password"`
		Lang     string `json:"lang"`
	}
	if !decodeJSON(c.w, c.r, &payload, 16*1024) {
		return
	}
	perm, ok := backend.ActionPermission(payload.Action)
	if !ok {
		writeError(c.w, http.StatusBadRequest, "Invalid action name")
		return
	}
	if err := backend.AuthorizeServerAction(&c.user, c.serverID, perm, nil); err != nil {
		writeAuthzError(c.w, err)
		return
	}
	if c.app.agentMode && !c.app.agentAllows(c.w, backend.ActionAccess(payload.Action)) {
		return
	}
	detail := fmt.Sprintf("Executed action %s", payload.Action)
	if payload.Mod != "" && backend.IsValidModName(payload.Mod) {
		detail += " (" + payload.Mod + ")"
	}
	c.audit("ACTION_"+strings.ToUpper(payload.Action), detail)
	input := backend.ActionInput{Mod: payload.Mod, Password: payload.Password}
	c.app.inst.RunAction(c.w, c.r, c.serverID, payload.Action, input, langOf(c.r, payload.Lang))
}

func (c serverCtx) getConsole() {
	mode := c.r.URL.Query().Get("mode")
	switch mode {
	case "", "tmux":
		mode = "tmux"
	case "log", "console", "script", "game":
	default:
		writeError(c.w, http.StatusBadRequest, "Invalid console mode")
		return
	}
	lines, err := c.app.inst.GetConsole(c.serverID, mode, langOf(c.r, ""))
	if err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(c.w, http.StatusOK, map[string]interface{}{"lines": lines})
}

// sendConsole handles both console input and RCON/player actions (kick, ban) through the same
// permission (console) and validation path.
func (c serverCtx) sendConsole(route string) {
	var payload struct {
		Command string `json:"command"`
	}
	if !decodeJSON(c.w, c.r, &payload, 16*1024) {
		return
	}
	if err := backend.ValidateConsoleCommand(payload.Command); err != nil {
		writeError(c.w, http.StatusBadRequest, "Invalid command payload")
		return
	}
	if err := c.app.inst.SendConsoleCommand(c.serverID, payload.Command); err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	action := "CONSOLE_COMMAND"
	if route == "rcon" {
		action = "RCON_COMMAND"
	}
	c.audit(action, "Sent: "+payload.Command)
	writeOK(c.w)
}

// actorIsAdmin reports whether the user behind the request is an administrator. On a node the
// tunnel identity is one, so the controller's header decides.
func (c serverCtx) actorIsAdmin() bool {
	if c.app.agentMode {
		return c.r.Header.Get(backend.AgentActorAdminHeader) == "1"
	}
	return c.user.Role == "admin"
}

func (c serverCtx) listConfigs() {
	configs, err := c.app.inst.GetConfigFiles(c.serverID)
	if err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	// LinuxGSM's own configs are bash and hold secrets: administrators only.
	visible := []backend.ConfigFile{}
	for _, f := range configs {
		if c.actorIsAdmin() || !c.app.inst.IsLinuxGSMSettingsFile(c.serverID, f.Path) {
			visible = append(visible, f)
		}
	}
	configs = visible
	writeJSON(c.w, http.StatusOK, configs)
}

func (c serverCtx) configFile() {
	path := c.r.URL.Query().Get("path")
	if c.r.Method != http.MethodGet {
		path = ""
	}
	deny := func(p string) bool {
		if !c.actorIsAdmin() && c.app.inst.IsLinuxGSMSettingsFile(c.serverID, p) {
			writeError(c.w, http.StatusForbidden, "LinuxGSM's own config files can only be edited by administrators")
			return true
		}
		return false
	}
	if path != "" && deny(path) {
		return
	}
	if c.r.Method == http.MethodGet {
		filePath := c.r.URL.Query().Get("path")
		if filePath == "" {
			writeError(c.w, http.StatusBadRequest, "Missing path query parameter")
			return
		}
		content, err := c.app.inst.GetConfigFileContent(c.serverID, filePath)
		if err != nil {
			writeError(c.w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(c.w, http.StatusOK, map[string]string{"content": content})
		return
	}
	var payload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeJSON(c.w, c.r, &payload, maxEditorBody) {
		return
	}
	if payload.Path == "" {
		writeError(c.w, http.StatusBadRequest, "Invalid payload")
		return
	}
	if deny(payload.Path) {
		return
	}
	result, err := c.app.inst.SaveConfigFileContent(c.serverID, payload.Path, payload.Content)
	if err != nil {
		writeError(c.w, http.StatusForbidden, err.Error())
		return
	}
	if result.RedirectedTo != "" {
		c.audit("CONFIG_EDIT", "Saved _default.cfg changes to "+filepath.Base(result.RedirectedTo))
		writeJSON(c.w, http.StatusOK, map[string]interface{}{"status": "ok", "redirected_to": result.RedirectedTo, "keys": result.Keys})
		return
	}
	c.audit("CONFIG_EDIT", "Saved config "+filepath.Base(payload.Path))
	writeOK(c.w)
}

func (c serverCtx) setTag() {
	var payload struct {
		Tag string `json:"tag"`
	}
	if !decodeJSON(c.w, c.r, &payload, 16*1024) {
		return
	}
	if err := c.app.inst.SetServerTag(c.serverID, payload.Tag); err != nil {
		writeError(c.w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(c.w)
}

func (c serverCtx) cron() {
	if c.r.Method == http.MethodGet {
		sched, err := c.app.inst.GetCronSchedule(c.serverID)
		if err != nil {
			writeError(c.w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(c.w, http.StatusOK, sched)
		return
	}
	var sched backend.CronSchedule
	if !decodeJSON(c.w, c.r, &sched, 16*1024) {
		return
	}
	if err := backend.ValidateCronSchedule(sched); err != nil {
		writeError(c.w, http.StatusBadRequest, err.Error())
		return
	}
	if err := c.app.inst.SaveCronSchedule(c.serverID, sched); err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	c.audit("CRON_SAVE", "Updated cron schedule")
	writeOK(c.w)
}

func (c serverCtx) listBackups() {
	backups, err := c.app.inst.ListBackups(c.serverID)
	if err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(c.w, http.StatusOK, backups)
}

func (c serverCtx) deleteBackup() {
	fileName := c.r.URL.Query().Get("file")
	if !backend.IsValidBackupFilename(fileName) {
		writeError(c.w, http.StatusBadRequest, "Invalid file parameter")
		return
	}
	if err := c.app.inst.DeleteBackup(c.serverID, fileName); err != nil {
		writeError(c.w, http.StatusNotFound, err.Error())
		return
	}
	c.audit("BACKUP_DELETE", "Deleted backup "+fileName)
	writeOK(c.w)
}

// uploadReader streams the first file part of a multipart request without buffering it in memory
// or temporary files. The total body is limited to maxUploadBytes.
func (c serverCtx) uploadReader(field string) (io.Reader, string, map[string]string, bool) {
	backend.DisableReadDeadline(c.w)
	backend.DisableWriteDeadline(c.w)
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, c.app.maxUploadBytes)
	mr, err := c.r.MultipartReader()
	if err != nil {
		writeError(c.w, http.StatusBadRequest, "Expected multipart/form-data upload")
		return nil, "", nil, false
	}
	values := map[string]string{}
	for {
		part, err := mr.NextPart()
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "Missing file parameter")
			return nil, "", nil, false
		}
		if part.FormName() == field && part.FileName() != "" {
			return part, part.FileName(), values, true
		}
		// Small text fields (e.g. target path) that precede the file part.
		data, err := io.ReadAll(io.LimitReader(part, 4096))
		if err == nil {
			values[part.FormName()] = string(data)
		}
	}
}

func uploadErrorStatus(err error) int {
	if isBodyTooLarge(err) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// isBodyTooLarge detects the error of http.MaxBytesReader. The message is matched as well because
// some callers wrap the error with %v, which hides the http.MaxBytesError type.
func isBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge) || err != nil && strings.Contains(err.Error(), "http: request body too large")
}

func (c serverCtx) uploadBackup() {
	reader, filename, _, ok := c.uploadReader("backup")
	if !ok {
		return
	}
	if err := c.app.inst.UploadBackup(c.serverID, filename, reader); err != nil {
		status := uploadErrorStatus(err)
		msg := err.Error()
		if status == http.StatusRequestEntityTooLarge {
			msg = "Upload exceeds the configured size limit"
		}
		writeError(c.w, status, msg)
		return
	}
	c.audit("BACKUP_UPLOAD", "Uploaded backup "+filename)
	writeOK(c.w)
}

// attachmentDisposition builds a safe Content-Disposition header.
func attachmentDisposition(name string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": name})
}

func (c serverCtx) downloadBackup() {
	fileName := c.r.URL.Query().Get("file")
	if !backend.IsValidBackupFilename(fileName) {
		writeError(c.w, http.StatusBadRequest, "Invalid file parameter")
		return
	}
	filePath, err := c.app.inst.GetBackupPath(c.serverID, fileName)
	if err != nil {
		writeError(c.w, http.StatusNotFound, "Backup file not found")
		return
	}
	f, err := backend.OpenInRootForRead(filePath)
	if err != nil {
		writeError(c.w, http.StatusNotFound, "Backup file not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(c.w, http.StatusNotFound, "Backup file not found")
		return
	}
	backend.DisableWriteDeadline(c.w)
	c.w.Header().Set("Content-Disposition", attachmentDisposition(fileName))
	format, _ := backend.BackupArchiveFormat(fileName)
	c.w.Header().Set("Content-Type", format.ContentType())
	c.w.Header().Set("Content-Security-Policy", sandboxCSP)
	http.ServeContent(c.w, c.r, fileName, info.ModTime(), f)
}

func (c serverCtx) restoreBackup() {
	var payload struct {
		File string `json:"file"`
		Lang string `json:"lang"`
	}
	if !decodeJSON(c.w, c.r, &payload, 16*1024) {
		return
	}
	if !backend.IsValidBackupFilename(payload.File) {
		writeError(c.w, http.StatusBadRequest, "Invalid file parameter")
		return
	}
	c.audit("BACKUP_RESTORE", "Restored backup "+payload.File)
	c.app.inst.RestoreBackup(c.w, c.r, c.serverID, payload.File, langOf(c.r, payload.Lang))
}

func (c serverCtx) backupSettings() {
	if c.r.Method == http.MethodGet {
		settings, err := c.app.inst.GetBackupSettings(c.serverID)
		if err != nil {
			writeError(c.w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(c.w, http.StatusOK, settings)
		return
	}
	var payload backend.BackupSettings
	if !decodeJSON(c.w, c.r, &payload, 16*1024) {
		return
	}
	// Changing the automatic backup cron requires the schedule permission.
	allowSchedule := c.user.HasPermission(backend.PermSchedule)
	if c.app.agentMode {
		// The tunnel identity is an administrator; the controller says what the user may do.
		allowSchedule = c.r.Header.Get(backend.AgentActorScheduleHeader) == "1"
	}
	if err := c.app.inst.SaveBackupSettings(c.serverID, payload, allowSchedule); err != nil {
		writeError(c.w, http.StatusBadRequest, err.Error())
		return
	}
	c.audit("SETTINGS_BACKUP", "Updated backup settings")
	writeOK(c.w)
}

func (c serverCtx) alerts() {
	if c.r.Method == http.MethodGet {
		settings, err := c.app.inst.GetAlertSettings(c.serverID)
		if err != nil {
			writeError(c.w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(c.w, http.StatusOK, settings)
		return
	}
	var payload struct {
		Values map[string]string `json:"values"`
	}
	if !decodeJSON(c.w, c.r, &payload, 64*1024) {
		return
	}
	if err := c.app.inst.SaveAlertSettings(c.serverID, payload.Values); err != nil {
		writeError(c.w, http.StatusBadRequest, err.Error())
		return
	}
	c.audit("SETTINGS_ALERTS", "Updated alert settings")
	writeOK(c.w)
}

func (c serverCtx) files(route string) {
	switch route {
	case "files/download":
		relPath := c.r.URL.Query().Get("path")
		if relPath == "" {
			writeError(c.w, http.StatusBadRequest, "Missing file path")
			return
		}
		f, info, err := c.app.inst.OpenServerFile(c.serverID, relPath)
		if err != nil {
			writeError(c.w, http.StatusNotFound, err.Error())
			return
		}
		defer f.Close()
		backend.DisableWriteDeadline(c.w)
		c.w.Header().Set("Content-Disposition", attachmentDisposition(info.Name()))
		c.w.Header().Set("Content-Type", "application/octet-stream")
		c.w.Header().Set("Content-Security-Policy", sandboxCSP)
		http.ServeContent(c.w, c.r, info.Name(), info.ModTime(), f)

	case "files/content":
		relPath := c.r.URL.Query().Get("path")
		if relPath == "" {
			writeError(c.w, http.StatusBadRequest, "Missing file path")
			return
		}
		content, err := c.app.inst.GetServerFileContent(c.serverID, relPath)
		if err != nil {
			writeError(c.w, http.StatusBadRequest, err.Error())
			return
		}
		c.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		c.w.Header().Set("Content-Security-Policy", sandboxCSP)
		_, _ = c.w.Write([]byte(content))

	case "files/upload":
		reader, filename, values, ok := c.uploadReader("file")
		if !ok {
			return
		}
		subPath := values["path"]
		if err := c.app.inst.UploadServerFile(c.serverID, subPath, filename, reader); err != nil {
			status := uploadErrorStatus(err)
			msg := err.Error()
			if status == http.StatusRequestEntityTooLarge {
				msg = "Upload exceeds the configured size limit"
			}
			writeError(c.w, status, msg)
			return
		}
		c.audit("FILE_UPLOAD", fmt.Sprintf("Uploaded file %s to %s", filepath.Base(filename), subPath))
		writeOK(c.w)

	case "files":
		if c.r.Method == http.MethodGet {
			nodes, err := c.app.inst.GetServerFiles(c.serverID, c.r.URL.Query().Get("path"))
			if err != nil {
				writeError(c.w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(c.w, http.StatusOK, nodes)
			return
		}
		var payload struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if !decodeJSON(c.w, c.r, &payload, maxEditorBody) {
			return
		}
		if payload.Path == "" {
			writeError(c.w, http.StatusBadRequest, "Invalid payload")
			return
		}
		if err := c.app.inst.SaveServerFileContent(c.serverID, payload.Path, payload.Content); err != nil {
			writeError(c.w, http.StatusBadRequest, err.Error())
			return
		}
		c.audit("FILE_EDIT", fmt.Sprintf("Saved file %s", payload.Path))
		writeOK(c.w)
	}
}

func (c serverCtx) stopMode() {
	if c.r.Method == http.MethodGet {
		stopMode, err := c.app.inst.GetStopModeSettings(c.serverID)
		if err != nil {
			writeError(c.w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(c.w, http.StatusOK, map[string]string{"stopmode": stopMode})
		return
	}
	var req struct {
		StopMode string `json:"stopmode"`
	}
	if !decodeJSON(c.w, c.r, &req, 16*1024) {
		return
	}
	if !backend.IsValidStopMode(req.StopMode) {
		writeError(c.w, http.StatusBadRequest, "Invalid stop mode")
		return
	}
	if err := c.app.inst.SaveStopModeSettings(c.serverID, req.StopMode); err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	c.audit("SETTINGS_STOPMODE", fmt.Sprintf("Updated stopmode to %s", req.StopMode))
	writeOK(c.w)
}

func (c serverCtx) clone() {
	var req struct {
		Suffix string `json:"suffix"`
	}
	if !decodeJSON(c.w, c.r, &req, 16*1024) {
		return
	}
	// Cloning a container copies the server files, which can take longer than the usual deadline.
	backend.DisableWriteDeadline(c.w)
	newScript, err := c.app.inst.CreateMultiInstance(c.serverID, req.Suffix)
	if err != nil {
		writeError(c.w, http.StatusBadRequest, err.Error())
		return
	}
	c.audit("SERVER_CLONE", fmt.Sprintf("Cloned instance %s to %s", c.serverID, newScript))
	writeJSON(c.w, http.StatusOK, map[string]string{"status": "ok", "new_script": newScript})
}

func (c serverCtx) cloudSettings() {
	if c.r.Method == http.MethodGet {
		settings, err := c.app.inst.GetCloudBackupSettings(c.serverID)
		if err != nil {
			writeError(c.w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(c.w, http.StatusOK, settings)
		return
	}
	var settings backend.CloudBackupSettings
	if !decodeJSON(c.w, c.r, &settings, 16*1024) {
		return
	}
	if err := c.app.inst.SaveCloudBackupSettings(c.serverID, settings); err != nil {
		writeError(c.w, http.StatusBadRequest, err.Error())
		return
	}
	c.audit("SETTINGS_CLOUDBACKUP", "Updated cloud backup settings")
	writeOK(c.w)
}

// -------------------------------------------------------------
// Games & installation
// -------------------------------------------------------------

func (a *app) handleInstall(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	var payload struct {
		Game       string `json:"game"`
		User       string `json:"user"`
		Password   string `json:"password"`
		InstallDir string `json:"install_dir"`
		Lang       string `json:"lang"`
		// Node to install on (multi-node); empty installs on this machine.
		Node string `json:"node"`
	}
	if !decodeJSON(w, r, &payload, 16*1024) {
		return
	}
	if payload.Game == "" || payload.User == "" {
		writeError(w, http.StatusBadRequest, "Missing game or user parameter")
		return
	}
	if !isValidGameCmd(payload.Game) || !isValidUsername(payload.User) {
		writeError(w, http.StatusBadRequest, "Invalid game command or username characters")
		return
	}
	if len(payload.Password) > 256 || strings.ContainsAny(payload.Password, ":\r\n\x00") {
		writeError(w, http.StatusBadRequest, "Invalid password")
		return
	}
	installDir := strings.TrimSpace(payload.InstallDir)
	if len(installDir) > 1 {
		installDir = strings.TrimRight(installDir, "/")
	}
	if payload.Node != "" && !a.agentMode {
		a.installOnNode(w, r, user, payload.Node, map[string]string{
			"game": payload.Game, "user": payload.User, "password": payload.Password,
			"install_dir": installDir, "lang": payload.Lang,
		})
		return
	}
	if err := a.inst.ValidateInstall(payload.Game, payload.User, installDir); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	details := fmt.Sprintf("Installed %s as user %s", payload.Game, payload.User)
	if installDir != "" {
		details += " in " + installDir
	}
	a.auth.LogAudit(user.Username, a.clientIP(r), "SERVER_INSTALL", details, "")
	a.inst.InstallGame(w, r, payload.Game, payload.User, payload.Password, installDir, langOf(r, payload.Lang))
}

func (a *app) handleGamesList(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	if a.crawler == nil {
		writeJSON(w, http.StatusOK, []backend.GameCrawlItem{})
		return
	}
	writeJSON(w, http.StatusOK, a.crawler.GetGames())
}

func (a *app) handleGamesSync(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	if a.crawler != nil {
		a.crawler.StartSync()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "syncing"})
}

func (a *app) handleGamesSyncStatus(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	status, progress, lastErr := "idle", "", ""
	if a.crawler != nil {
		status, progress, lastErr = a.crawler.GetStatus()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status, "progress": progress, "error": lastErr})
}

// GET /api/games/icon/{scriptName} (public, icons are cached files from linuxgsm.com)
func (a *app) handleGameIcon(w http.ResponseWriter, r *http.Request) {
	scriptName := strings.TrimPrefix(r.URL.Path, "/api/games/icon/")
	if !isValidGameCmd(scriptName) {
		http.NotFound(w, r)
		return
	}

	iconsDir := filepath.Join(a.configDir, "icons")
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp"} {
		iconFile := filepath.Join(iconsDir, scriptName+ext)
		info, err := os.Lstat(iconFile)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
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
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#7c4dff" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" style="width:100%;height:100%;"><rect x="2" y="2" width="20" height="20" rx="2" ry="2"></rect><path d="M6 12h12M12 6v12"></path></svg>`))
}

// -------------------------------------------------------------
// Administration
// -------------------------------------------------------------

func validNodeList(ids []string) bool {
	if len(ids) > 200 {
		return false
	}
	for _, id := range ids {
		if !backend.IsValidNodeID(id) {
			return false
		}
	}
	return true
}

func validServerList(ids []string) bool {
	if len(ids) > 1000 {
		return false
	}
	for _, id := range ids {
		if !backend.IsValidServerID(id) {
			return false
		}
	}
	return true
}

func (a *app) handleUsers(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	ip := a.clientIP(r)
	switch r.Method {
	case http.MethodGet:
		users := a.auth.GetUsers()
		for i := range users {
			users[i] = users[i].Sanitized()
		}
		writeJSON(w, http.StatusOK, users)

	case http.MethodPost, http.MethodPut:
		var payload struct {
			Username    string   `json:"username"`
			Password    string   `json:"password"`
			Role        string   `json:"role"`
			Servers     []string `json:"servers"`
			Permissions []string `json:"permissions"`
			// Nodes whose servers the user may access; nil keeps the current assignment.
			Nodes *[]string `json:"nodes"`
		}
		if !decodeJSON(w, r, &payload, 64*1024) {
			return
		}
		if payload.Username == "" || (r.Method == http.MethodPost && payload.Password == "") || len(payload.Password) > 1024 {
			writeError(w, http.StatusBadRequest, "Invalid payload")
			return
		}
		if r.Method == http.MethodPost && !isValidUsername(payload.Username) {
			writeError(w, http.StatusBadRequest, "Invalid username format")
			return
		}
		if payload.Nodes != nil && !validNodeList(*payload.Nodes) {
			writeError(w, http.StatusBadRequest, "Invalid node list")
			return
		}
		if !validServerList(payload.Servers) {
			writeError(w, http.StatusBadRequest, "Invalid server list")
			return
		}
		var err error
		if r.Method == http.MethodPost {
			err = a.auth.CreateUser(payload.Username, payload.Password, payload.Role, payload.Servers, payload.Permissions)
		} else {
			err = a.auth.UpdateUser(payload.Username, payload.Password, payload.Role, payload.Servers, payload.Permissions)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if payload.Nodes != nil {
			if err := a.auth.SetUserNodes(payload.Username, *payload.Nodes); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		action := "USER_CREATE"
		if r.Method == http.MethodPut {
			action = "USER_UPDATE"
		}
		a.auth.LogAudit(admin.Username, ip, action, fmt.Sprintf("%s (role %s)", payload.Username, payload.Role), "")
		writeOK(w)

	case http.MethodDelete:
		username := r.URL.Query().Get("username")
		if username == "" {
			writeError(w, http.StatusBadRequest, "Missing username parameter")
			return
		}
		if err := a.auth.DeleteUser(username); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.auth.LogAudit(admin.Username, ip, "USER_DELETE", username, "")
		writeOK(w)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET/PUT /api/admin/server-paths: additional directories scanned for LinuxGSM servers.
// handleTimeZone reads or changes the time zone of the dashboard's own scheduler (Docker mode).
func (a *app) handleTimeZone(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	if _, ok := a.inst.ScheduleTimeZone(); !ok {
		writeError(w, http.StatusNotImplemented, "The schedule time zone is set by the operating system in this runtime")
		return
	}
	switch r.Method {
	case http.MethodGet:
		info, _ := a.inst.ScheduleTimeZone()
		writeJSON(w, http.StatusOK, info)
	case http.MethodPut, http.MethodPost:
		var payload struct {
			TimeZone string `json:"timezone"`
		}
		if !decodeJSON(w, r, &payload, 4*1024) {
			return
		}
		if err := a.inst.SetScheduleTimeZone(payload.TimeZone); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		info, _ := a.inst.ScheduleTimeZone()
		a.auth.LogAudit(admin.Username, a.clientIP(r), "SCHEDULE_TIMEZONE", "Schedule time zone set to "+info.Effective, "")
		writeJSON(w, http.StatusOK, info)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// -------------------------------------------------------------
// Nodes (multi-node)
// -------------------------------------------------------------

// GET /agent/connect: WebSocket carrier of agent tunnels. 404 until the first node is added.
func (a *app) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	gw, err := a.agents.Gateway(false)
	if err != nil || gw == nil {
		http.NotFound(w, r)
		return
	}
	gw.HandleWebSocket(w, r, a.clientIP(r))
}

type nodeView struct {
	backend.Node
	backend.NodeStatus
	Update backend.NodeUpdate `json:"update"`
}

// GET /api/admin/nodes lists the nodes; POST creates a pairing command for a new node (name,
// mode) or for re-pairing an existing one (repair_id).
func (a *app) handleNodes(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	switch r.Method {
	case http.MethodGet:
		nodes := a.agents.Nodes.Nodes()
		views := make([]nodeView, 0, len(nodes))
		for _, n := range nodes {
			n.Keys, n.MachineID = nil, ""
			st := a.agents.Hub.Status(n.ID)
			if st.Hello != nil {
				hello := *st.Hello
				hello.MachineID = ""
				st.Hello = &hello
			}
			views = append(views, nodeView{Node: n, NodeStatus: st, Update: backend.NodeUpdateInfo(Version, a.agents.Hub.Status(n.ID))})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"nodes": views, "agent_port": a.agentPort})

	case http.MethodPost:
		var payload struct {
			Name     string `json:"name"`
			Mode     string `json:"mode"`
			RepairID string `json:"repair_id"`
		}
		if !decodeJSON(w, r, &payload, 4*1024) {
			return
		}
		if payload.RepairID != "" && !backend.IsValidNodeID(payload.RepairID) {
			writeError(w, http.StatusBadRequest, "Invalid node")
			return
		}
		gw, err := a.agents.Gateway(true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to set up the agent CA")
			return
		}
		ticket, err := a.agents.Nodes.CreatePairing(payload.Name, payload.Mode, payload.RepairID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		action, details := "NODE_PAIRING", fmt.Sprintf("Created pairing command for node %s (%s)", ticket.NodeID, ticket.Mode)
		if payload.RepairID != "" {
			action, details = "NODE_REPAIR", fmt.Sprintf("Created re-pairing command for node %s (%s)", ticket.NodeID, ticket.Mode)
		}
		a.auth.LogAudit(admin.Username, a.clientIP(r), action, details, "")

		fingerprint := gw.CA.Fingerprint()
		scheme := "http"
		if a.isHTTPS(r) {
			scheme = "https"
		}
		command := func(url string) string {
			return fmt.Sprintf("/opt/lgsm-dashboard/lgsm-dashboard agent join %s --token %s --ca-fingerprint %s --mode %s",
				url, ticket.Token, fingerprint, ticket.Mode)
		}
		resp := map[string]interface{}{
			"ticket":         ticket,
			"ca_fingerprint": fingerprint,
			"command":        command(scheme + "://" + r.Host),
		}
		if host, _, err := net.SplitHostPort(r.Host); a.agentPort > 0 {
			if err != nil {
				host = r.Host
			}
			resp["command_direct"] = command(fmt.Sprintf("tls://%s", net.JoinHostPort(host, strconv.Itoa(a.agentPort))))
		}
		writeJSON(w, http.StatusOK, resp)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// POST /api/admin/nodes/update changes name and access mode of a node.
func (a *app) handleNodeUpdate(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	var payload struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	if !backend.IsValidNodeID(payload.ID) {
		writeError(w, http.StatusBadRequest, "Invalid node")
		return
	}
	if err := a.agents.Nodes.UpdateNode(payload.ID, payload.Name, payload.Mode); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.auth.LogAudit(admin.Username, a.clientIP(r), "NODE_UPDATE", fmt.Sprintf("Updated node %s (%s)", payload.ID, payload.Mode), "")
	writeOK(w)
}

// POST /api/admin/nodes/upgrade updates a node to the dashboard's release. The node downloads and
// verifies the release itself (same signature check as a dashboard update) and restarts.
func (a *app) handleNodeUpgrade(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	var payload struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	if !backend.IsValidNodeID(payload.ID) {
		writeError(w, http.StatusBadRequest, "Invalid node")
		return
	}
	info := backend.NodeUpdateInfo(Version, a.agents.Hub.Status(payload.ID))
	client, ok := a.agents.Hub.Tunnel(payload.ID)
	if !info.Possible || !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "The node cannot be updated from the dashboard", "reason": info.Reason})
		return
	}
	backend.DisableWriteDeadline(w)
	body, _ := json.Marshal(map[string]string{"tag_name": info.Target})
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/admin/update/trigger", bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(backend.AgentActorHeader, admin.Username)
	req.Header.Set(backend.AgentActorIPHeader, a.clientIP(r))
	a.auth.LogAudit(admin.Username, a.clientIP(r), "NODE_UPGRADE", fmt.Sprintf("Updating node %s to %s", payload.ID, info.Target), "")
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "The node did not answer")
		return
	}
	defer resp.Body.Close()
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&answer)
	if resp.StatusCode != http.StatusOK {
		msg := plainText(answer.Error, 300)
		if msg == "" {
			msg = resp.Status
		}
		writeError(w, http.StatusBadGateway, "The node could not update: "+msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "target": info.Target})
}

// POST /api/admin/nodes/delete removes a node; its tunnel is closed and its key stops working.
func (a *app) handleNodeDelete(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	var payload struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	if !backend.IsValidNodeID(payload.ID) {
		writeError(w, http.StatusBadRequest, "Invalid node")
		return
	}
	if err := a.agents.RemoveNode(payload.ID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	// A node paired later under the same ID must not inherit assignments or tags.
	if err := a.auth.RemoveNodeFromUsers(payload.ID); err != nil {
		fmt.Printf("[WARNING] Failed to remove node %s from users: %v\n", payload.ID, err)
	}
	if err := a.inst.RemoveNodeServerTags(payload.ID); err != nil {
		fmt.Printf("[WARNING] Failed to remove tags of node %s: %v\n", payload.ID, err)
	}
	a.auth.LogAudit(admin.Username, a.clientIP(r), "NODE_DELETE", "Removed node "+payload.ID, "")
	writeOK(w)
}

// -------------------------------------------------------------
// Agent mode
// -------------------------------------------------------------

// policy returns the access mode and local restrictions of this node (agent mode).
func (a *app) policy() backend.AgentPolicy {
	if p := a.agentPolicy.Load(); p != nil {
		return *p
	}
	return backend.AgentPolicy{Mode: backend.AgentModeReadOnly}
}

// agentAllows checks an access class against the node's access mode and answers 403 if it is
// not allowed.
func (a *app) agentAllows(w http.ResponseWriter, class string) bool {
	if backend.AgentModeAllows(a.policy().Mode, class) {
		return true
	}
	writeError(w, http.StatusForbidden, "This node is read-only (start, stop and restart are allowed)")
	return false
}

// agentAllowsServer applies the node's server list; servers outside of it do not exist for the
// controller.
func (a *app) agentAllowsServer(w http.ResponseWriter, serverID string) bool {
	if allowed := a.policy().Servers; len(allowed) == 0 || slices.Contains(allowed, serverID) {
		return true
	}
	writeError(w, http.StatusNotFound, "Server not found")
	return false
}

// Access classes of agent routes that are not decided per method.
const (
	accessInHandler   = "in-handler"  // server routes and bulk: decided per route or action
	accessSelfUpdate  = "self-update" // allowed by allow_self_update in every access mode
	accessMaintenance = "maintenance" // keeping the tunnel working (certificate renewal), every mode
)

// agentRoute is one endpoint of the agent API. access maps each allowed method to its access
// class; other methods are refused.
type agentRoute struct {
	pattern string
	access  map[string]string
	handler http.HandlerFunc
}

// agentRouteTable lists everything an agent serves. Users, logins, 2FA, the UI and the game
// list exist only on the controller.
func (a *app) agentRouteTable() []agentRoute {
	read := map[string]string{http.MethodGet: backend.AccessRead}
	readWrite := map[string]string{http.MethodGet: backend.AccessRead, http.MethodPut: backend.AccessWrite, http.MethodPost: backend.AccessWrite}
	return []agentRoute{
		{backend.AgentHelloPath, read, a.handleAgentHello},
		{backend.AgentCSRPath, map[string]string{http.MethodPost: accessMaintenance}, a.handleAgentCSR},
		{backend.AgentCertPath, map[string]string{http.MethodPost: accessMaintenance}, a.handleAgentCert},
		{"/api/servers", read, a.requireAuth(a.handleListServers)},
		{"/api/servers/bulk", map[string]string{http.MethodPost: accessInHandler}, a.requireAuth(a.handleBulk)},
		{"/api/servers/", map[string]string{http.MethodGet: accessInHandler, http.MethodPost: accessInHandler}, a.requireAuth(a.handleServer)},
		{"/api/system/stats", read, a.requireAuth(a.handleStats)},
		{"/api/system/info", read, a.requireAuth(a.handleInfo)},
		{"/api/system/firewall", read, a.requireAdmin(a.handleFirewallStatus)},
		{"/api/system/firewall/open", map[string]string{http.MethodPost: backend.AccessWrite}, a.agentFirewallAllowed(a.requireAdmin(a.handleFirewallOpen))},
		{"/api/games/install", map[string]string{http.MethodPost: backend.AccessWrite}, a.requireAdmin(a.handleInstall)},
		{"/api/admin/server-paths", readWrite, a.requireAdmin(a.handleServerPaths)},
		{"/api/admin/timezone", readWrite, a.requireAdmin(a.handleTimeZone)},
		{"/api/admin/update/check", read, a.requireAdmin(a.handleUpdateCheck)},
		{"/api/admin/update/trigger", map[string]string{http.MethodPost: accessSelfUpdate}, a.requireAdmin(a.handleUpdateTrigger)},
		{"/fastdl/", map[string]string{http.MethodGet: backend.AccessRead, http.MethodHead: backend.AccessRead}, a.handleFastDL},
	}
}

func (a *app) agentFirewallAllowed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.policy().AllowFirewall {
			writeError(w, http.StatusForbidden, "Opening firewall ports is disabled on this node (allow_firewall in agent-policy.json)")
			return
		}
		next(w, r)
	}
}

// agentRoutes is the handler an agent serves on its tunnel.
func (a *app) agentRoutes() http.Handler {
	mux := http.NewServeMux()
	for _, route := range a.agentRouteTable() {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			class, ok := route.access[r.Method]
			if !ok {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			switch class {
			case accessInHandler, accessMaintenance:
			case accessSelfUpdate:
				if !a.policy().AllowSelfUpdate {
					writeError(w, http.StatusForbidden, "Updates from the dashboard are disabled on this node (allow_self_update in agent-policy.json)")
					return
				}
			default:
				if !a.agentAllows(w, class) {
					return
				}
			}
			route.handler(w, r)
		})
	}
	return deadlines(mux)
}

// POST /agent/v1/csr prepares a new node key and answers with a certificate request for it.
func (a *app) handleAgentCSR(w http.ResponseWriter, r *http.Request) {
	csr, err := backend.AgentPrepareRenewal(a.configDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string][]byte{"csr": csr})
}

// POST /agent/v1/cert installs the renewed certificate; it is used from the next connection on.
func (a *app) handleAgentCert(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Cert []byte `json:"cert"`
	}
	if !decodeJSON(w, r, &payload, 64*1024) {
		return
	}
	if err := backend.AgentInstallRenewal(a.configDir, payload.Cert); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fmt.Println("[AGENT] Node certificate renewed")
	writeOK(w)
}

// GET /agent/v1/hello describes this node to the controller.
func (a *app) handleAgentHello(w http.ResponseWriter, r *http.Request) {
	p := a.policy()
	hostname, _ := os.Hostname()
	servers := p.Servers
	if servers == nil {
		servers = []string{}
	}
	writeJSON(w, http.StatusOK, backend.AgentHello{
		Version:         Version,
		Protocol:        backend.AgentProtocolVersion,
		Hostname:        hostname,
		MachineID:       backend.MachineID(),
		Capabilities:    a.inst.Capabilities(),
		Mode:            p.Mode,
		AllowFirewall:   p.AllowFirewall,
		AllowSelfUpdate: p.AllowSelfUpdate,
		FastDLURL:       a.fastdlURL,
		Servers:         servers,
	})
}

// runAgent is `lgsm-dashboard agent run`: the node's game servers are managed with the same
// runtime as a dashboard, but served only to the controller through the tunnel.
// fastdlPort > 0 also serves FastDL of servers in mode "node" on that port; fastdlURL is the
// public base URL of that FastDL (or of a web server the node owner runs for it).
func runAgent(configDir string, maxUploadBytes int64, fastdlPort int, fastdlURL string) int {
	if _, err := backend.LoadAgentIdentity(configDir); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
		return 1
	}
	rt, err := backend.SelectRuntime(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[FATAL] %v\n", err)
		return 1
	}
	fmt.Printf("[SYS] Runtime: %s\n", rt.Name())
	caps := rt.Capabilities()
	audit, err := backend.NewAuditOnlyAuthManager(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[FATAL] %v\n", err)
		return 1
	}
	instances := backend.NewInstanceManager(configDir, rt)
	metrics := backend.NewSystemMetricsCollector()
	metrics.ProcessStats = instances.ProcessStats
	var updater *backend.Updater
	if caps.SelfUpdate {
		updater = &backend.Updater{Repo: releaseRepo, PublicKey: releasePublicKey, CurrentVersion: Version}
	}
	a := &app{
		auth:           audit,
		inst:           instances,
		metrics:        metrics,
		updater:        updater,
		configDir:      configDir,
		maxUploadBytes: maxUploadBytes,
		agentMode:      true,
		fastdlURL:      strings.TrimSpace(fastdlURL),
	}
	load := func() (backend.AgentIdentity, error) {
		// The policy is re-read for every connection, so `agent mode` applies on reconnect.
		policy := backend.LoadAgentPolicy(configDir)
		a.agentPolicy.Store(&policy)
		return backend.LoadAgentIdentity(configDir)
	}

	if fastdlPort > 0 {
		fastdl := &http.Server{
			Addr:    fmt.Sprintf(":%d", fastdlPort),
			Handler: http.HandlerFunc(a.serveNodeFastDL),
			// Public port: requests are tiny, so slow clients cannot hold a request open.
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    64 << 10,
		}
		l, err := net.Listen("tcp", fastdl.Addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] FastDL port: %v\n", err)
			return 1
		}
		go func() {
			// Public port: at most 128 connections at a time, so it cannot exhaust the node.
			if err := fastdl.Serve(backend.LimitListener(l, 128)); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "[ERROR] FastDL port: %v\n", err)
			}
		}()
		fmt.Printf("[SYS] Serving FastDL of servers in mode \"node\" on port %d\n", fastdlPort)
		if a.fastdlURL == "" {
			fmt.Println("[SYS] Set --fastdl-url to the public address of that port, so the dashboard can show it")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = backend.RunAgent(ctx, load, a.agentRoutes(), func(format string, args ...interface{}) {
		fmt.Printf(format+"\n", args...)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
		return 1
	}
	return 0
}

// runAgentCommand implements `lgsm-dashboard agent <join|run|status|mode>`.
func runAgentCommand(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  lgsm-dashboard agent join <dashboard-url> --token <token> --ca-fingerprint <sha256:...> --mode read-only|read-write [--insecure-proxy-tls] [--config-dir <dir>]")
		fmt.Fprintln(os.Stderr, "  lgsm-dashboard agent run [--config-dir <dir>] [--max-upload-mb <MiB>] [--fastdl-port <port>] [--fastdl-url <url>]")
		fmt.Fprintln(os.Stderr, "  lgsm-dashboard agent status [--config-dir <dir>]")
		fmt.Fprintln(os.Stderr, "  lgsm-dashboard agent mode read-only|read-write [--config-dir <dir>]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	cmd, args := args[0], args[1:]
	// Positional arguments may come before the flags (`agent join <url> --token ...`).
	var positional []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positional = append(positional, args[0])
		args = args[1:]
	}
	fset := flag.NewFlagSet("agent "+cmd, flag.ContinueOnError)
	configDir := fset.String("config-dir", "./config", "Directory of the agent configuration")

	switch cmd {
	case "join":
		token := fset.String("token", "", "Pairing token from the dashboard")
		fingerprint := fset.String("ca-fingerprint", "", "CA fingerprint from the dashboard")
		mode := fset.String("mode", "", "Access mode: read-only or read-write")
		insecure := fset.Bool("insecure-proxy-tls", false, "Do not verify the certificate of a reverse proxy in front of the dashboard (the connection to the dashboard itself is always verified)")
		if err := fset.Parse(args); err != nil {
			return 2
		}
		positional = append(positional, fset.Args()...)
		if len(positional) != 1 || *token == "" || *fingerprint == "" || *mode == "" {
			return usage()
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cfg, err := backend.AgentJoin(ctx, *configDir, backend.AgentJoinOptions{
			ControllerURL:    positional[0],
			Token:            *token,
			CAFingerprint:    *fingerprint,
			Mode:             *mode,
			Version:          Version,
			InsecureProxyTLS: *insecure,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] Pairing failed: %v\n", err)
			return 1
		}
		policy := backend.LoadAgentPolicy(*configDir)
		fmt.Printf("[OK] Paired as node %s (%s), access mode %s.\n", cfg.NodeID, cfg.Name, policy.Mode)
		return 0

	case "run":
		maxUploadMB := fset.Int64("max-upload-mb", 4096, "Maximum size of backup and file manager uploads in MiB")
		fastdlPort := fset.Int("fastdl-port", 0, "Serve FastDL of servers in mode node on this port (0: off)")
		fastdlURL := fset.String("fastdl-url", "", "Public base URL of the node's FastDL, e.g. http://203.0.113.5:8090/fastdl/")
		if err := fset.Parse(args); err != nil || len(positional)+fset.NArg() > 0 {
			return usage()
		}
		if *maxUploadMB <= 0 {
			*maxUploadMB = 4096
		}
		if *fastdlPort < 0 || *fastdlPort > 65535 {
			return usage()
		}
		return runAgent(*configDir, *maxUploadMB<<20, *fastdlPort, *fastdlURL)

	case "status":
		if err := fset.Parse(args); err != nil || len(positional)+fset.NArg() > 0 {
			return usage()
		}
		id, err := backend.LoadAgentIdentity(*configDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
			return 1
		}
		policy := backend.LoadAgentPolicy(*configDir)
		fmt.Printf("Node:         %s (%s)\n", id.Config.NodeID, id.Config.Name)
		fmt.Printf("Dashboard:    %s\n", id.Config.ControllerURL)
		fmt.Printf("Access mode:  %s\n", policy.Mode)
		fmt.Printf("Certificate:  valid until %s\n", id.Leaf.NotAfter.Local().Format("2006-01-02 15:04"))
		return 0

	case "mode":
		if err := fset.Parse(args); err != nil {
			return 2
		}
		positional = append(positional, fset.Args()...)
		if len(positional) != 1 || !backend.IsValidAgentMode(positional[0]) {
			return usage()
		}
		policy := backend.LoadAgentPolicy(*configDir)
		policy.Mode = positional[0]
		if err := backend.SaveAgentPolicy(*configDir, policy); err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
			return 1
		}
		fmt.Printf("[OK] Access mode set to %s. It applies from the next connection; the dashboard may restrict it further.\n", policy.Mode)
		return 0

	default:
		return usage()
	}
}

func (a *app) handleServerPaths(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string][]string{"paths": a.inst.GetCustomServerPaths()})
	case http.MethodPut, http.MethodPost:
		var payload struct {
			Paths []string `json:"paths"`
		}
		if !decodeJSON(w, r, &payload, 64*1024) {
			return
		}
		paths, err := a.inst.SetCustomServerPaths(payload.Paths)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.auth.LogAudit(admin.Username, a.clientIP(r), "SETTINGS_SERVER_PATHS", strings.Join(paths, ", "), "")
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "paths": paths})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) handleUpdateCheck(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	if a.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "Updates are not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	st := a.updater.Check(ctx)
	if st.Error != "" && st.Latest == "" {
		fmt.Printf("[WARNING] update check failed: %s\n", st.Error)
		writeError(w, http.StatusBadGateway, "Failed to query the latest release")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *app) handleUpdateTrigger(w http.ResponseWriter, r *http.Request, admin backend.User, _ string) {
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if !decodeJSON(w, r, &payload, 16*1024) {
		return
	}
	if !backend.IsStableReleaseTag(payload.TagName) {
		writeError(w, http.StatusBadRequest, "Invalid release tag")
		return
	}
	if a.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "Updates are not available")
		return
	}
	backend.DisableWriteDeadline(w)

	execPath, err := os.Executable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Cannot locate the dashboard binary")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := a.updater.Install(ctx, payload.TagName, execPath); err != nil {
		fmt.Printf("[ERROR] self-update failed: %v\n", err)
		a.auth.LogAudit(admin.Username, a.clientIP(r), "DASHBOARD_UPDATE_FAILED", err.Error(), "")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.auth.LogAudit(admin.Username, a.clientIP(r), "DASHBOARD_UPDATE", "Updated dashboard to "+payload.TagName, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Dashboard updated successfully. Restarting..."})

	go func() {
		time.Sleep(1 * time.Second)
		fmt.Println("[SYS] Exiting for restart after self-update...")
		os.Exit(0)
	}()
}

// -------------------------------------------------------------
// FastDL (public)
// -------------------------------------------------------------

// FastDL Web Server endpoint: /fastdl/{serverID}/... -> <server dir>/public_html/fastdl/...
func (a *app) handleFastDL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/fastdl/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || !backend.IsValidServerID(parts[0]) || !isCanonicalFastDLPath(parts[1]) {
		http.NotFound(w, r)
		return
	}
	if node, local := backend.SplitServerID(parts[0]); node != "" {
		if a.agentMode {
			http.NotFound(w, r)
			return
		}
		a.forwardFastDL(w, r, node, local, parts[1])
		return
	}
	// Through the dashboard (or a node's tunnel) only in mode "dashboard"; a node's own FastDL
	// port serves the servers in mode "node" (serveNodeFastDL).
	if a.inst.FastDLMode(parts[0]) != backend.FastDLDashboard || a.agentMode && !a.policyAllowsServer(parts[0]) {
		http.NotFound(w, r)
		return
	}
	a.serveFastDLFile(w, r, parts[0], parts[1])
}

// forwardFastDL serves the FastDL files of a node's server through its tunnel. FastDL is public;
// the node itself only answers for servers in mode "dashboard".
func (a *app) forwardFastDL(w http.ResponseWriter, r *http.Request, nodeID, localID, rel string) {
	if a.agents == nil || !backend.IsValidNodeID(nodeID) {
		http.NotFound(w, r)
		return
	}
	client, ok := a.agents.Hub.Tunnel(nodeID)
	st := a.agents.Hub.Status(nodeID)
	if !ok || !st.Online || !st.Compatible {
		http.NotFound(w, r)
		return
	}
	release, ok := a.agents.Hub.TryFastDLSlot(nodeID)
	if !ok {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Too many downloads, try again shortly", http.StatusServiceUnavailable)
		return
	}
	defer release()
	a.forwardToNode(w, r, client, backend.User{Username: "fastdl"}, "/fastdl/"+localID+"/"+rel)
}

// serveNodeFastDL is the handler of an agent's own FastDL port (agent run --fastdl-port): only
// static files of servers in mode "node", nothing else of the agent API.
func (a *app) serveNodeFastDL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", sandboxCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/fastdl/"), "/", 2)
	if !strings.HasPrefix(r.URL.Path, "/fastdl/") || len(parts) < 2 || !isCanonicalFastDLPath(parts[1]) ||
		!backend.IsValidLocalServerID(parts[0]) || !a.policyAllowsServer(parts[0]) ||
		a.inst.FastDLMode(parts[0]) != backend.FastDLNode {
		http.NotFound(w, r)
		return
	}
	a.serveFastDLFile(w, r, parts[0], parts[1])
}

// isCanonicalFastDLPath accepts a relative file path without empty, "." or ".." segments,
// backslashes or control characters. FastDL requests are forwarded to nodes, which serve them
// with full rights, so a path must never be able to climb into the agent's API.
func isCanonicalFastDLPath(rel string) bool {
	if rel == "" || len(rel) > 1024 || strings.ContainsAny(rel, "\\\x00") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, c := range seg {
			if c < 0x20 || c == 0x7f {
				return false
			}
		}
	}
	return true
}

// policyAllowsServer applies the node's server list (all servers when it lists none).
func (a *app) policyAllowsServer(serverID string) bool {
	allowed := a.policy().Servers
	return len(allowed) == 0 || slices.Contains(allowed, serverID)
}

// serveFastDLFile sends one file below public_html/fastdl of a server of this machine.
func (a *app) serveFastDLFile(w http.ResponseWriter, r *http.Request, serverID, rel string) {
	target, err := a.inst.ResolveFastDLFile(serverID, rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := backend.OpenInRootForRead(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	backend.DisableWriteDeadline(w)
	// Never let user controlled content render (and run scripts) in the dashboard origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentDisposition(info.Name()))
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
