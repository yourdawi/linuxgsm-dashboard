package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yourdawi/linuxgsm-dashboard/backend"
)

// -------------------------------------------------------------
// Test fixtures
// -------------------------------------------------------------

const testPassword = "test-password-123"

type testEnv struct {
	t       *testing.T
	app     *app
	handler http.Handler
	srvDir  string // directory of server "srv1"
	tmp     string
}

// newTestEnv creates an app with two fake servers (no discovery, nonexistent Linux users) and
// the users admin2 (admin), viewer, operator (start/stop), full (all permissions) and other
// (all permissions on srv2 only).
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "config")
	am, err := backend.NewAuthManager(cfgDir)
	if err != nil {
		t.Fatal(err)
	}

	srvDir := filepath.Join(tmp, "srv1")
	writeFile(t, filepath.Join(srvDir, "serverfiles", "server.cfg"), "hostname test")
	writeFile(t, filepath.Join(srvDir, "serverfiles", "readme.txt"), "hello")
	writeFile(t, filepath.Join(srvDir, "serverfiles_evil", "secret.txt"), "TOP-SECRET")
	writeFile(t, filepath.Join(tmp, "outside.txt"), "TOP-SECRET")
	writeFile(t, filepath.Join(srvDir, "lgsm", "config-lgsm", "srv1server", "srv1server.cfg"), "discordalert=\"on\"\ndiscordwebhook=\"https://hook/SECRET-WEBHOOK\"\n")
	writeFile(t, filepath.Join(srvDir, "public_html", "fastdl", "maps", "de_test.bsp"), "MAPDATA")
	writeFile(t, filepath.Join(srvDir, "public_html", "fastdl_evil", "secret.txt"), "TOP-SECRET")

	inst := backend.NewStaticInstanceManager(cfgDir,
		backend.GameServerInstance{ID: "srv1", Name: "Server 1", User: "lgsmtest_nouser1", Script: "srv1server", Dir: srvDir, Status: "stopped"},
		backend.GameServerInstance{ID: "srv2", Name: "Server 2", User: "lgsmtest_nouser2", Script: "srv2server", Dir: filepath.Join(tmp, "srv2"), Status: "stopped"},
	)

	users := []struct {
		name, role string
		servers    []string
		perms      []string
	}{
		{"admin2", backend.RoleAdmin, nil, nil},
		{"viewer", backend.RoleUser, []string{"srv1"}, nil},
		{"operator", backend.RoleUser, []string{"srv1"}, []string{backend.PermStart, backend.PermStop}},
		{"full", backend.RoleUser, []string{"srv1"}, backend.AssignablePermissions},
		{"other", backend.RoleUser, []string{"srv2"}, backend.AssignablePermissions},
		{"configonly", backend.RoleUser, []string{"srv1"}, []string{backend.PermConfig}},
		{"filesonly", backend.RoleUser, []string{"srv1"}, []string{backend.PermFiles}},
	}
	for _, u := range users {
		if err := am.CreateUser(u.name, testPassword, u.role, u.servers, u.perms); err != nil {
			t.Fatal(err)
		}
	}

	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		uiFS:           ui,
		auth:           am,
		inst:           inst,
		limiter:        backend.NewLoginLimiter(),
		configDir:      cfgDir,
		maxUploadBytes: 1 << 20,
	}
	return &testEnv{t: t, app: a, handler: a.routes(), srvDir: srvDir, tmp: tmp}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

type session struct {
	cookies []*http.Cookie
	csrf    string
}

