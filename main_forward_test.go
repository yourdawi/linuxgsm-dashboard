package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yourdawi/linuxgsm-dashboard/backend"
)

// multiNodeEnv is a controller (testEnv) with one paired node "node-a" whose agent serves another
// testEnv (servers srv1 and srv2) through a real tunnel.
type multiNodeEnv struct {
	*testEnv
	agent     *agentEnv
	stopAgent func()
}

func newMultiNodeEnv(t *testing.T, controllerMode string) *multiNodeEnv {
	t.Helper()
	e := newTestEnv(t)
	agents, err := backend.NewAgentController(filepath.Join(e.tmp, "config"), Version, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	e.app.agents = agents
	gw, err := agents.Gateway(true)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw.HandleWebSocket(w, r, "192.0.2.10")
	}))
	t.Cleanup(srv.Close)

	ticket, err := agents.Nodes.CreatePairing("Node A", controllerMode, "")
	if err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(t.TempDir(), "agent")
	if _, err := backend.AgentJoin(context.Background(), agentDir, backend.AgentJoinOptions{
		ControllerURL: srv.URL, Token: ticket.Token, CAFingerprint: gw.CA.Fingerprint(),
		Mode: backend.AgentModeReadWrite, Version: Version,
	}); err != nil {
		t.Fatal(err)
	}

	ae := newAgentEnv(t, backend.AgentPolicy{Mode: backend.AgentModeReadWrite})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = backend.RunAgent(ctx, func() (backend.AgentIdentity, error) { return backend.LoadAgentIdentity(agentDir) }, ae.h, t.Logf)
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)

	m := &multiNodeEnv{testEnv: e, agent: ae, stopAgent: stop}
	m.waitFor(t, func() bool {
		_, ok := agents.Hub.NodeServer("node-a~srv1")
		return ok
	})
	return m
}

func (m *multiNodeEnv) waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func serverIDs(t *testing.T, body string) map[string]backend.GameServerInstance {
	t.Helper()
	var list []backend.GameServerInstance
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("server list: %v (%s)", err, body)
	}
	out := make(map[string]backend.GameServerInstance)
	for _, s := range list {
		out[s.ID] = s
	}
	return out
}

