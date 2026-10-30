package backend

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Agent tunnel: one long-lived connection between an agent and the controller. The agent dials
// the controller and is the TLS client, but inside the tunnel the roles are reversed: the agent
// serves HTTP/2 and the controller sends the requests. Both directions use the HTTP/2
// implementation of the standard library:
//
//   - Agent: http.Server.Serve on a listener that returns the established *tls.Conn once. Because
//     ALPN negotiated "h2", the server hands the connection to its HTTP/2 implementation.
//   - Controller: an http.Transport whose DialTLSContext returns that same *tls.Conn exactly once.
//     With ForceAttemptHTTP2 the transport runs HTTP/2 on it and multiplexes every request over
//     it. Later dials fail, so a dead tunnel never turns into a new connection somewhere else.
//
// Liveness is an application level ping (AgentTunnelPingPath) that the controller sends and the
// agent's watchdog expects. HTTP/2 PING frames (http.HTTP2Config) would only tell the side that
// sends them; the application ping lets both sides notice a dead tunnel.

const (
	// AgentTunnelServerName is the fixed DNS name in the controller's tunnel certificate. The
	// agent verifies this name instead of the address it dialed, so a LAN IP, a DNS name and a
	// dynamic DNS name all work with the same pairing.
	AgentTunnelServerName = "controller.lgsm.internal"
	// AgentTunnelPingPath is answered by the tunnel itself on the agent side.
	AgentTunnelPingPath = "/agent/v1/ping"

	agentTunnelALPN = "h2"
	// agentTunnelHandshakeTimeout limits the inner TLS handshake.
	agentTunnelHandshakeTimeout = 10 * time.Second
	// agentTunnelMaxStreams stays below the 250 concurrent streams the standard HTTP/2 server
	// allows. Above that limit the client transport would try to dial a second connection,
	// which a tunnel cannot provide, so further requests wait instead.
	agentTunnelMaxStreams = 200
)

// ErrAgentTunnelClosed is returned for requests on a tunnel that is no longer connected.
var ErrAgentTunnelClosed = errors.New("agent tunnel closed")

func agentTunnelTLSBase() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Both ends are this binary, so the post-quantum hybrid key exchange can be required:
		// recorded tunnel traffic stays protected against a future quantum computer.
		CurvePreferences: []tls.CurveID{tls.X25519MLKEM768},
		NextProtos:       []string{agentTunnelALPN},
	}
}

// AgentTunnelClientTLSConfig is the TLS configuration of the agent: it presents the node
// certificate and trusts nothing but the controller CA.
func AgentTunnelClientTLSConfig(controllerCA *x509.CertPool, nodeCert tls.Certificate) *tls.Config {
	cfg := agentTunnelTLSBase()
	cfg.RootCAs = controllerCA
	cfg.ServerName = AgentTunnelServerName
	cfg.Certificates = []tls.Certificate{nodeCert}
	return cfg
}

// AgentTunnelServerTLSConfig is the TLS configuration of the controller. Client certificates
// must chain to the controller CA; verifyNode additionally decides whether the verified leaf
// belongs to a paired node (allowlist check), so removed nodes are rejected during the handshake.
func AgentTunnelServerTLSConfig(controllerCA *x509.CertPool, serverCert tls.Certificate, verifyNode func(leaf *x509.Certificate) error) *tls.Config {
	cfg := agentTunnelTLSBase()
	cfg.Certificates = []tls.Certificate{serverCert}
	cfg.ClientCAs = controllerCA
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	cfg.SessionTicketsDisabled = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("agent tunnel: no client certificate")
		}
		if verifyNode == nil {
			return nil
		}
		return verifyNode(cs.PeerCertificates[0])
	}
	return cfg
}

// tunnelHandshake runs the inner TLS handshake on raw and checks that HTTP/2 was negotiated.
func tunnelHandshake(ctx context.Context, conn *tls.Conn) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, agentTunnelHandshakeTimeout)
		defer cancel()
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("agent tunnel handshake: %w", err)
	}
	if p := conn.ConnectionState().NegotiatedProtocol; p != agentTunnelALPN {
		return fmt.Errorf("agent tunnel handshake: protocol %q negotiated instead of %q", p, agentTunnelALPN)
	}
	return nil
}