func (e *testEnv) login(user string) session {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": testPassword})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login %s failed: %d %s", user, rec.Code, rec.Body.String())
	}
	s := session{cookies: rec.Result().Cookies()}
	for _, c := range s.cookies {
		if c.Name == backend.CSRFCookieName {
			s.csrf = c.Value
		}
	}
	return s
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func (e *testEnv) do(s *session, method, path string, body interface{}, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	case []byte:
		reader = bytes.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	if s != nil {
		for _, c := range s.cookies {
			req.AddCookie(c)
		}
		if method != http.MethodGet && s.csrf != "" {
			req.Header.Set("X-CSRF-Token", s.csrf)
		}
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func isPermissionDenied(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "missing permission")
}

// -------------------------------------------------------------
// RBAC matrix for every server scoped route
// -------------------------------------------------------------

// sideEffectRoutes are never executed with an *allowed* user in tests because they would run
// real system commands (useradd/userdel/systemctl/crontab/rclone/LinuxGSM).
var sideEffectRoutes = map[string]bool{
	"delete POST": true, "action POST": true, "backups/restore POST": true, "cloudbackup/sync POST": true,
	"systemd/install POST": true, "cron/install POST": true, "cron POST": true, "clone POST": true,
	"backups/settings POST": true, "console/send POST": true, "rcon POST": true, "portcheck GET": true,
	"backups/upload POST": true, "files/upload POST": true, "backups/delete POST": true,
}

func routeRequest(key string) (method, path string) {
	parts := strings.SplitN(key, " ", 2)
	return parts[1], "/api/servers/srv1/" + parts[0]
}

func TestRBACMatrixAllServerRoutes(t *testing.T) {
	e := newTestEnv(t)
	viewer := e.login("viewer")
	full := e.login("full")
	other := e.login("other")
	admin := e.login("admin2")

	for key, policy := range serverRoutes {
		perm := policy.perm
		method, path := routeRequest(key)

		// A user assigned to a different server is always denied.
		if rec := e.do(&other, method, path, "{}"); !isPermissionDenied(rec) {
			t.Errorf("%s: user of another server got %d %s", key, rec.Code, rec.Body.String())
		}

		switch perm {
		case backend.PermView:
			if rec := e.do(&viewer, method, path, "{}"); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				if !sideEffectRoutes[key] {
					t.Errorf("%s: viewer should be allowed, got %d", key, rec.Code)
				}
			}
		case backend.PermAdmin:
			// Even a user holding every assignable permission is denied.
			if rec := e.do(&full, method, path, "{}"); !isPermissionDenied(rec) {
				t.Errorf("%s: non-admin got %d %s", key, rec.Code, rec.Body.String())
			}
		case permByAction:
			// covered by TestActionEndpoint
		default:
			if rec := e.do(&viewer, method, path, "{}"); !isPermissionDenied(rec) {
				t.Errorf("%s: viewer without %s got %d %s", key, perm, rec.Code, rec.Body.String())
			}
			// a user with every permission except the required one is denied
			name := "without_" + perm
			var perms []string
			for _, p := range backend.AssignablePermissions {
				if p != perm {
					perms = append(perms, p)
				}
			}
			if _, ok := e.app.auth.GetUser(name); !ok {
				if err := e.app.auth.CreateUser(name, testPassword, backend.RoleUser, []string{"srv1"}, perms); err != nil {
					t.Fatal(err)
				}
			}
			without := e.login(name)
			if rec := e.do(&without, method, path, "{}"); !isPermissionDenied(rec) {
				t.Errorf("%s: user without %s got %d %s", key, perm, rec.Code, rec.Body.String())
			}
		}

		if sideEffectRoutes[key] {
			continue
		}
		allowed := &full
		if perm == backend.PermAdmin {
			allowed = &admin
		}
		if rec := e.do(allowed, method, path, "{}"); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s: authorized user got %d %s", key, rec.Code, rec.Body.String())
		}
	}
}

func TestUnknownServerAndRoute(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login("admin2")
	viewer := e.login("viewer")

	if rec := e.do(&admin, http.MethodGet, "/api/servers/doesnotexist/mods", nil); rec.Code != http.StatusNotFound {
		t.Errorf("admin unknown server: %d", rec.Code)
	}
	// Non-admins cannot probe which server IDs exist.
	if rec := e.do(&viewer, http.MethodGet, "/api/servers/doesnotexist/mods", nil); rec.Code != http.StatusForbidden {
		t.Errorf("viewer unknown server: %d", rec.Code)
	}
	if rec := e.do(&admin, http.MethodGet, "/api/servers/srv1/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown route: %d", rec.Code)
	}
	if rec := e.do(nil, http.MethodGet, "/api/servers/srv1/mods", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := e.do(&admin, http.MethodGet, "/api/servers/..%2f..%2fetc/mods", nil); rec.Code == http.StatusOK {
		t.Errorf("encoded traversal server id accepted")
	}
}

func TestServerListOnlyShowsAssignedServers(t *testing.T) {
	e := newTestEnv(t)
	viewer := e.login("viewer")
	rec := e.do(&viewer, http.MethodGet, "/api/servers", nil)
	var list []backend.GameServerInstance
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != "srv1" {
		t.Fatalf("viewer sees %v", list)
	}
}

// -------------------------------------------------------------
// Actions & bulk
// -------------------------------------------------------------