func TestNodeServersAreForwarded(t *testing.T) {
	m := newMultiNodeEnv(t, backend.AgentModeReadWrite)
	admin := m.login("admin2")

	// The list contains the local servers and those of the node.
	rec := m.do(&admin, http.MethodGet, "/api/servers", nil)
	list := serverIDs(t, rec.Body.String())
	remote, ok := list["node-a~srv1"]
	if _, local := list["srv1"]; !ok || !local {
		t.Fatalf("list: %s", rec.Body.String())
	}
	if remote.Node != "node-a" || remote.NodeName != "Node A" || remote.NodeOffline || remote.Capabilities == nil {
		t.Fatalf("remote entry %+v", remote)
	}

	// A request for the node's server is answered by the node, with hardened headers.
	rec = m.do(&admin, http.MethodGet, "/api/servers/node-a~srv1/configs", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "srv1server.cfg") {
		t.Fatalf("configs via node: %d %s", rec.Code, rec.Body.String())
	}
	if csp := rec.Header().Values("Content-Security-Policy"); len(csp) != 1 || csp[0] != sandboxCSP {
		t.Fatalf("CSP of a forwarded answer: %v", csp)
	}

	// Writes reach the node and are recorded in both audit logs.
	rec = m.do(&admin, http.MethodPost, "/api/servers/node-a~srv1/configs/file", map[string]string{"path": filepath.Join(m.agent.srvDir, "serverfiles", "server.cfg"), "content": "hostname changed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("config write via node: %d %s", rec.Code, rec.Body.String())
	}
	foundAgent := false
	for _, entry := range m.agent.app.auth.GetAuditLogs() {
		if entry.Username == "controller:admin2" {
			foundAgent = true
		}
	}
	if !foundAgent {
		t.Error("the node's audit log does not name the dashboard user")
	}

	// Actions stream through the tunnel.
	rec = m.do(&admin, http.MethodPost, "/api/servers/node-a~srv1/action", map[string]string{"action": "stop"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"type":"exit"`) {
		t.Fatalf("action via node: %d %s", rec.Code, rec.Body.String())
	}

	// Bulk actions are split between this machine and the node.
	rec = m.do(&admin, http.MethodPost, "/api/servers/bulk", map[string]interface{}{"action": "stop", "serverIds": []string{"srv1", "node-a~srv2"}})
	var bulk map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &bulk); err != nil || bulk["srv1"] == "" || bulk["node-a~srv2"] == "" {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body.String())
	}

	// Tags of node servers are kept by the dashboard.
	if rec := m.do(&admin, http.MethodPost, "/api/servers/node-a~srv1/tag", map[string]string{"tag": "Cluster B"}); rec.Code != http.StatusOK {
		t.Fatalf("tag: %d %s", rec.Code, rec.Body.String())
	}
	if l := serverIDs(t, m.do(&admin, http.MethodGet, "/api/servers", nil).Body.String()); l["node-a~srv1"].Tag != "Cluster B" {
		t.Fatalf("tag not shown: %+v", l["node-a~srv1"])
	}

	// Unknown servers of a node and routes outside the table are not forwarded.
	if rec := m.do(&admin, http.MethodGet, "/api/servers/node-a~nope/configs", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown node server: %d", rec.Code)
	}
	if rec := m.do(&admin, http.MethodGet, "/api/servers/node-a~srv1/..%2f..%2fapi%2fadmin", nil); rec.Code != http.StatusNotFound {
		t.Errorf("traversal: %d", rec.Code)
	}
}

func TestNodeServerPermissions(t *testing.T) {
	m := newMultiNodeEnv(t, backend.AgentModeReadWrite)

	// "operator" is assigned to the local srv1 only.
	op := m.login("operator")
	if l := serverIDs(t, m.do(&op, http.MethodGet, "/api/servers", nil).Body.String()); len(l) != 1 {
		t.Fatalf("operator sees %v", l)
	}
	if rec := m.do(&op, http.MethodPost, "/api/servers/node-a~srv1/action", map[string]string{"action": "stop"}); !isPermissionDenied(rec) {
		t.Fatalf("unassigned node server: %d", rec.Code)
	}

	// Assigning the node gives access to all of its servers, with the user's permissions.
	if err := m.app.auth.SetUserNodes("operator", []string{"node-a"}); err != nil {
		t.Fatal(err)
	}
	op = m.login("operator")
	l := serverIDs(t, m.do(&op, http.MethodGet, "/api/servers", nil).Body.String())
	if _, ok := l["node-a~srv2"]; !ok {
		t.Fatalf("node assignment not applied: %v", l)
	}
	if rec := m.do(&op, http.MethodPost, "/api/servers/node-a~srv2/action", map[string]string{"action": "stop"}); rec.Code != http.StatusOK {
		t.Fatalf("stop on assigned node: %d %s", rec.Code, rec.Body.String())
	}
	for _, denied := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/servers/node-a~srv2/action", `{"action":"update"}`},
		{http.MethodPost, "/api/servers/node-a~srv2/action", `{"action":"restart"}`},
		{http.MethodGet, "/api/servers/node-a~srv2/configs", ""},
		{http.MethodPost, "/api/servers/node-a~srv2/delete", "{}"},
	} {
		if rec := m.do(&op, denied.method, denied.path, denied.body); !isPermissionDenied(rec) {
			t.Errorf("%s %s %s: %d %s", denied.method, denied.path, denied.body, rec.Code, rec.Body.String())
		}
	}

	// Removing the node removes its assignment.
	admin := m.login("admin2")
	if rec := m.do(&admin, http.MethodPost, "/api/admin/nodes/delete", map[string]string{"id": "node-a"}); rec.Code != http.StatusOK {
		t.Fatalf("delete node: %d %s", rec.Code, rec.Body.String())
	}
	if u, _ := m.app.auth.GetUser("operator"); len(u.Nodes) != 0 {
		t.Fatalf("node assignment kept: %v", u.Nodes)
	}
}

func TestNodeReadOnlyAndOffline(t *testing.T) {
	m := newMultiNodeEnv(t, backend.AgentModeReadOnly)
	admin := m.login("admin2")

	// The controller enforces the effective mode itself before forwarding.
	rec := m.do(&admin, http.MethodPost, "/api/servers/node-a~srv1/configs/file?path=serverfiles/server.cfg", map[string]string{"content": "x"})
	if !isReadOnlyDenial(rec) {
		t.Fatalf("write on read-only node: %d %s", rec.Code, rec.Body.String())
	}
	if rec := m.do(&admin, http.MethodPost, "/api/servers/node-a~srv1/action", map[string]string{"action": "update"}); !isReadOnlyDenial(rec) {
		t.Fatalf("update on read-only node: %d %s", rec.Code, rec.Body.String())
	}
	if rec := m.do(&admin, http.MethodPost, "/api/servers/node-a~srv1/action", map[string]string{"action": "restart"}); rec.Code != http.StatusOK {
		t.Fatalf("restart on read-only node: %d %s", rec.Code, rec.Body.String())
	}
	rec = m.do(&admin, http.MethodPost, "/api/servers/bulk", map[string]interface{}{"action": "update", "serverIds": []string{"node-a~srv1"}})
	if !strings.Contains(rec.Body.String(), "read-only") {
		t.Fatalf("bulk update on read-only node: %s", rec.Body.String())
	}

	// An offline node keeps its servers in the list, marked offline; requests fail clearly.
	m.stopAgent()
	m.waitFor(t, func() bool { return !m.app.agents.Hub.Status("node-a").Online })
	l := serverIDs(t, m.do(&admin, http.MethodGet, "/api/servers", nil).Body.String())
	if s, ok := l["node-a~srv1"]; !ok || !s.NodeOffline {
		t.Fatalf("offline node: %+v", s)
	}
	if rec := m.do(&admin, http.MethodGet, "/api/servers/node-a~srv1/console", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("request to offline node: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHardenNodeResponse(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{
			"Content-Type":                {"text/html; charset=utf-8"},
			"Set-Cookie":                  {"lgsm_session=evil; Path=/"},
			"Content-Security-Policy":     {"default-src *"},
			"Location":                    {"https://evil.example/"},
			"Content-Disposition":         {`inline; filename="../../etc/passwd"`},
			"Access-Control-Allow-Origin": {"*"},
			"Etag":                        {`"abc"`},
		},
		Body: io.NopCloser(strings.NewReader("<script>alert(1)</script>")),
	}
	if err := hardenNodeResponse(resp); err != nil {
		t.Fatal(err)
	}
	h := resp.Header
	if h.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("content type %q", h.Get("Content-Type"))
	}
	for _, k := range []string{"Set-Cookie", "Content-Security-Policy", "Location", "Access-Control-Allow-Origin"} {
		if h.Get(k) != "" {
			t.Errorf("%s passed through", k)
		}
	}
	if cd := h.Get("Content-Disposition"); cd != `attachment; filename=passwd` {
		t.Errorf("disposition %q", cd)
	}
	if h.Get("Etag") != `"abc"` {
		t.Error("ETag dropped")
	}

	resp = &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(""))}
	_ = hardenNodeResponse(resp)
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("SSE content type %q", resp.Header.Get("Content-Type"))
	}
}
