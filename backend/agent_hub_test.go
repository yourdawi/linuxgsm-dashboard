package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// helloAgent answers hello with the given values and 204 for everything else.
func helloAgent(hello AgentHello) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != AgentHelloPath {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hello)
	})
}

// hubSetup pairs one node "box" (controller mode) with a hub running controllerVersion.
func hubSetup(t *testing.T, controllerVersion, mode string) (*testController, *AgentHub, string) {
	t.Helper()
	c := newTestController(t)
	hub := NewAgentHub(c.nodes, controllerVersion, t.Logf)
	c.gw.OnTunnel = hub.Attach
	dir := t.TempDir()
	if _, err := AgentJoin(context.Background(), dir, joinOpts(c.wsURL, c.pair(t, "box", mode), c.ca.Fingerprint(), AgentModeReadWrite)); err != nil {
		t.Fatal(err)
	}
	return c, hub, dir
}

func runTestAgent(t *testing.T, dir string, h http.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RunAgent(ctx, func() (AgentIdentity, error) { return LoadAgentIdentity(dir) }, h, t.Logf)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitStatus polls the hub until cond holds.
func waitStatus(t *testing.T, hub *AgentHub, cond func(NodeStatus) bool) NodeStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := hub.Status("box")
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("status not reached, last: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentHubHello(t *testing.T) {
	t.Run("compatible", func(t *testing.T) {
		_, hub, dir := hubSetup(t, "1.4.2", AgentModeReadOnly)
		runTestAgent(t, dir, helloAgent(AgentHello{Version: "1.4.0", Protocol: AgentProtocolVersion, Mode: AgentModeReadWrite,
			Servers: []string{"vhserver", "../evil"}}))
		st := waitStatus(t, hub, func(s NodeStatus) bool { return s.Hello != nil })
		if !st.Online || !st.Compatible || st.Problem != "" {
			t.Fatalf("status %+v", st)
		}
		// The controller allows read-only, so read-only it is.
		if st.EffectiveMode != AgentModeReadOnly {
			t.Fatalf("effective mode %s", st.EffectiveMode)
		}
		if len(st.Hello.Servers) != 1 || st.Hello.Servers[0] != "vhserver" {
			t.Fatalf("invalid server IDs from the node were kept: %v", st.Hello.Servers)
		}
	})

	t.Run("version mismatch stays connected", func(t *testing.T) {
		c, hub, dir := hubSetup(t, "1.5.0", AgentModeReadWrite)
		runTestAgent(t, dir, helloAgent(AgentHello{Version: "1.4.9", Protocol: AgentProtocolVersion, Mode: AgentModeReadWrite}))
		st := waitStatus(t, hub, func(s NodeStatus) bool { return s.Hello != nil })
		if !st.Online || st.Compatible || !strings.Contains(st.Problem, "update required") {
			t.Fatalf("status %+v", st)
		}
		if n, _ := c.nodes.Node("box"); n.Version != "1.4.9" {
			t.Fatalf("version not recorded: %q", n.Version)
		}
	})

	t.Run("cloned machine is disconnected", func(t *testing.T) {
		c, hub, dir := hubSetup(t, "1.4.0", AgentModeReadWrite)
		c.nodes.mu.Lock()
		c.nodes.nodes["box"].MachineID = "0123456789abcdef0123456789abcdef"
		c.nodes.mu.Unlock()
		runTestAgent(t, dir, helloAgent(AgentHello{Version: "1.4.0", Protocol: AgentProtocolVersion, MachineID: "ffffffffffffffffffffffffffffffff"}))
		st := waitStatus(t, hub, func(s NodeStatus) bool { return s.Problem != "" && !s.Online })
		if !strings.Contains(st.Problem, "another machine") {
			t.Fatalf("status %+v", st)
		}
	})

	t.Run("agent without hello is disconnected", func(t *testing.T) {
		_, hub, dir := hubSetup(t, "1.4.0", AgentModeReadWrite)
		runTestAgent(t, dir, http.NotFoundHandler())
		st := waitStatus(t, hub, func(s NodeStatus) bool { return s.Problem != "" && !s.Online })
		if !strings.Contains(st.Problem, "hello") {
			t.Fatalf("status %+v", st)
		}
	})
}

func TestAgentVersionsCompatible(t *testing.T) {
	cases := []struct {
		controller, agent string
		want              bool
	}{
		{"1.4.0", "1.4.7", true},
		{"v1.4.0", "1.4.0", true},
		{"1.4.0", "1.5.0", false},
		{"2.0.0", "1.9.9", false},
		{"dev-main-1a2b3c4", "dev-main-1a2b3c4", true},
		{"dev-main-1a2b3c4", "dev-main-9z9z9z9", false},
		{"1.4.0", "dev-main-1a2b3c4", false},
		{"dev", "dev", true},
		{"", "", false},
	}
	for _, c := range cases {
		if got := AgentVersionsCompatible(c.controller, c.agent); got != c.want {
			t.Errorf("AgentVersionsCompatible(%q, %q) = %v", c.controller, c.agent, got)
		}
	}
}