func TestActionEndpoint(t *testing.T) {
	e := newTestEnv(t)
	viewer := e.login("viewer")
	operator := e.login("operator")
	full := e.login("full")

	// GET must never execute anything.
	if rec := e.do(&operator, http.MethodGet, "/api/servers/srv1/action?action=stop", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET action: %d", rec.Code)
	}
	if rec := e.do(&operator, http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": "rm -rf"}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid action: %d", rec.Code)
	}
	if rec := e.do(&viewer, http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": "stop"}); !isPermissionDenied(rec) {
		t.Errorf("viewer stop: %d", rec.Code)
	}
	if rec := e.do(&operator, http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": "restart"}); !isPermissionDenied(rec) {
		t.Errorf("operator restart without permission: %d", rec.Code)
	}
	for _, adminOnly := range []string{"update", "full-wipe", "postdetails", "mods-install", "change-password"} {
		if rec := e.do(&full, http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": adminOnly}); !isPermissionDenied(rec) {
			t.Errorf("non-admin %s: %d", adminOnly, rec.Code)
		}
	}
	// Allowed: the (fake) server user does not exist, so the command fails harmlessly.
	rec := e.do(&operator, http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": "stop"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"type":"exit"`) {
		t.Errorf("allowed stop: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBulkActionAuthorization(t *testing.T) {
	e := newTestEnv(t)
	operator := e.login("operator")
	viewer := e.login("viewer")
	admin := e.login("admin2")

	cases := []struct {
		name string
		s    *session
		body map[string]interface{}
		want int
	}{
		{"unauthorized server in list", &operator, map[string]interface{}{"action": "stop", "serverIds": []string{"srv1", "srv2"}}, http.StatusForbidden},
		{"only foreign server", &operator, map[string]interface{}{"action": "stop", "serverIds": []string{"srv2"}}, http.StatusForbidden},
		{"no servers", &operator, map[string]interface{}{"action": "stop", "serverIds": []string{}}, http.StatusBadRequest},
		{"invalid action", &admin, map[string]interface{}{"action": "full-wipe", "serverIds": []string{"srv1"}}, http.StatusBadRequest},
		{"shell action", &admin, map[string]interface{}{"action": "stop; id", "serverIds": []string{"srv1"}}, http.StatusBadRequest},
		{"viewer stop", &viewer, map[string]interface{}{"action": "stop", "serverIds": []string{"srv1"}}, http.StatusForbidden},
		{"operator update (admin only)", &operator, map[string]interface{}{"action": "update", "serverIds": []string{"srv1"}}, http.StatusForbidden},
		{"admin unknown server", &admin, map[string]interface{}{"action": "stop", "serverIds": []string{"srv1", "ghost"}}, http.StatusNotFound},
	}
	for _, c := range cases {
		if rec := e.do(c.s, http.MethodPost, "/api/servers/bulk", c.body); rec.Code != c.want {
			t.Errorf("%s: got %d want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}

	// All allowed (duplicates are collapsed); commands fail harmlessly for the fake user.
	rec := e.do(&operator, http.MethodPost, "/api/servers/bulk", map[string]interface{}{"action": "stop", "serverIds": []string{"srv1", "srv1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed bulk: %d %s", rec.Code, rec.Body.String())
	}
	var res map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res) != 1 {
		t.Fatalf("duplicates not collapsed: %v", res)
	}
}

// -------------------------------------------------------------
// Console / RCON, cron, alerts
// -------------------------------------------------------------

func TestConsoleAndRconRequireConsolePermission(t *testing.T) {
	e := newTestEnv(t)
	operator := e.login("operator") // server access, no console permission
	for _, path := range []string{"/api/servers/srv1/rcon", "/api/servers/srv1/console/send"} {
		if rec := e.do(&operator, http.MethodPost, path, map[string]string{"command": "kick player"}); !isPermissionDenied(rec) {
			t.Errorf("%s without console: %d", path, rec.Code)
		}
	}
	if rec := e.do(&operator, http.MethodGet, "/api/servers/srv1/console", nil); !isPermissionDenied(rec) {
		t.Errorf("console read without console: %d", rec.Code)
	}
	full := e.login("full")
	if rec := e.do(&full, http.MethodPost, "/api/servers/srv1/rcon", map[string]string{"command": "say hi\nquit"}); rec.Code != http.StatusBadRequest {
		t.Errorf("multi-line rcon accepted: %d", rec.Code)
	}
}

func TestCronWriteRequiresSchedulePermission(t *testing.T) {
	e := newTestEnv(t)
	viewer := e.login("viewer")
	if rec := e.do(&viewer, http.MethodPost, "/api/servers/srv1/cron", map[string]interface{}{"jobs": map[string]string{"monitor": "*/5 * * * *"}}); !isPermissionDenied(rec) {
		t.Errorf("viewer cron write: %d", rec.Code)
	}
	if rec := e.do(&viewer, http.MethodPost, "/api/servers/srv1/cron/install", "{}"); !isPermissionDenied(rec) {
		t.Errorf("viewer cron install: %d", rec.Code)
	}
	full := e.login("full")
	if rec := e.do(&full, http.MethodPost, "/api/servers/srv1/cron", map[string]interface{}{"jobs": map[string]string{"monitor": "5 * * * * id #"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("cron injection: %d", rec.Code)
	}
}

func TestAlertsPermissionAndSecretMasking(t *testing.T) {
	e := newTestEnv(t)
	operator := e.login("operator")
	if rec := e.do(&operator, http.MethodGet, "/api/servers/srv1/alerts", nil); !isPermissionDenied(rec) {
		t.Errorf("alerts read without permission: %d", rec.Code)
	}
	if rec := e.do(&operator, http.MethodPost, "/api/servers/srv1/alerts", map[string]interface{}{"values": map[string]string{"discordwebhook": "https://evil"}}); !isPermissionDenied(rec) {
		t.Errorf("alerts write without permission: %d", rec.Code)
	}

	full := e.login("full")
	rec := e.do(&full, http.MethodGet, "/api/servers/srv1/alerts", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "SECRET-WEBHOOK") || !strings.Contains(rec.Body.String(), backend.SecretMask) {
		t.Fatalf("secret leaked or not masked: %s", rec.Body.String())
	}
	for _, bad := range []map[string]string{{"discordwebhook": "x\nid"}, {"evil": "x"}, {"discordalert": "yes"}} {
		if rec := e.do(&full, http.MethodPost, "/api/servers/srv1/alerts", map[string]interface{}{"values": bad}); rec.Code != http.StatusBadRequest {
			t.Errorf("invalid alert values %v accepted: %d", bad, rec.Code)
		}
	}
}

// -------------------------------------------------------------
// Files vs config, traversal, uploads
// -------------------------------------------------------------

func TestConfigPermissionDoesNotGrantFiles(t *testing.T) {
	e := newTestEnv(t)
	cfg := e.login("configonly")
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/servers/srv1/files"},
		{http.MethodGet, "/api/servers/srv1/files/content?path=readme.txt"},
		{http.MethodGet, "/api/servers/srv1/files/download?path=readme.txt"},
		{http.MethodPost, "/api/servers/srv1/files"},
		{http.MethodPost, "/api/servers/srv1/files/upload"},
	} {
		if rec := e.do(&cfg, r.method, r.path, `{"path":"x","content":"y"}`); !isPermissionDenied(rec) {
			t.Errorf("config-only %s %s: %d", r.method, r.path, rec.Code)
		}
	}
	if rec := e.do(&cfg, http.MethodGet, "/api/servers/srv1/configs", nil); rec.Code != http.StatusOK {
		t.Errorf("config list: %d", rec.Code)
	}
	files := e.login("filesonly")
	if rec := e.do(&files, http.MethodGet, "/api/servers/srv1/configs", nil); !isPermissionDenied(rec) {
		t.Errorf("files-only config list: %d", rec.Code)
	}
}

func TestFileManagerTraversal(t *testing.T) {
	e := newTestEnv(t)
	files := e.login("filesonly")

	if rec := e.do(&files, http.MethodGet, "/api/servers/srv1/files/content?path=readme.txt", nil); rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("normal read: %d %q", rec.Code, rec.Body.String())
	}
	attacks := []string{
		"../serverfiles_evil/secret.txt",
		"../../outside.txt",
		"..%2F..%2Foutside.txt",
		"%2e%2e/%2e%2e/outside.txt",
		"/etc/passwd",
		filepath.Join(e.tmp, "outside.txt"),
	}
	for _, a := range attacks {
		for _, ep := range []string{"files/content", "files/download"} {
			rec := e.do(&files, http.MethodGet, "/api/servers/srv1/"+ep+"?path="+a, nil)
			if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "TOP-SECRET") {
				t.Errorf("%s %q: %d %s", ep, a, rec.Code, rec.Body.String())
			}
		}
		// JSON paths are not URL-decoded: "%2e%2e" is a literal (harmless) file name there, so
		// only real traversal sequences must be rejected outright.
		rec := e.do(&files, http.MethodPost, "/api/servers/srv1/files", map[string]string{"path": a, "content": "pwned"})
		if rec.Code == http.StatusOK && !strings.Contains(a, "%") {
			t.Errorf("write %q accepted", a)
		}
	}
	for _, p := range []string{filepath.Join(e.tmp, "outside.txt"), filepath.Join(e.srvDir, "serverfiles_evil", "secret.txt")} {
		if data, _ := os.ReadFile(p); string(data) != "TOP-SECRET" {
			t.Errorf("file outside the root was modified: %s", p)
		}
	}
	// symlink escape
	if err := os.Symlink(e.tmp, filepath.Join(e.srvDir, "serverfiles", "escape")); err == nil {
		rec := e.do(&files, http.MethodGet, "/api/servers/srv1/files/content?path=escape/outside.txt", nil)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "TOP-SECRET") {
			t.Errorf("symlink escape: %d %s", rec.Code, rec.Body.String())
		}
	}

	cfg := e.login("configonly")
	rec := e.do(&cfg, http.MethodGet, "/api/servers/srv1/configs/file?path="+filepath.Join(e.tmp, "outside.txt"), nil)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "TOP-SECRET") {
		t.Errorf("config editor outside root: %d %s", rec.Code, rec.Body.String())
	}
	good := filepath.Join(e.srvDir, "serverfiles", "server.cfg")
	if rec := e.do(&cfg, http.MethodGet, "/api/servers/srv1/configs/file?path="+good, nil); rec.Code != http.StatusOK {
		t.Errorf("config editor valid file: %d %s", rec.Code, rec.Body.String())
	}
}

func multipartBody(t *testing.T, field, filename string, size int, extra map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range extra {
		_ = w.WriteField(k, v)
	}
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(bytes.Repeat([]byte("a"), size))
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestUploadLimitsAndNames(t *testing.T) {
	e := newTestEnv(t)
	files := e.login("filesonly")

	body, ct := multipartBody(t, "file", "ok.txt", 100, map[string]string{"path": ""})
	rec := e.do(&files, http.MethodPost, "/api/servers/srv1/files/upload", body.Bytes(), withHeader("Content-Type", ct))
	if rec.Code != http.StatusOK {
		t.Fatalf("normal upload: %d %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(e.srvDir, "serverfiles", "ok.txt")); len(data) != 100 {
		t.Fatal("uploaded file missing")
	}

	body, ct = multipartBody(t, "file", "big.bin", 2<<20, map[string]string{"path": ""})
	rec = e.do(&files, http.MethodPost, "/api/servers/srv1/files/upload", body.Bytes(), withHeader("Content-Type", ct))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.srvDir, "serverfiles", "big.bin")); err == nil {
		t.Error("oversized upload was stored")
	}

	body, ct = multipartBody(t, "file", "x.txt", 10, map[string]string{"path": "../../"})
	rec = e.do(&files, http.MethodPost, "/api/servers/srv1/files/upload", body.Bytes(), withHeader("Content-Type", ct))
	if rec.Code == http.StatusOK {
		t.Error("upload traversal accepted")
	}

	full := e.login("full")
	body, ct = multipartBody(t, "backup", "../../evil.tar.gz", 10, nil)
	rec = e.do(&full, http.MethodPost, "/api/servers/srv1/backups/upload", body.Bytes(), withHeader("Content-Type", ct))
	if rec.Code == http.StatusOK {
		// filename is reduced to its base name by the multipart reader; ensure nothing escaped
		if _, err := os.Stat(filepath.Join(e.tmp, "evil.tar.gz")); err == nil {
			t.Error("backup upload escaped the backup directory")
		}
	}

	if rec := e.do(&full, http.MethodPost, "/api/servers/srv1/files", strings.Repeat("a", maxEditorBody+10)); rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Errorf("oversized JSON body: %d", rec.Code)
	}
}

