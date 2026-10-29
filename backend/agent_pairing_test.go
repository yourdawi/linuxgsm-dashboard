package backend

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type testController struct {
	dir     string
	ca      *ControllerCA
	nodes   *NodeRegistry
	gw      *AgentGateway
	tunnels chan tunnelEvent
	wsURL   string // http://... (WebSocket carrier)
	tlsURL  string // tls://host:port (direct carrier)
}

type tunnelEvent struct {
	nodeID string
	client *AgentTunnelClient
}

func newTestController(t *testing.T) *testController {
	t.Helper()
	dir := t.TempDir()
	ca, err := LoadOrCreateControllerCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := NewNodeRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &testController{dir: dir, ca: ca, nodes: nodes, tunnels: make(chan tunnelEvent, 8)}
	c.gw = NewAgentGateway(ca, nodes)
	c.gw.Limiter = NewLoginLimiter()
	c.gw.OnTunnel = func(nodeID string, client *AgentTunnelClient) {
		t.Cleanup(func() { client.Close() })
		c.tunnels <- tunnelEvent{nodeID, client}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != AgentConnectPath {
			http.NotFound(w, r)
			return
		}
		c.gw.HandleWebSocket(w, r, "192.0.2.1")
	}))
	t.Cleanup(srv.Close)
	c.wsURL = srv.URL
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go c.gw.Serve(l)
	c.tlsURL = "tls://" + l.Addr().String()
	return c
}

func (c *testController) pair(t *testing.T, name, mode string) PairingTicket {
	t.Helper()
	ticket, err := c.nodes.CreatePairing(name, mode, "")
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func joinOpts(url string, ticket PairingTicket, fp, mode string) AgentJoinOptions {
	return AgentJoinOptions{ControllerURL: url, Token: ticket.Token, CAFingerprint: fp, Mode: mode, Version: "1.2.0"}
}

func (c *testController) waitTunnel(t *testing.T) tunnelEvent {
	t.Helper()
	select {
	case ev := <-c.tunnels:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no tunnel arrived")
		return tunnelEvent{}
	}
}

func (c *testController) noTunnel(t *testing.T) {
	t.Helper()
	select {
	case ev := <-c.tunnels:
		t.Fatalf("unexpected tunnel from %s", ev.nodeID)
	case <-time.After(500 * time.Millisecond):
	}
}

func helloHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") })
}

