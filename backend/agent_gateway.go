package backend

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// AgentGateway is the controller's endpoint for agents. Every carrier connection (WebSocket on
// the web port or the direct agent port) starts with the inner TLS handshake; the negotiated
// ALPN protocol then decides what the connection is:
//
//   - AgentEnrollALPN without client certificate: one pairing request (EnrollRequest).
//   - "h2" with a paired node certificate: the tunnel of that node.
//
// Everything else is closed.

// AgentEnrollALPN is the ALPN protocol of pairing connections.
const AgentEnrollALPN = "lgsm-enroll/1"

const (
	// maxAgentHandshakes bounds concurrent unauthenticated handshakes; maxAgentHandshakesPerIP keeps
	// a single address from taking all of them (addresses of a local reverse proxy are exempt,
	// because every agent behind it shares that address).
	maxAgentHandshakes      = 64
	maxAgentHandshakesPerIP = 4
	agentEnrollTimeout      = 15 * time.Second
	maxEnrollBody           = 64 << 10
)

// AgentGateway accepts agent connections for the controller.
type AgentGateway struct {
	CA    *ControllerCA
	Nodes *NodeRegistry
	// Limiter throttles failed pairing attempts per source IP (nil: no throttling).
	Limiter *LoginLimiter
	// OnTunnel receives every established node tunnel. It owns the client from then on.
	OnTunnel func(nodeID string, client *AgentTunnelClient)
	// Logf receives log lines (nil: discarded).
	Logf func(format string, args ...interface{})

	handshakes chan struct{}
	perIPMu    sync.Mutex
	perIP      map[string]int
}

// NewAgentGateway creates a gateway.
func NewAgentGateway(ca *ControllerCA, nodes *NodeRegistry) *AgentGateway {
	return &AgentGateway{CA: ca, Nodes: nodes, handshakes: make(chan struct{}, maxAgentHandshakes), perIP: make(map[string]int)}
}

func (g *AgentGateway) logf(format string, args ...interface{}) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

// HandleWebSocket serves the WebSocket carrier. clientIP is the address used for throttling.
// Pairing failures are throttled in handleEnroll only, so paired nodes behind the same address
// keep connecting.
func (g *AgentGateway) HandleWebSocket(w http.ResponseWriter, r *http.Request, clientIP string) {
	conn, err := AcceptWebSocket(w, r)
	if err != nil {
		return
	}
	g.HandleCarrier(conn, clientIP)
}

// Serve accepts direct carrier connections from l until it is closed.
func (g *AgentGateway) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		go g.HandleCarrier(conn, ip)
	}
}

func agentEnrollLimiterKey(ip string) string { return "agent-enroll:" + ip }

// HandleCarrier runs the inner handshake on a carrier connection and dispatches it.
func (g *AgentGateway) HandleCarrier(raw net.Conn, clientIP string) {
	if !g.acquireHandshake(clientIP) {
		raw.Close()
		return
	}
	conn := tls.Server(raw, g.CA.ServerTLSConfig(g.Nodes.VerifyNodeCert))
	ctx, cancel := context.WithTimeout(context.Background(), agentTunnelHandshakeTimeout)
	err := conn.HandshakeContext(ctx)
	cancel()
	g.releaseHandshake(clientIP)
	if err != nil {
		g.logf("[AGENT] Handshake from %s failed: %v", clientIP, err)
		raw.Close()
		return
	}

	state := conn.ConnectionState()
	switch {
	case state.NegotiatedProtocol == AgentEnrollALPN && len(state.PeerCertificates) == 0:
		g.handleEnroll(conn, clientIP)
	case state.NegotiatedProtocol == agentTunnelALPN && len(state.PeerCertificates) > 0:
		nodeID := state.PeerCertificates[0].Subject.CommonName
		if g.OnTunnel == nil {
			conn.Close()
			return
		}
		g.OnTunnel(nodeID, newAgentTunnelClient(conn))
	default:
		g.logf("[AGENT] Rejected connection from %s (protocol %q)", clientIP, state.NegotiatedProtocol)
		conn.Close()
	}
}

// perIPExempt reports whether an address is a local proxy, where all agents share one address.
func perIPExempt(clientIP string) bool {
	ip := net.ParseIP(clientIP)
	return ip == nil || ip.IsLoopback()
}

// acquireHandshake takes a slot for an unauthenticated handshake, globally and per address.
func (g *AgentGateway) acquireHandshake(clientIP string) bool {
	if !perIPExempt(clientIP) {
		g.perIPMu.Lock()
		if g.perIP[clientIP] >= maxAgentHandshakesPerIP {
			g.perIPMu.Unlock()
			return false
		}
		g.perIP[clientIP]++
		g.perIPMu.Unlock()
	}
	select {
	case g.handshakes <- struct{}{}:
		return true
	default:
		g.releasePerIP(clientIP)
		return false
	}
}