// -------------------------------------------------------------
// FastDL
// -------------------------------------------------------------

func TestFastDL(t *testing.T) {
	e := newTestEnv(t)
	rec := e.do(nil, http.MethodGet, "/fastdl/srv1/maps/de_test.bsp", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "MAPDATA" {
		t.Fatalf("fastdl normal: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("fastdl content type %q", ct)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Error("fastdl must be sandboxed")
	}
	for _, p := range []string{
		"/fastdl/srv1/../fastdl_evil/secret.txt",
		"/fastdl/srv1/..%2Ffastdl_evil%2Fsecret.txt",
		"/fastdl/srv1/%2e%2e/%2e%2e/%2e%2e/outside.txt",
		"/fastdl/srv1/maps",
		"/fastdl/srv2/maps/de_test.bsp",
		"/fastdl/..%2Fsrv1/maps/de_test.bsp",
	} {
		rec := e.do(nil, http.MethodGet, p, nil)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "TOP-SECRET") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	if err := os.Symlink(filepath.Join(e.tmp, "outside.txt"), filepath.Join(e.srvDir, "public_html", "fastdl", "leak.txt")); err == nil {
		if rec := e.do(nil, http.MethodGet, "/fastdl/srv1/leak.txt", nil); rec.Code == http.StatusOK {
			t.Error("fastdl symlink escape")
		}
	}
}

// -------------------------------------------------------------
// Authentication, CSRF, cookies, headers, CORS
// -------------------------------------------------------------

func TestLoginRateLimit(t *testing.T) {
	e := newTestEnv(t)
	bad, _ := json.Marshal(map[string]string{"username": "viewer", "password": "wrong"})
	var last int
	for i := 0; i < 7; i++ {
		last = e.do(nil, http.MethodPost, "/api/auth/login", bad).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
	good, _ := json.Marshal(map[string]string{"username": "viewer", "password": testPassword})
	if rec := e.do(nil, http.MethodPost, "/api/auth/login", good); rec.Code != http.StatusTooManyRequests {
		t.Errorf("cooldown should also apply to correct password, got %d", rec.Code)
	}
}

func TestCookieFlags(t *testing.T) {
	e := newTestEnv(t)
	s := e.login("viewer")
	var sess, csrf *http.Cookie
	for _, c := range s.cookies {
		switch c.Name {
		case backend.SessionCookieName:
			sess = c
		case backend.CSRFCookieName:
			csrf = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode || sess.Secure {
		t.Fatalf("unexpected session cookie %+v", sess)
	}
	if csrf == nil || csrf.HttpOnly || csrf.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected csrf cookie %+v", csrf)
	}

	e.app.secureCookies = true
	s = e.login("viewer")
	for _, c := range s.cookies {
		if !c.Secure {
			t.Errorf("cookie %s not secure", c.Name)
		}
	}
	e.app.secureCookies = false
	e.app.trustProxy = true
	body, _ := json.Marshal(map[string]string{"username": "viewer", "password": testPassword})
	rec := e.do(nil, http.MethodPost, "/api/auth/login", body, withHeader("X-Forwarded-Proto", "https"))
	for _, c := range rec.Result().Cookies() {
		if !c.Secure {
			t.Errorf("cookie %s not secure behind https proxy", c.Name)
		}
	}
}

func TestSessionFixationAndLogout(t *testing.T) {
	e := newTestEnv(t)
	first := e.login("viewer")
	// Log in again presenting the old cookie: the old session must die.
	body, _ := json.Marshal(map[string]string{"username": "viewer", "password": testPassword})
	rec := e.do(&session{cookies: first.cookies}, http.MethodPost, "/api/auth/login", body)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if r := e.do(&first, http.MethodGet, "/api/servers", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("old session still valid after re-login: %d", r.Code)
	}

	s := e.login("viewer")
	if r := e.do(&s, http.MethodPost, "/api/auth/logout", nil); r.Code != http.StatusOK {
		t.Fatal(r.Code)
	}
	if r := e.do(&s, http.MethodGet, "/api/servers", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("session valid after logout: %d", r.Code)
	}
}

func TestCSRFProtection(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login("admin2")

	noToken := session{cookies: admin.cookies}
	if rec := e.do(&noToken, http.MethodPost, "/api/servers/srv1/tag", map[string]string{"tag": "x"}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "CSRF") {
		t.Errorf("missing token: %d %s", rec.Code, rec.Body.String())
	}
	wrong := session{cookies: admin.cookies, csrf: strings.Repeat("0", 64)}
	if rec := e.do(&wrong, http.MethodPost, "/api/servers/srv1/tag", map[string]string{"tag": "x"}); rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec := e.do(&admin, http.MethodPost, "/api/servers/srv1/tag", map[string]string{"tag": "x"}, withHeader("Origin", "https://evil.example")); rec.Code != http.StatusForbidden {
		t.Errorf("cross origin: %d", rec.Code)
	}
	if rec := e.do(&admin, http.MethodPost, "/api/servers/srv1/tag", map[string]string{"tag": "x"}, withHeader("Origin", "http://example.com")); rec.Code != http.StatusOK {
		t.Errorf("same origin with token: %d %s", rec.Code, rec.Body.String())
	}
	// Login itself rejects cross-origin posts (login CSRF).
	body, _ := json.Marshal(map[string]string{"username": "viewer", "password": testPassword})
	if rec := e.do(nil, http.MethodPost, "/api/auth/login", body, withHeader("Origin", "https://evil.example")); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin login: %d", rec.Code)
	}
	// GET endpoints keep working without a token.
	if rec := e.do(&noToken, http.MethodGet, "/api/servers", nil); rec.Code != http.StatusOK {
		t.Errorf("GET without token: %d", rec.Code)
	}
}

func TestStateChangingGETsAreRejected(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login("admin2")
	for _, p := range []string{
		"/api/servers/srv1/action?action=restart",
		"/api/servers/srv1/backups/restore?file=x.tar.gz",
		"/api/servers/srv1/cloudbackup/sync",
		"/api/games/install?game=vhserver&user=vhserver",
		"/api/games/sync",
		"/api/servers/srv1/delete",
		"/api/admin/update/trigger",
		"/api/auth/logout",
	} {
		if rec := e.do(&admin, http.MethodGet, p, nil); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
}

func TestSecurityHeadersAndNoCORS(t *testing.T) {
	e := newTestEnv(t)
	s := e.login("viewer")
	rec := e.do(&s, http.MethodGet, "/api/servers", nil, withHeader("Origin", "https://evil.example"))
	h := rec.Header()
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if h.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, h.Get(k), v)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, d := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("CSP missing %q", d)
		}
	}
	if h.Get("Access-Control-Allow-Origin") != "" || h.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("CORS headers must not be sent")
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must only be sent over HTTPS")
	}
	if rec := e.do(nil, http.MethodGet, "/app.js", nil); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("UI assets must be served and revalidated after updates: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if cc := e.do(nil, http.MethodGet, "/app.js", nil).Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("UI assets must be revalidated after updates, got Cache-Control %q", cc)
	}
}