// startAgent runs the connection loop of an agent until the test ends.
func startAgent(t *testing.T, dir string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RunAgent(ctx, func() (AgentIdentity, error) { return LoadAgentIdentity(dir) }, helloHandler(), t.Logf)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestAgentPairingAndTunnel(t *testing.T) {
	for _, carrier := range []string{"websocket", "direct"} {
		t.Run(carrier, func(t *testing.T) {
			c := newTestController(t)
			url := c.wsURL
			if carrier == "direct" {
				url = c.tlsURL
			}
			ticket := c.pair(t, "Game Box #1", AgentModeReadWrite)
			if ticket.NodeID != "game-box-1" {
				t.Fatalf("node ID %q", ticket.NodeID)
			}

			agentDir := filepath.Join(t.TempDir(), "agent")
			cfg, err := AgentJoin(context.Background(), agentDir, joinOpts(url, ticket, c.ca.Fingerprint(), AgentModeReadWrite))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.NodeID != "game-box-1" || cfg.Name != "Game Box #1" {
				t.Fatalf("config %+v", cfg)
			}
			if p := LoadAgentPolicy(agentDir); p.Mode != AgentModeReadWrite || !p.AllowSelfUpdate || p.AllowFirewall {
				t.Fatalf("policy %+v", p)
			}
			node, ok := c.nodes.Node("game-box-1")
			if !ok || len(node.Keys) != 1 || node.Mode != AgentModeReadWrite || node.Version != "1.2.0" {
				t.Fatalf("node %+v", node)
			}
			if runtime.GOOS == "linux" {
				for _, f := range []string{agentKeyFile, agentConfigFile, agentPolicyFile} {
					st, err := os.Stat(filepath.Join(agentDir, f))
					if err != nil || st.Mode().Perm() != 0600 {
						t.Fatalf("%s: %v %v", f, st.Mode(), err)
					}
				}
				if st, _ := os.Stat(filepath.Join(c.dir, agentCADirName, "ca.key")); st.Mode().Perm() != 0600 {
					t.Fatalf("ca.key mode %v", st.Mode())
				}
			}

			// The registry survives a restart of the controller.
			if reloaded, err := NewNodeRegistry(c.dir); err != nil || len(reloaded.Nodes()) != 1 {
				t.Fatalf("reload: %v", err)
			}

			startAgent(t, agentDir)
			ev := c.waitTunnel(t)
			if ev.nodeID != "game-box-1" {
				t.Fatalf("tunnel from %q", ev.nodeID)
			}
			if code, body := tunnelGet(t, ev.client, "/anything"); code != http.StatusOK || body != "hello" {
				t.Fatalf("tunnel request: %d %q", code, body)
			}

			// After the controller drops the tunnel, the agent reconnects.
			ev.client.Close()
			ev = c.waitTunnel(t)
			if code, _ := tunnelGet(t, ev.client, "/"); code != http.StatusOK {
				t.Fatal("no request after reconnect")
			}

			// A removed node cannot connect any more.
			if err := c.nodes.RemoveNode("game-box-1"); err != nil {
				t.Fatal(err)
			}
			ev.client.Close()
			assertRejected(t, agentDir)
			c.noTunnel(t)
		})
	}
}

func TestAgentPairingTokenRules(t *testing.T) {
	c := newTestController(t)
	fp := c.ca.Fingerprint()

	t.Run("single use", func(t *testing.T) {
		ticket := c.pair(t, "a", AgentModeReadOnly)
		if _, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(c.wsURL, ticket, fp, AgentModeReadOnly)); err != nil {
			t.Fatal(err)
		}
		_, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(c.wsURL, ticket, fp, AgentModeReadOnly))
		if err == nil || !strings.Contains(err.Error(), "invalid or expired") {
			t.Fatalf("second use: %v", err)
		}
	})

	t.Run("token format is forgiving", func(t *testing.T) {
		ticket := c.pair(t, "b", AgentModeReadOnly)
		ticket.Token = strings.ToLower(strings.ReplaceAll(ticket.Token, "-", ""))
		if _, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(c.wsURL, ticket, fp, AgentModeReadOnly)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		ticket := c.pair(t, "c", AgentModeReadOnly)
		c.nodes.now = func() time.Time { return time.Now().Add(AgentPairingValidity + time.Second) }
		defer func() { c.nodes.now = time.Now }()
		if _, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(c.wsURL, ticket, fp, AgentModeReadOnly)); err == nil {
			t.Fatal("expired token accepted")
		}
	})

	t.Run("wrong fingerprint does not consume the token", func(t *testing.T) {
		ticket := c.pair(t, "d", AgentModeReadOnly)
		wrong := "sha256:" + strings.Repeat("0", 64)
		_, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(c.wsURL, ticket, wrong, AgentModeReadOnly))
		if err == nil || !strings.Contains(err.Error(), "fingerprint") {
			t.Fatalf("wrong fingerprint: %v", err)
		}
		if _, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(c.wsURL, ticket, fp, AgentModeReadOnly)); err != nil {
			t.Fatalf("token was consumed by the failed attempt: %v", err)
		}
	})

	t.Run("impostor controller", func(t *testing.T) {
		impostor := newTestController(t)
		ticket := impostor.pair(t, "e", AgentModeReadOnly)
		// The agent pins the real controller's CA; the impostor has its own.
		if _, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(impostor.wsURL, ticket, fp, AgentModeReadOnly)); err == nil {
			t.Fatal("joined a controller with another CA")
		}
	})

	t.Run("mode is the lower of token and agent", func(t *testing.T) {
		ticket := c.pair(t, "f", AgentModeReadOnly)
		dir := t.TempDir()
		if _, err := AgentJoin(context.Background(), dir, joinOpts(c.wsURL, ticket, fp, AgentModeReadWrite)); err != nil {
			t.Fatal(err)
		}
		if p := LoadAgentPolicy(dir); p.Mode != AgentModeReadOnly {
			t.Fatalf("mode %s", p.Mode)
		}
		ticket = c.pair(t, "g", AgentModeReadWrite)
		dir = t.TempDir()
		if _, err := AgentJoin(context.Background(), dir, joinOpts(c.wsURL, ticket, fp, AgentModeReadOnly)); err != nil {
			t.Fatal(err)
		}
		if p := LoadAgentPolicy(dir); p.Mode != AgentModeReadOnly {
			t.Fatalf("mode %s", p.Mode)
		}
	})

	t.Run("failed attempts are throttled", func(t *testing.T) {
		c2 := newTestController(t)
		bad := PairingTicket{Token: "AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA"}
		for i := 0; i < 6; i++ {
			_, _ = AgentJoin(context.Background(), t.TempDir(), joinOpts(c2.wsURL, bad, c2.ca.Fingerprint(), AgentModeReadOnly))
		}
		good := c2.pair(t, "h", AgentModeReadOnly)
		_, err := AgentJoin(context.Background(), t.TempDir(), joinOpts(c2.wsURL, good, c2.ca.Fingerprint(), AgentModeReadOnly))
		if err == nil || !strings.Contains(err.Error(), "too many") {
			t.Fatalf("not throttled: %v", err)
		}
	})
}

