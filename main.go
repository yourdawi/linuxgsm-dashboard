package main

// NOTE: all package main code lives in this single file on purpose. Dashboard versions up to
// 1.0.7 self-update with `go build main.go`, which only compiles this file.

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	// CLI Flags
	portFlag := flag.Int("port", 0, "Port to run the dashboard on (overrides config)")
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
	rt, err := backend.SelectRuntime()
	if err != nil {
		fmt.Printf("[FATAL] %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[SYS] Runtime: %s\n", rt.Name())

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

	updater := &backend.Updater{
		Repo:           releaseRepo,
		PublicKey:      releasePublicKey,
		CurrentVersion: Version,
	}
	updater.StartBackgroundCheck(updateCheckInterval)

	instances := backend.NewInstanceManager(*configDirFlag, rt)
	metrics := backend.NewSystemMetricsCollector()
	metrics.ProcessStats = instances.ProcessStats

	application := &app{
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

	secureCookies  bool  // always set the Secure flag on cookies
	trustProxy     bool  // honour X-Forwarded-For / X-Forwarded-Proto from a reverse proxy
	maxUploadBytes int64 // upload limit for backups and file manager uploads
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
	if a.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
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

// currentUser resolves the logged in user from the session cookie.
func (a *app) currentUser(r *http.Request) (backend.User, string, error) {
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
	mux.HandleFunc("/api/admin/update/check", methodOnly(http.MethodGet, a.requireAdmin(a.handleUpdateCheck)))
	mux.HandleFunc("/api/admin/update/trigger", methodOnly(http.MethodPost, a.requireAdmin(a.handleUpdateTrigger)))

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
	if ok, wait := a.limiter.Allow(keys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Too many failed login attempts. Please try again later.")
		return
	}

	result, err := a.auth.BeginLogin(creds.Username, creds.Password)
	if err != nil {
		a.limiter.Failure(keys...)
		a.auth.LogAudit(creds.Username, ip, "LOGIN_FAILED", "Invalid username or password", "")
		writeError(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}
	a.limiter.Success(keys...)

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
	if ok, wait := a.limiter.Allow(keys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Please try again later.")
		return
	}
	sessionID, usedRecovery, err := a.auth.CompleteLogin(payload.Challenge, payload.Code)
	if err != nil {
		a.limiter.Failure(keys...)
		a.auth.LogAudit(username, ip, "LOGIN_2FA_FAILED", "Invalid two-factor code", "")
		if errors.Is(err, backend.ErrChallengeExpired) {
			writeError(w, http.StatusUnauthorized, "Login expired, please sign in again")
			return
		}
		writeError(w, http.StatusUnauthorized, "Invalid code")
		return
	}
	a.limiter.Success(keys...)
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

func (a *app) handleTwoFactorSetup(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
	var payload struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &payload, 4*1024) {
		return
	}
	setup, err := a.auth.StartTwoFactorSetup(user.Username, payload.Password, requestHostName(r))
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
	if err := a.auth.DisableTwoFactor(user.Username, payload.Password, payload.Code, sessionID); err != nil {
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
	codes, err := a.auth.RegenerateRecoveryCodes(user.Username, payload.Password, payload.Code)
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
	if err := a.auth.ChangeUserPassword(user.Username, payload.OldPassword, payload.NewPassword, sessionID); err != nil {
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
	writeJSON(w, http.StatusOK, backend.GetFirewallStatus())
}

func (a *app) handleFirewallOpen(w http.ResponseWriter, r *http.Request, user backend.User, _ string) {
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

	// Authorize EVERY server before anything is executed; one denied server rejects the request.
	seen := make(map[string]bool)
	ids := make([]string, 0, len(payload.ServerIDs))
	for _, id := range payload.ServerIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := backend.AuthorizeServerAction(&user, id, perm, a.inst.ServerExists); err != nil {
			writeAuthzError(w, err)
			return
		}
		ids = append(ids, id)
	}

	ip := a.clientIP(r)
	for _, id := range ids {
		a.auth.LogAudit(user.Username, ip, "BULK_"+strings.ToUpper(payload.Action), fmt.Sprintf("Executed bulk action %s", payload.Action), id)
	}
	writeJSON(w, http.StatusOK, a.inst.RunBulkAction(payload.Action, ids))
}

// -------------------------------------------------------------
// Server scoped routes: /api/servers/{id}/{route}[/{sub}]
// -------------------------------------------------------------

// permByAction marks routes whose permission depends on the requested LinuxGSM action.
const permByAction = "by-action"

// serverRoutes is the central RBAC policy for server scoped endpoints:
// "<route>[/<sub>] <METHOD>" -> required permission.
var serverRoutes = map[string]string{
	"delete POST":               backend.PermAdmin,
	"portcheck GET":             backend.PermView,
	"action POST":               permByAction,
	"console GET":               backend.PermConsole,
	"console/send POST":         backend.PermConsole,
	"rcon POST":                 backend.PermConsole,
	"configs GET":               backend.PermConfig,
	"configs/file GET":          backend.PermConfig,
	"configs/file POST":         backend.PermConfig,
	"tag POST":                  backend.PermAdmin,
	"cron GET":                  backend.PermView,
	"cron POST":                 backend.PermSchedule,
	"cron/install POST":         backend.PermAdmin,
	"backups GET":               backend.PermBackup,
	"backups/delete POST":       backend.PermBackup,
	"backups/upload POST":       backend.PermBackup,
	"backups/download GET":      backend.PermBackup,
	"backups/restore POST":      backend.PermBackup,
	"backups/settings GET":      backend.PermBackup,
	"backups/settings POST":     backend.PermBackup,
	"alerts GET":                backend.PermAlerts,
	"alerts POST":               backend.PermAlerts,
	"systemd/install POST":      backend.PermAdmin,
	"files GET":                 backend.PermFiles,
	"files POST":                backend.PermFiles,
	"files/download GET":        backend.PermFiles,
	"files/content GET":         backend.PermFiles,
	"files/upload POST":         backend.PermFiles,
	"mods GET":                  backend.PermView,
	"stopmode GET":              backend.PermView,
	"stopmode POST":             backend.PermAdmin,
	"clone POST":                backend.PermAdmin,
	"cloudbackup/settings GET":  backend.PermBackup,
	"cloudbackup/settings POST": backend.PermAdmin,
	"cloudbackup/sync POST":     backend.PermBackup,
}

// serverRoutePermission looks up the policy. status is 404 for unknown routes and 405 when the
// route exists with a different method.
func serverRoutePermission(route, method string) (perm string, status int) {
	if p, ok := serverRoutes[route+" "+method]; ok {
		return p, http.StatusOK
	}
	for key := range serverRoutes {
		if strings.HasPrefix(key, route+" ") {
			return "", http.StatusMethodNotAllowed
		}
	}
	return "", http.StatusNotFound
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

	perm, status := serverRoutePermission(route, r.Method)
	if status != http.StatusOK {
		if status == http.StatusMethodNotAllowed {
			http.Error(w, "Method not allowed", status)
		} else {
			http.NotFound(w, r)
		}
		return
	}

	authPerm := perm
	if perm == permByAction {
		authPerm = backend.PermView // refined below once the action is known
	}
	if err := backend.AuthorizeServerAction(&user, serverID, authPerm, a.inst.ServerExists); err != nil {
		writeAuthzError(w, err)
		return
	}

	ctx := serverCtx{app: a, w: w, r: r, user: user, serverID: serverID, ip: a.clientIP(r)}
	switch route {
	case "delete":
		a.auth.LogAudit(user.Username, ctx.ip, "SERVER_DELETE", "Deleted server", serverID)
		a.inst.DeleteServer(w, r, serverID)
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
	case "stopmode":
		ctx.stopMode()
	case "clone":
		ctx.clone()
	case "cloudbackup/settings":
		ctx.cloudSettings()
	case "cloudbackup/sync":
		a.auth.LogAudit(user.Username, ctx.ip, "CLOUDBACKUP_SYNC", "Triggered cloud backup sync", serverID)
		a.inst.SyncCloudBackup(w, r, serverID, langOf(r, ""))
	default:
		http.NotFound(w, r)
	}
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
		Action string `json:"action"`
		Lang   string `json:"lang"`
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
	c.audit("ACTION_"+strings.ToUpper(payload.Action), fmt.Sprintf("Executed action %s", payload.Action))
	c.app.inst.RunAction(c.w, c.r, c.serverID, payload.Action, langOf(c.r, payload.Lang))
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

func (c serverCtx) listConfigs() {
	configs, err := c.app.inst.GetConfigFiles(c.serverID)
	if err != nil {
		writeError(c.w, http.StatusInternalServerError, err.Error())
		return
	}
	if configs == nil {
		configs = []backend.ConfigFile{}
	}
	writeJSON(c.w, http.StatusOK, configs)
}

func (c serverCtx) configFile() {
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
	if err := c.app.inst.SaveConfigFileContent(c.serverID, payload.Path, payload.Content); err != nil {
		writeError(c.w, http.StatusForbidden, err.Error())
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

// isBodyTooLarge detects the error of http.MaxBytesReader. The message is matched instead of the
// http.MaxBytesError type, which only exists since Go 1.19 (distribution Go may be older).
func isBodyTooLarge(err error) bool {
	return err != nil && strings.Contains(err.Error(), "http: request body too large")
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
	var payload backend.AlertSettings
	if !decodeJSON(c.w, c.r, &payload, 64*1024) {
		return
	}
	if err := c.app.inst.SaveAlertSettings(c.serverID, payload); err != nil {
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
	if err := backend.ValidateInstallDir(installDir); err != nil {
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
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" || !backend.IsValidServerID(parts[0]) {
		http.NotFound(w, r)
		return
	}
	target, err := a.inst.ResolveFastDLFile(parts[0], parts[1])
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
