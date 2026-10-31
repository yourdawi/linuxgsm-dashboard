package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yourdawi/linuxgsm-dashboard/backend"
)

func TestIsCanonicalFastDLPath(t *testing.T) {
	for _, ok := range []string{"maps/de_dust2.bsp.bz2", "a", "sound/x y.wav"} {
		if !isCanonicalFastDLPath(ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"", "/a", "a/", "a//b", ".", "..", "../api/servers", "a/../b", "a/./b", `a\b`, "a\x00b", "a\nb"} {
		if isCanonicalFastDLPath(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestNodeFastDLRejectsDotSegments(t *testing.T) {
	m := newMultiNodeEnv(t, backend.AgentModeReadWrite)
	for _, p := range []string{
		"/fastdl/node-a~srv1/..%2F..%2Fapi%2Fservers%2Fsrv1%2Ffiles%2Fdownload?path=x",
		"/fastdl/node-a~srv1/%2e%2e/%2e%2e/api/servers",
		"/fastdl/node-a~srv1/maps%2F..%2Fmaps%2Fde_test.bsp",
	} {
		rec := httptest.NewRecorder()
		m.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	m.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fastdl/node-a~srv1/maps/de_test.bsp", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("canonical path: %d", rec.Code)
	}
}

func TestNodeRequestLimiter(t *testing.T) {
	var l nodeRequestLimiter
	var releases []func()
	for i := 0; i < maxNodeRequestsPerUser; i++ {
		rel, ok := l.acquire("n", "alice")
		if !ok {
			t.Fatalf("slot %d refused", i)
		}
		releases = append(releases, rel)
	}
	if _, ok := l.acquire("n", "alice"); ok {
		t.Fatal("limit not enforced")
	}
	if rel, ok := l.acquire("n", "bob"); !ok {
		t.Fatal("other user blocked")
	} else {
		rel()
	}
	if rel, ok := l.acquire("m", "alice"); !ok {
		t.Fatal("other node blocked")
	} else {
		rel()
	}
	releases[0]()
	if _, ok := l.acquire("n", "alice"); !ok {
		t.Fatal("released slot not reusable")
	}
}

func TestHardenNodeResponseSetsSecurityHeaders(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusFound} {
		resp := &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": {"text/html"}, "Content-Security-Policy": {"default-src *"}},
			Body:       io.NopCloser(strings.NewReader("x")),
		}
		if err := hardenNodeResponse(resp); err != nil {
			t.Fatal(err)
		}
		if resp.Header.Get("Content-Security-Policy") != sandboxCSP || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
			resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("status %d: headers %v", status, resp.Header)
		}
	}
}

// Pairing a node again (new machine, stolen key) must cut off the old installation at once.
func TestRepairDisconnectsOldInstallation(t *testing.T) {
	m := newMultiNodeEnv(t, backend.AgentModeReadWrite)
	old, ok := m.app.agents.Hub.Tunnel("node-a")
	if !ok {
		t.Fatal("node not connected")
	}
	ticket, err := m.app.agents.Nodes.CreatePairing("Node A", backend.AgentModeReadWrite, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentJoin(context.Background(), filepath.Join(t.TempDir(), "agent2"), backend.AgentJoinOptions{
		ControllerURL: m.gwURL, Token: ticket.Token, CAFingerprint: m.caFP,
		Mode: backend.AgentModeReadWrite, Version: Version,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the old installation is still connected")
	}
	// It keeps trying with its old key and is refused.
	time.Sleep(3 * time.Second)
	if _, ok := m.app.agents.Hub.Tunnel("node-a"); ok {
		t.Fatal("the old installation connected again")
	}
}

// Behind nginx ($proxy_add_x_forwarded_for) only the rightmost entry is trustworthy.
func TestClientIPBehindProxy(t *testing.T) {
	a := &app{trustProxy: true}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	if got := a.clientIP(r); got != "203.0.113.9" {
		t.Errorf("got %s", got)
	}
	r.Header.Set("X-Forwarded-For", "garbage")
	if got := a.clientIP(r); got != "127.0.0.1" {
		t.Errorf("invalid header: got %s", got)
	}
	a.trustProxy = false
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := a.clientIP(r); got != "127.0.0.1" {
		t.Errorf("header used without -trust-proxy: %s", got)
	}
}