// DialAgentTunnel runs the agent side of the inner TLS handshake on an established carrier
// connection (TCP or WebSocket). raw is closed when the handshake fails.
func DialAgentTunnel(ctx context.Context, raw net.Conn, cfg *tls.Config) (*tls.Conn, error) {
	conn := tls.Client(raw, cfg)
	if err := tunnelHandshake(ctx, conn); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// AcceptAgentTunnel runs the controller side of the inner TLS handshake on an accepted carrier
// connection and returns a client for requests to the agent together with the verified node
// certificate. raw is closed when the handshake fails.
func AcceptAgentTunnel(ctx context.Context, raw net.Conn, cfg *tls.Config) (*AgentTunnelClient, *x509.Certificate, error) {
	conn := tls.Server(raw, cfg)
	if err := tunnelHandshake(ctx, conn); err != nil {
		raw.Close()
		return nil, nil, err
	}
	leaf := conn.ConnectionState().PeerCertificates[0]
	return newAgentTunnelClient(conn), leaf, nil
}

// -------------------------------------------------------------
// Agent side
// -------------------------------------------------------------

// singleConnListener hands out one connection and then blocks until it is closed.
type singleConnListener struct {
	conns     chan net.Conn
	addr      net.Addr
	done      chan struct{}
	closeOnce sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	l := &singleConnListener{conns: make(chan net.Conn, 1), addr: conn.LocalAddr(), done: make(chan struct{})}
	l.conns <- conn
	return l
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *singleConnListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.addr }

// ServeAgentTunnel serves h over an established tunnel on the agent side and blocks until the
// tunnel ends. The tunnel answers AgentTunnelPingPath itself. When idle is positive, the tunnel
// is closed after that long without any request; the controller pings regularly, so silence
// means the connection is dead even if TCP has not noticed yet.
func ServeAgentTunnel(conn *tls.Conn, h http.Handler, idle time.Duration) error {
	if p := conn.ConnectionState().NegotiatedProtocol; p != agentTunnelALPN {
		conn.Close()
		return fmt.Errorf("agent tunnel: protocol %q negotiated instead of %q", p, agentTunnelALPN)
	}
	var lastRequest int64
	atomic.StoreInt64(&lastRequest, time.Now().UnixNano())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt64(&lastRequest, time.Now().UnixNano())
		if r.URL.Path == AgentTunnelPingPath {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})

	l := newSingleConnListener(conn)
	srv := &http.Server{
		Handler: handler,
		// No IdleTimeout and ReadTimeout: the HTTP/2 server would close the idle tunnel.
		ReadHeaderTimeout: 30 * time.Second,
		MaxHeaderBytes:    1 << 20,
		// A rejected or dropped tunnel is reported by RunAgent; the server's own messages
		// ("error reading preface") would only repeat it.
		ErrorLog: log.New(io.Discard, "", 0),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed || state == http.StateHijacked {
				l.Close()
			}
		},
	}

	stopWatchdog := make(chan struct{})
	defer close(stopWatchdog)
	if idle > 0 {
		go func() {
			tick := time.NewTicker(idle / 4)
			defer tick.Stop()
			for {
				select {
				case <-stopWatchdog:
					return
				case <-tick.C:
					if time.Since(time.Unix(0, atomic.LoadInt64(&lastRequest))) > idle {
						conn.Close()
						return
					}
				}
			}
		}()
	}

	err := srv.Serve(l)
	conn.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// -------------------------------------------------------------
// Controller side
// -------------------------------------------------------------

// AgentTunnelClient sends HTTP requests to an agent over its tunnel.
type AgentTunnelClient struct {
	conn      *tls.Conn
	transport *http.Transport
	streams   chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newAgentTunnelClient(conn *tls.Conn) *AgentTunnelClient {
	t := &AgentTunnelClient{
		conn:    conn,
		streams: make(chan struct{}, agentTunnelMaxStreams),
		done:    make(chan struct{}),
	}
	var dialed int32
	t.transport = &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if atomic.CompareAndSwapInt32(&dialed, 0, 1) {
				return conn, nil
			}
			return nil, ErrAgentTunnelClosed
		},
		// Bodies are passed through unchanged; actions stream for minutes, so no header timeout.
		DisableCompression: true,
		MaxIdleConns:       1,
	}
	return t
}

// RemoteAddr is the carrier address of the agent as seen by the controller.
func (t *AgentTunnelClient) RemoteAddr() net.Addr { return t.conn.RemoteAddr() }

// Done is closed when the tunnel has been closed.
func (t *AgentTunnelClient) Done() <-chan struct{} { return t.done }

// Close ends the tunnel. Running requests fail.
func (t *AgentTunnelClient) Close() error {
	t.closeOnce.Do(func() {
		close(t.done)
		t.conn.Close()
		t.transport.CloseIdleConnections()
	})
	return nil
}

// Do sends req to the agent. Only path and query of req.URL are used. At most
// agentTunnelMaxStreams requests run at the same time; further requests wait for a free stream
// or until their context ends. The response body must be closed.
func (t *AgentTunnelClient) Do(req *http.Request) (*http.Response, error) {
	select {
	case t.streams <- struct{}{}:
	case <-t.done:
		return nil, ErrAgentTunnelClosed
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	out := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = "https"
	u.Host = "agent"
	out.URL = &u
	out.Host = "agent"
	out.RequestURI = ""

	resp, err := t.transport.RoundTrip(out)
	if err != nil {
		<-t.streams
		select {
		case <-t.done:
			return nil, ErrAgentTunnelClosed
		default:
		}
		if errors.Is(err, ErrAgentTunnelClosed) {
			t.Close()
		}
		return nil, err
	}
	resp.Body = &tunnelBody{ReadCloser: resp.Body, release: func() { <-t.streams }}
	return resp, nil
}

// tunnelBody frees the stream slot when the response body is closed.
type tunnelBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *tunnelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

// Ping checks that the agent answers on the tunnel.
func (t *AgentTunnelClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, AgentTunnelPingPath, nil)
	if err != nil {
		return err
	}
	resp, err := t.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("agent tunnel ping: unexpected status %s", resp.Status)
	}
	return nil
}

// KeepAlive pings the agent every interval until the tunnel is closed. After failures
// consecutive failed pings (each limited to timeout) it closes the tunnel. Several failures are
// tolerated because a ping can queue behind a large upload on a slow uplink.
func (t *AgentTunnelClient) KeepAlive(interval, timeout time.Duration, failures int) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	failed := 0
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err := t.Ping(ctx)
			cancel()
			if err == nil {
				failed = 0
				continue
			}
			failed++
			if failed >= failures || errors.Is(err, ErrAgentTunnelClosed) {
				t.Close()
				return
			}
		}
	}
}

// PeerCertificate returns the node certificate the tunnel was opened with.
func (t *AgentTunnelClient) PeerCertificate() *x509.Certificate {
	if t.conn == nil {
		return nil
	}
	certs := t.conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	return certs[0]
}