func (g *AgentGateway) releaseHandshake(clientIP string) {
	<-g.handshakes
	g.releasePerIP(clientIP)
}

func (g *AgentGateway) releasePerIP(clientIP string) {
	if perIPExempt(clientIP) {
		return
	}
	g.perIPMu.Lock()
	if g.perIP[clientIP]--; g.perIP[clientIP] <= 0 {
		delete(g.perIP, clientIP)
	}
	g.perIPMu.Unlock()
}

func (g *AgentGateway) handleEnroll(conn *tls.Conn, clientIP string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(agentEnrollTimeout))
	reply := func(resp EnrollResponse) {
		_ = json.NewEncoder(conn).Encode(resp)
	}
	key := agentEnrollLimiterKey(clientIP)
	if g.Limiter != nil {
		if ok, _ := g.Limiter.Allow(key); !ok {
			reply(EnrollResponse{Error: "too many failed pairing attempts, try again later"})
			return
		}
	}
	var req EnrollRequest
	dec := json.NewDecoder(io.LimitReader(conn, maxEnrollBody))
	if err := dec.Decode(&req); err != nil {
		reply(EnrollResponse{Error: "invalid pairing request"})
		return
	}
	resp, err := g.Nodes.Enroll(req, g.CA)
	if err != nil {
		if g.Limiter != nil {
			g.Limiter.Failure(key)
		}
		g.logf("[AGENT] Pairing from %s failed: %v", clientIP, err)
		msg := err.Error()
		if errors.Is(err, ErrInvalidPairingToken) {
			msg = ErrInvalidPairingToken.Error()
		}
		reply(EnrollResponse{Error: msg})
		return
	}
	if g.Limiter != nil {
		g.Limiter.Success(key)
	}
	g.logf("[AGENT] Node %s paired from %s (%s)", resp.NodeID, clientIP, resp.Mode)
	reply(resp)
}

// -------------------------------------------------------------
// Agent side of pairing
// -------------------------------------------------------------

// enrollTLSConfig is used by an agent that does not know the controller CA yet. The controller
// sends its CA certificate along with the tunnel certificate; the chain is accepted only when
// that CA matches caFingerprint from the pairing command and the tunnel certificate is valid
// under it.
func enrollTLSConfig(caFingerprint string) *tls.Config {
	cfg := agentTunnelTLSBase()
	cfg.NextProtos = []string{AgentEnrollALPN}
	// Verification is done in VerifyConnection against the pinned CA instead of system roots.
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		_, err := verifyPinnedChain(cs.PeerCertificates, caFingerprint)
		return err
	}
	return cfg
}

// verifyPinnedChain finds the CA with the pinned fingerprint in certs and verifies the leaf
// against it. It returns the CA certificate.
func verifyPinnedChain(certs []*x509.Certificate, caFingerprint string) (*x509.Certificate, error) {
	if len(certs) == 0 {
		return nil, errors.New("controller sent no certificate")
	}
	var ca *x509.Certificate
	for _, c := range certs[1:] {
		if c.IsCA && CertFingerprint(c.Raw) == caFingerprint {
			ca = c
		}
	}
	if ca == nil {
		return nil, errors.New("the controller's CA does not match the fingerprint of the pairing command")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := certs[0].Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   AgentTunnelServerName,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, fmt.Errorf("controller certificate: %w", err)
	}
	return ca, nil
}

// enrollOverCarrier sends req over an established carrier connection and returns the answer.
func enrollOverCarrier(ctx context.Context, raw net.Conn, caFingerprint string, req EnrollRequest) (EnrollResponse, error) {
	conn := tls.Client(raw, enrollTLSConfig(caFingerprint))
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(agentEnrollTimeout + agentTunnelHandshakeTimeout))
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return EnrollResponse{}, fmt.Errorf("pairing handshake: %w", err)
	}
	if p := conn.ConnectionState().NegotiatedProtocol; p != AgentEnrollALPN {
		return EnrollResponse{}, fmt.Errorf("controller does not support pairing (protocol %q)", p)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return EnrollResponse{}, err
	}
	var resp EnrollResponse
	if err := json.NewDecoder(bufio.NewReader(io.LimitReader(conn, maxEnrollBody))).Decode(&resp); err != nil {
		return EnrollResponse{}, fmt.Errorf("pairing response: %w", err)
	}
	if resp.Error != "" {
		return EnrollResponse{}, errors.New(resp.Error)
	}
	return resp, nil
}