func TestAdminEndpointsRequireAdmin(t *testing.T) {
	e := newTestEnv(t)
	full := e.login("full")
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/users"},
		{http.MethodPost, "/api/admin/users"},
		{http.MethodPut, "/api/admin/users"},
		{http.MethodDelete, "/api/admin/users?username=viewer"},
		{http.MethodGet, "/api/admin/server-paths"},
		{http.MethodPut, "/api/admin/server-paths"},
		{http.MethodGet, "/api/system/audit"},
		{http.MethodPost, "/api/system/firewall/open"},
		{http.MethodPost, "/api/games/install"},
		{http.MethodPost, "/api/admin/update/trigger"},
	} {
		if rec := e.do(&full, r.method, r.path, "{}"); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", r.method, r.path, rec.Code)
		}
	}

	// Privilege escalation via self-update of own account is impossible (admin-only API), and
	// admins cannot assign unknown permissions.
	admin := e.login("admin2")
	rec := e.do(&admin, http.MethodPut, "/api/admin/users", map[string]interface{}{"username": "viewer", "role": "user", "permissions": []string{"superuser"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown permission accepted: %d", rec.Code)
	}
	rec = e.do(&admin, http.MethodPost, "/api/games/install", map[string]string{"game": "vhserver", "user": "newuser", "install_dir": "/etc/evil"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("install into system dir accepted: %d", rec.Code)
	}
	rec = e.do(&admin, http.MethodPut, "/api/admin/server-paths", map[string]interface{}{"paths": []string{"/etc"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("system dir accepted as server path: %d", rec.Code)
	}
}

// enableTwoFactorViaAPI runs the setup through the HTTP API and returns secret and recovery codes.
func enableTwoFactorViaAPI(t *testing.T, e *testEnv, s *session) (string, []string) {
	t.Helper()
	rec := e.do(s, http.MethodPost, "/api/account/2fa/setup", map[string]string{"password": testPassword})
	var setup struct{ Secret, URI, QR string }
	_ = json.Unmarshal(rec.Body.Bytes(), &setup)
	if rec.Code != http.StatusOK || setup.Secret == "" || !strings.HasPrefix(setup.QR, "data:image/png;base64,") {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := backend.TOTPCode(setup.Secret, time.Now().Unix()/30)
	rec = e.do(s, http.MethodPost, "/api/account/2fa/enable", map[string]string{"code": code})
	var enabled struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &enabled)
	if rec.Code != http.StatusOK || len(enabled.RecoveryCodes) != 10 {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	return setup.Secret, enabled.RecoveryCodes
}

func loginChallenge(t *testing.T, e *testEnv, user string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": testPassword})
	rec := e.do(nil, http.MethodPost, "/api/auth/login", body)
	var res struct{ Status, Challenge string }
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Status != "2fa_required" || res.Challenge == "" {
		t.Fatalf("expected 2fa challenge for %s: %d %s", user, rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("password step must not set session cookies")
	}
	return res.Challenge
}

func TestTwoFactorLoginOverHTTP(t *testing.T) {
	e := newTestEnv(t)
	viewer := e.login("viewer")
	operator := e.login("operator")
	_, viewerCodes := enableTwoFactorViaAPI(t, e, &viewer)
	_, operatorCodes := enableTwoFactorViaAPI(t, e, &operator)

	// The second step contains no username; the challenge decides the account.
	challenge := loginChallenge(t, e, "viewer")
	rec := e.do(nil, http.MethodPost, "/api/auth/login/2fa", map[string]string{"challenge": challenge, "code": operatorCodes[0]})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("viewer login accepted operator's recovery code: %d", rec.Code)
	}
	rec = e.do(nil, http.MethodPost, "/api/auth/login/2fa", map[string]string{"challenge": challenge, "code": viewerCodes[0]})
	if rec.Code != http.StatusOK {
		t.Fatalf("own recovery code rejected: %d %s", rec.Code, rec.Body.String())
	}
	s := session{cookies: rec.Result().Cookies()}
	info := e.do(&s, http.MethodGet, "/api/system/info", nil)
	if !strings.Contains(info.Body.String(), `"username":"viewer"`) {
		t.Fatalf("logged into the wrong account: %s", info.Body.String())
	}

	// Unknown / reused challenges are rejected, cross-origin requests too.
	if rec := e.do(nil, http.MethodPost, "/api/auth/login/2fa", map[string]string{"challenge": challenge, "code": viewerCodes[1]}); rec.Code != http.StatusUnauthorized {
		t.Errorf("reused challenge: %d", rec.Code)
	}
	if rec := e.do(nil, http.MethodPost, "/api/auth/login/2fa", map[string]string{"challenge": "forged", "code": "123456"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("forged challenge: %d", rec.Code)
	}
	c2 := loginChallenge(t, e, "operator")
	if rec := e.do(nil, http.MethodPost, "/api/auth/login/2fa", map[string]string{"challenge": c2, "code": operatorCodes[1]}, withHeader("Origin", "https://evil.example")); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin second step: %d", rec.Code)
	}

	// Secrets never leave the server.
	admin := e.login("admin2")
	users := e.do(&admin, http.MethodGet, "/api/admin/users", nil).Body.String()
	if strings.Contains(users, "totp_secret") || strings.Contains(users, "recovery_codes") || !strings.Contains(users, `"totp_enabled":true`) {
		t.Fatalf("user list exposes 2FA secrets or misses the status: %s", users)
	}
}

func TestTwoFactorAdminReset(t *testing.T) {
	e := newTestEnv(t)
	viewer := e.login("viewer")
	enableTwoFactorViaAPI(t, e, &viewer)

	full := e.login("full")
	if rec := e.do(&full, http.MethodPost, "/api/admin/users/2fa-reset", map[string]string{"username": "viewer"}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin reset: %d", rec.Code)
	}
	admin := e.login("admin2")
	if rec := e.do(&admin, http.MethodPost, "/api/admin/users/2fa-reset", map[string]string{"username": "viewer"}); rec.Code != http.StatusOK {
		t.Fatalf("admin reset: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(&viewer, http.MethodGet, "/api/servers", nil); rec.Code != http.StatusUnauthorized {
		t.Error("reset must log the user out")
	}
	e.login("viewer") // password only again
}

// A user with only start/stop must not be able to use anything on the settings page except
// changing the own password and reading the (harmless) schedule and stop mode.
func TestSettingsPageForStartStopUser(t *testing.T) {
	e := newTestEnv(t)
	op := e.login("operator")

	denied := []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/update/check", ""},
		{http.MethodPost, "/api/admin/update/trigger", `{"tag_name":"v9.9.9"}`},
		{http.MethodGet, "/api/system/firewall", ""},
		{http.MethodPost, "/api/system/firewall/open", `{"port":1234,"protocol":"tcp"}`},
		{http.MethodGet, "/api/admin/server-paths", ""},
		{http.MethodPost, "/api/servers/srv1/tag", `{"tag":"x"}`},
		{http.MethodPost, "/api/servers/srv1/stopmode", `{"stopmode":"1"}`},
		{http.MethodPost, "/api/servers/srv1/clone", `{"suffix":"x"}`},
		{http.MethodPost, "/api/servers/srv1/systemd/install", `{}`},
		{http.MethodPost, "/api/servers/srv1/cron/install", `{}`},
		{http.MethodPost, "/api/servers/srv1/cron", `{"monitor_interval":"5"}`},
		{http.MethodGet, "/api/servers/srv1/alerts", ""},
		{http.MethodPost, "/api/servers/srv1/alerts", `{}`},
		{http.MethodGet, "/api/servers/srv1/cloudbackup/settings", ""},
		{http.MethodPost, "/api/servers/srv1/cloudbackup/settings", `{}`},
	}
	for _, d := range denied {
		if rec := e.do(&op, d.method, d.path, d.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403 (%s)", d.method, d.path, rec.Code, rec.Body.String())
		}
	}

	for _, p := range []string{"/api/servers/srv1/cron", "/api/servers/srv1/stopmode"} {
		if rec := e.do(&op, http.MethodGet, p, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s: got %d", p, rec.Code)
		}
	}
	rec := e.do(&op, http.MethodPost, "/api/settings/password", map[string]string{"oldPassword": testPassword, "newPassword": "operator-new-pass"})
	if rec.Code != http.StatusOK {
		t.Errorf("own password change: %d %s", rec.Code, rec.Body.String())
	}
	if u, _ := e.app.auth.GetUser("admin2"); !backend.VerifyPassword(testPassword, u.PasswordHash) {
		t.Error("changing the own password must not touch other accounts")
	}
}

func TestPasswordChangeInvalidatesOtherSessions(t *testing.T) {
	e := newTestEnv(t)
	a := e.login("viewer")
	b := e.login("viewer")
	rec := e.do(&a, http.MethodPost, "/api/settings/password", map[string]string{"oldPassword": testPassword, "newPassword": "brand-new-password"})
	if rec.Code != http.StatusOK {
		t.Fatalf("password change: %d %s", rec.Code, rec.Body.String())
	}
	if r := e.do(&a, http.MethodGet, "/api/servers", nil); r.Code != http.StatusOK {
		t.Errorf("current session lost: %d", r.Code)
	}
	if r := e.do(&b, http.MethodGet, "/api/servers", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("other session still valid: %d", r.Code)
	}
}

// -------------------------------------------------------------
// Frontend XSS regression (static checks of the embedded UI)
// -------------------------------------------------------------

func TestFrontendEscapesUntrustedValues(t *testing.T) {
	data, err := uiFS.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	forbidden := []string{
		"new EventSource(",                      // state changing actions must use POST
		"'kick ${escapeHtml(",                   // HTML-escaping inside a JS string in an attribute is bypassable
		"'ban ${escapeHtml(",                    //
		"escapeJs(",                             // removed helper that did not HTML-escape
		">${server.name}",                       // server names come from game configs
		"<strong>${srv.name}</strong>",          //
		"<h4>${game.name}</h4>",                 // crawled from a remote site
		"openFileEditor('${escapeHtml(f.path)}", // file names are attacker controlled
	}
	for _, f := range forbidden {
		if strings.Contains(js, f) {
			t.Errorf("unsafe pattern present in app.js: %q", f)
		}
	}
	for _, required := range []string{"function jsArg(", "X-CSRF-Token", "jsArg('kick ' + p.name)"} {
		if !strings.Contains(js, required) {
			t.Errorf("expected %q in app.js", required)
		}
	}
}