func TestAgentRepairReplacesKeys(t *testing.T) {
	c := newTestController(t)
	fp := c.ca.Fingerprint()
	oldDir := t.TempDir()
	if _, err := AgentJoin(context.Background(), oldDir, joinOpts(c.wsURL, c.pair(t, "box", AgentModeReadWrite), fp, AgentModeReadWrite)); err != nil {
		t.Fatal(err)
	}
	repair, err := c.nodes.CreatePairing("", AgentModeReadWrite, "box")
	if err != nil {
		t.Fatal(err)
	}
	newDir := t.TempDir()
	cfg, err := AgentJoin(context.Background(), newDir, joinOpts(c.wsURL, repair, fp, AgentModeReadWrite))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "box" || len(c.nodes.Nodes()) != 1 {
		t.Fatalf("re-pairing created another node: %+v %+v", cfg, c.nodes.Nodes())
	}

	// Only the new installation can connect.
	startAgent(t, newDir)
	if ev := c.waitTunnel(t); ev.nodeID != "box" {
		t.Fatalf("tunnel from %q", ev.nodeID)
	}
	assertRejected(t, oldDir)
	c.noTunnel(t)
}

// assertRejected checks that the controller refuses the identity in dir with a TLS alert.
func assertRejected(t *testing.T, dir string) {
	t.Helper()
	id, err := LoadAgentIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ConnectAgentTunnel(context.Background(), id)
	if err == nil {
		// TLS 1.3 completes the client side first; the rejection arrives as an alert on the
		// first read. An accepted tunnel would just stay silent until the deadline.
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Read(make([]byte, 1))
		conn.Close()
	}
	var ne net.Error
	if err == nil || errors.As(err, &ne) && ne.Timeout() || !strings.Contains(err.Error(), "remote error") {
		t.Fatalf("expected the controller to reject the node, got %v", err)
	}
}

func TestControllerCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateControllerCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateControllerCA(dir)
	if err != nil || again.Fingerprint() != ca.Fingerprint() {
		t.Fatalf("CA not persisted: %v", err)
	}

	t.Run("tunnel certificate renewal", func(t *testing.T) {
		before := ca.serverLeaf.SerialNumber
		if err := ca.loadOrRenewServerCert(time.Now().Add(agentServerValidity / 2)); err != nil {
			t.Fatal(err)
		}
		if ca.serverLeaf.SerialNumber.Cmp(before) != 0 {
			t.Fatal("renewed too early")
		}
		if err := ca.loadOrRenewServerCert(time.Now().Add(agentServerRenewAfter + time.Hour)); err != nil {
			t.Fatal(err)
		}
		if ca.serverLeaf.SerialNumber.Cmp(before) == 0 {
			t.Fatal("not renewed when due")
		}
	})

	t.Run("CSR subject is ignored", func(t *testing.T) {
		csr := testCSR(t, "evil.example", true)
		der, err := ca.SignNodeCSR(csr, "node-a", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		if cert.Subject.CommonName != "node-a" || len(cert.DNSNames) != 0 || cert.IsCA ||
			len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
			t.Fatalf("issued %+v", cert.Subject)
		}
		if d := cert.NotAfter.Sub(cert.NotBefore); d != AgentNodeCertValidity+agentCertBackdate {
			t.Fatalf("validity %v", d)
		}
	})

	t.Run("non-Ed25519 CSR is rejected", func(t *testing.T) {
		if _, err := ca.SignNodeCSR(testCSR(t, "x", false), "node-a", time.Now()); err == nil {
			t.Fatal("ECDSA CSR accepted")
		}
	})
}

func testCSR(t *testing.T, cn string, ed bool) []byte {
	t.Helper()
	var key interface{}
	if ed {
		_, k, _ := ed25519.GenerateKey(rand.Reader)
		key = k
	} else {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		key = k
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestNodeIDsAndMachineID(t *testing.T) {
	cases := map[string]string{
		"Game Box #1":           "game-box-1",
		"  LXC 101 (Valheim)":   "lxc-101-valheim",
		"Ünïcödé":               "n-c-d",
		"###":                   "node",
		strings.Repeat("a", 50): strings.Repeat("a", 28),
	}
	for in, want := range cases {
		if got := nodeIDFromName(in); got != want || !IsValidNodeID(got) {
			t.Errorf("nodeIDFromName(%q) = %q, want %q", in, got, want)
		}
	}

	r, err := NewNodeRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		ticket, err := r.CreatePairing("Box", AgentModeReadOnly, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ticket.NodeID)
	}
	if fmt.Sprint(ids) != "[box box-2 box-3]" {
		t.Fatalf("pending pairings got IDs %v", ids)
	}

	r.nodes["box"] = &Node{ID: "box", MachineID: "aaaa"}
	if err := r.CheckMachineID("box", "aaaa"); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckMachineID("box", "bbbb"); err != ErrNodeMachineMismatch {
		t.Fatalf("clone not detected: %v", err)
	}
	if err := r.CheckMachineID("box", ""); err != nil {
		t.Fatalf("agent without machine ID: %v", err)
	}
}
