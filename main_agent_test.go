package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yourdawi/linuxgsm-dashboard/backend"
)

// agentEnv serves the test app as an agent with the given policy.
type agentEnv struct {
	*testEnv
	h http.Handler
}

func newAgentEnv(t *testing.T, policy backend.AgentPolicy) *agentEnv {
	t.Helper()
	e := newTestEnv(t)
	e.app.agentMode = true
	e.app.agentPolicy.Store(&policy)
	return &agentEnv{testEnv: e, h: e.app.agentRoutes()}
}

func (e *agentEnv) req(method, path string, body interface{}) *httptest.ResponseRecorder {
	e.t.Helper()
	var data []byte
	switch b := body.(type) {
	case nil:
	case string:
		data = []byte(b)
	default:
		data, _ = json.Marshal(b)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set(backend.AgentActorHeader, "alice")
	r.Header.Set(backend.AgentActorIPHeader, "198.51.100.7")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func isReadOnlyDenial(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "read-only")
}

func TestAgentRoutesAreClassified(t *testing.T) {
	valid := map[string]bool{backend.AccessRead: true, backend.AccessControl: true, backend.AccessWrite: true}
	for key, p := range serverRoutes {
		switch {
		case key == "action POST":
			if p.access != backend.AccessByAction {
				t.Errorf("%s must be classified by action", key)
			}
		case !valid[p.access]:
			t.Errorf("server route %s has no valid access class (%q)", key, p.access)
		case strings.HasSuffix(key, " POST") && p.access == backend.AccessRead:
			t.Errorf("server route %s changes state but is classified as read", key)
		}
	}
	e := newAgentEnv(t, backend.AgentPolicy{Mode: backend.AgentModeReadOnly})
	for _, r := range e.app.agentRouteTable() {
		if len(r.access) == 0 {
			t.Errorf("agent route %s allows no method", r.pattern)
		}
		for method, class := range r.access {
			if class == "" {
				t.Errorf("agent route %s %s has no access class", method, r.pattern)
			}
			if method != http.MethodGet && method != http.MethodHead && class == backend.AccessRead {
				t.Errorf("agent route %s %s changes state but is classified as read", method, r.pattern)
			}
		}
	}
	for _, a := range []string{"start", "stop", "restart"} {
		if backend.ActionAccess(a) != backend.AccessControl {
			t.Errorf("%s should be a control action", a)
		}
	}
	for _, a := range []string{"update", "backup", "full-wipe", "unknown"} {
		if backend.ActionAccess(a) != backend.AccessWrite {
			t.Errorf("%s should be a write action", a)
		}
	}
}

func TestAgentReadOnlyMode(t *testing.T) {
	e := newAgentEnv(t, backend.AgentPolicy{Mode: backend.AgentModeReadOnly})

	// Reading works without session or CSRF token; the tunnel is the authentication.
	if rec := e.req(http.MethodGet, "/api/servers", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"srv1"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/servers/srv1/console", "/api/servers/srv1/configs", "/api/servers/srv1/files?path=", "/api/system/stats"} {
		if rec := e.req(http.MethodGet, path, nil); isReadOnlyDenial(rec) || rec.Code == http.StatusUnauthorized {
			t.Errorf("GET %s denied: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// Start, stop and restart are allowed (the fake server user fails harmlessly afterwards).
	for _, action := range []string{"start", "stop", "restart"} {
		if rec := e.req(http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": action}); isReadOnlyDenial(rec) {
			t.Errorf("%s denied on a read-only node", action)
		}
	}
	if rec := e.req(http.MethodPost, "/api/servers/bulk", map[string]interface{}{"action": "restart", "serverIds": []string{"srv1"}}); isReadOnlyDenial(rec) {
		t.Error("bulk restart denied on a read-only node")
	}

	// Everything else that changes the node is refused.
	writes := []struct {
		path string
		body interface{}
	}{
		{"/api/servers/srv1/action", map[string]string{"action": "update"}},
		{"/api/servers/srv1/action", map[string]string{"action": "backup"}},
		{"/api/servers/bulk", map[string]interface{}{"action": "update", "serverIds": []string{"srv1"}}},
		{"/api/servers/srv1/configs/file", map[string]string{"path": "x", "content": "y"}},
		{"/api/servers/srv1/console/send", map[string]string{"command": "say hi"}},
		{"/api/servers/srv1/files", map[string]string{"action": "delete", "path": "readme.txt"}},
		{"/api/servers/srv1/delete", "{}"},
		{"/api/servers/srv1/clone", map[string]string{"suffix": "2"}},
		{"/api/games/install", map[string]string{"game": "vhserver", "user": "vhserver"}},
		{"/api/admin/server-paths", map[string][]string{"paths": {"/srv"}}},
	}
	for _, w := range writes {
		if rec := e.req(http.MethodPost, w.path, w.body); !isReadOnlyDenial(rec) {
			t.Errorf("POST %s %v on a read-only node: %d %s", w.path, w.body, rec.Code, rec.Body.String())
		}
	}
	if !strings.Contains(e.app.policy().Mode, "read-only") {
		t.Fatal("policy changed")
	}

	// Routes of the dashboard itself do not exist on an agent.
	for _, path := range []string{"/api/auth/login", "/api/admin/users", "/api/settings/password", "/api/admin/nodes", "/"} {
		if rec := e.req(http.MethodPost, path, "{}"); rec.Code != http.StatusNotFound {
			t.Errorf("%s exists on the agent: %d", path, rec.Code)
		}
	}
}

func TestAgentReadWriteModeAndLocalRestrictions(t *testing.T) {
	e := newAgentEnv(t, backend.AgentPolicy{Mode: backend.AgentModeReadWrite, Servers: []string{"srv1"}})

	if rec := e.req(http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": "update"}); isReadOnlyDenial(rec) {
		t.Error("update denied on a read-write node")
	}
	if rec := e.req(http.MethodPost, "/api/servers/srv1/configs/file", map[string]string{"path": "x", "content": "y"}); isReadOnlyDenial(rec) {
		t.Error("config write denied on a read-write node")
	}

	// The node's server list hides other servers completely.
	if rec := e.req(http.MethodGet, "/api/servers", nil); strings.Contains(rec.Body.String(), `"srv2"`) {
		t.Errorf("srv2 listed: %s", rec.Body.String())
	}
	if rec := e.req(http.MethodGet, "/api/servers/srv2/console", nil); rec.Code != http.StatusNotFound {
		t.Errorf("srv2 reachable: %d", rec.Code)
	}
	if rec := e.req(http.MethodPost, "/api/servers/bulk", map[string]interface{}{"action": "stop", "serverIds": []string{"srv1", "srv2"}}); rec.Code != http.StatusNotFound {
		t.Errorf("bulk with srv2: %d", rec.Code)
	}

	// Firewall and updates need their own local opt-in.
	if rec := e.req(http.MethodPost, "/api/system/firewall/open", map[string]interface{}{"port": 2456, "protocol": "udp"}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "allow_firewall") {
		t.Errorf("firewall: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.req(http.MethodPost, "/api/admin/update/trigger", "{}"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "allow_self_update") {
		t.Errorf("update trigger: %d %s", rec.Code, rec.Body.String())
	}

	// hello describes the node.
	rec := e.req(http.MethodGet, backend.AgentHelloPath, nil)
	var hello backend.AgentHello
	if err := json.Unmarshal(rec.Body.Bytes(), &hello); err != nil || hello.Mode != backend.AgentModeReadWrite ||
		hello.Protocol != backend.AgentProtocolVersion || len(hello.Servers) != 1 {
		t.Fatalf("hello: %v %s", err, rec.Body.String())
	}

	// The audit log names the controller user and address.
	e.req(http.MethodPost, "/api/servers/srv1/action", map[string]string{"action": "stop"})
	found := false
	for _, entry := range e.app.auth.GetAuditLogs() {
		if entry.Username == "controller:alice" && entry.IP == "198.51.100.7" && entry.Action == "ACTION_STOP" {
			found = true
		}
	}
	if !found {
		t.Errorf("audit entry missing: %+v", e.app.auth.GetAuditLogs())
	}
}
