package backend

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// -------------------------------------------------------------
// Test PKI
// -------------------------------------------------------------

type tunnelPKI struct {
	pool       *x509.CertPool
	serverCert tls.Certificate
	nodeCert   tls.Certificate
}

func newTestCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test controller CA"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return ca, key
}

func newTestLeaf(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, cn string, usage x509.ExtKeyUsage, dns []string) tls.Certificate {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dns,
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newTunnelPKI(t *testing.T) tunnelPKI {
	t.Helper()
	ca, caKey := newTestCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return tunnelPKI{
		pool:       pool,
		serverCert: newTestLeaf(t, ca, caKey, "controller", x509.ExtKeyUsageServerAuth, []string{AgentTunnelServerName}),
		nodeCert:   newTestLeaf(t, ca, caKey, "node-1", x509.ExtKeyUsageClientAuth, nil),
	}
}

// -------------------------------------------------------------
// Carriers
// -------------------------------------------------------------

// tunnelPair is a connected tunnel: client is the controller side, serveErr reports when the
// agent side has stopped serving.
type tunnelPair struct {
	client   *AgentTunnelClient
	leaf     *x509.Certificate
	agentRaw net.Conn
	serveErr chan error
}

type carrier struct {
	name string
	// connect returns the agent's carrier connection; accepted delivers the controller's.
	connect func(t *testing.T) (agentRaw net.Conn, accepted <-chan net.Conn)
}

func directCarrier() carrier {
	return carrier{name: "direct", connect: func(t *testing.T) (net.Conn, <-chan net.Conn) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		accepted := make(chan net.Conn, 1)
		go func() {
			c, err := l.Accept()
			if err == nil {
				accepted <- c
			}
		}()
		c, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return c, accepted
	}}
}

// wsController is the controller's WebSocket endpoint.
func wsController(accepted chan<- net.Conn) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/connect" {
			http.NotFound(w, r)
			return
		}
		c, err := AcceptWebSocket(w, r)
		if err == nil {
			accepted <- c
		}
	})
}

func webSocketCarrier() carrier {
	return carrier{name: "websocket", connect: func(t *testing.T) (net.Conn, <-chan net.Conn) {
		accepted := make(chan net.Conn, 1)
		srv := httptest.NewServer(wsController(accepted))
		t.Cleanup(srv.Close)
		c, err := DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/agent/connect", nil)
		if err != nil {
			t.Fatal(err)
		}
		return c, accepted
	}}
}

// proxyCarrier puts a TLS-terminating reverse proxy between agent and controller, like nginx or
// Caddy in front of the dashboard. The proxy sees only the inner TLS session.
func proxyCarrier() carrier {
	return carrier{name: "websocket-via-tls-proxy", connect: func(t *testing.T) (net.Conn, <-chan net.Conn) {
		accepted := make(chan net.Conn, 1)
		backend := httptest.NewServer(wsController(accepted))
		t.Cleanup(backend.Close)
		target, _ := url.Parse(backend.URL)
		proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
		t.Cleanup(proxy.Close)
		pool := x509.NewCertPool()
		pool.AddCert(proxy.Certificate())
		c, err := DialWebSocket(context.Background(), "wss"+strings.TrimPrefix(proxy.URL, "https")+"/agent/connect",
			&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		return c, accepted
	}}
}

func allCarriers() []carrier {
	return []carrier{directCarrier(), webSocketCarrier(), proxyCarrier()}
}

func openTunnel(t *testing.T, cr carrier, pki tunnelPKI, h http.Handler, idle time.Duration) *tunnelPair {
	t.Helper()
	agentRaw, accepted := cr.connect(t)

	type acceptResult struct {
		client *AgentTunnelClient
		leaf   *x509.Certificate
		err    error
	}
	results := make(chan acceptResult, 1)
	go func() {
		select {
		case raw := <-accepted:
			client, leaf, err := AcceptAgentTunnel(context.Background(), raw, AgentTunnelServerTLSConfig(pki.pool, pki.serverCert, nil))
			results <- acceptResult{client, leaf, err}
		case <-time.After(10 * time.Second):
			results <- acceptResult{err: errors.New("carrier not accepted")}
		}
	}()

	conn, err := DialAgentTunnel(context.Background(), agentRaw, AgentTunnelClientTLSConfig(pki.pool, pki.nodeCert))
	if err != nil {
		t.Fatalf("agent handshake: %v", err)
	}
	res := <-results
	if res.err != nil {
		t.Fatalf("controller handshake: %v", res.err)
	}
	p := &tunnelPair{client: res.client, leaf: res.leaf, agentRaw: agentRaw, serveErr: make(chan error, 1)}
	go func() { p.serveErr <- ServeAgentTunnel(conn, h, idle) }()
	t.Cleanup(func() {
		p.client.Close()
		agentRaw.Close()
	})
	return p
}

func tunnelGet(t *testing.T, c *AgentTunnelClient, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s body: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

// -------------------------------------------------------------
// Agent handler used by the tests
// -------------------------------------------------------------

// patternReader produces a deterministic pseudo-random stream quickly.
type patternReader struct{ rng *mrand.Rand }

func (p patternReader) Read(b []byte) (int, error) { return p.rng.Read(b) }

func pattern(seed int64, n int64) io.Reader {
	return io.LimitReader(patternReader{mrand.New(mrand.NewSource(seed))}, n)
}

func testTunnelSize() int64 {
	if os.Getenv("LGSM_TUNNEL_BIG") != "" {
		return 1 << 30
	}
	return 64 << 20
}

type testAgent struct {
	mux          *http.ServeMux
	sseRelease   chan struct{}
	cancelSeen   chan struct{}
	cancelOnce   sync.Once
	maxInFlight  int
	inFlight     int
	inFlightLock sync.Mutex
}

func newTestAgent() *testAgent {
	a := &testAgent{mux: http.NewServeMux(), sseRelease: make(chan struct{}), cancelSeen: make(chan struct{})}
	a.mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"proto":%q,"path":%q,"query":%q}`, r.Proto, r.URL.Path, r.URL.RawQuery)
	})
	a.mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: first\n\n")
		fl.Flush()
		select {
		case <-a.sseRelease:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: second\n\n")
	})
	a.mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		h := sha256.New()
		n, err := io.Copy(h, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "%d %s", n, hex.EncodeToString(h.Sum(nil)))
	})
	a.mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, pattern(7, testTunnelSize()))
	})
	a.mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		a.inFlightLock.Lock()
		a.inFlight++
		if a.inFlight > a.maxInFlight {
			a.maxInFlight = a.inFlight
		}
		a.inFlightLock.Unlock()
		time.Sleep(50 * time.Millisecond)
		a.inFlightLock.Lock()
		a.inFlight--
		a.inFlightLock.Unlock()
		fmt.Fprint(w, r.URL.Query().Get("i"))
	})
	a.mux.HandleFunc("/wait-cancel", func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		a.cancelOnce.Do(func() { close(a.cancelSeen) })
	})
	return a
}

// -------------------------------------------------------------
// Tests
// -------------------------------------------------------------

func TestAgentTunnel(t *testing.T) {
	for _, cr := range allCarriers() {
		t.Run(cr.name, func(t *testing.T) {
			pki := newTunnelPKI(t)
			agent := newTestAgent()
			p := openTunnel(t, cr, pki, agent.mux, 0)

			if p.leaf.Subject.CommonName != "node-1" {
				t.Fatalf("controller saw node %q", p.leaf.Subject.CommonName)
			}

			t.Run("json over http2", func(t *testing.T) {
				code, body := tunnelGet(t, p.client, "/hello?x=1&y=%2F")
				if code != http.StatusOK || !strings.Contains(body, `"proto":"HTTP/2.0"`) || !strings.Contains(body, `"query":"x=1&y=%2F"`) && !strings.Contains(body, `"query":"x=1&y=%2F"`) {
					t.Fatalf("unexpected response %d %s", code, body)
				}
			})

			t.Run("ping", func(t *testing.T) {
				if err := p.client.Ping(context.Background()); err != nil {
					t.Fatal(err)
				}
			})

			t.Run("sse is not buffered", func(t *testing.T) {
				req, _ := http.NewRequest(http.MethodGet, "/sse", nil)
				resp, err := p.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				br := bufio.NewReader(resp.Body)
				got := make(chan string, 1)
				go func() {
					line, _ := br.ReadString('\n')
					got <- line
				}()
				select {
				case line := <-got:
					if line != "data: first\n" {
						t.Fatalf("first event %q", line)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("first event did not arrive while the stream was open")
				}
				close(agent.sseRelease)
				rest, _ := io.ReadAll(br)
				if !strings.Contains(string(rest), "data: second") {
					t.Fatalf("second event missing: %q", rest)
				}
			})

			t.Run("large upload", func(t *testing.T) {
				size := testTunnelSize()
				want := sha256.New()
				_, _ = io.Copy(want, pattern(3, size))
				req, _ := http.NewRequest(http.MethodPost, "/upload", pattern(3, size))
				req.ContentLength = size
				start := time.Now()
				resp, err := p.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(body) != fmt.Sprintf("%d %s", size, hex.EncodeToString(want.Sum(nil))) {
					t.Fatalf("upload mismatch: %s", body)
				}
				t.Logf("upload %d MiB in %v", size>>20, time.Since(start).Round(time.Millisecond))
			})

			t.Run("large download next to other requests", func(t *testing.T) {
				size := testTunnelSize()
				want := sha256.New()
				_, _ = io.Copy(want, pattern(7, size))
				req, _ := http.NewRequest(http.MethodGet, "/download", nil)
				resp, err := p.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				// Read the download slowly at first; small requests must still get through.
				head := make([]byte, 1<<20)
				if _, err := io.ReadFull(resp.Body, head); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 5; i++ {
					if code, _ := tunnelGet(t, p.client, "/hello"); code != http.StatusOK {
						t.Fatalf("request during download: %d", code)
					}
				}
				got := sha256.New()
				got.Write(head)
				if _, err := io.Copy(got, resp.Body); err != nil {
					t.Fatal(err)
				}
				if hex.EncodeToString(got.Sum(nil)) != hex.EncodeToString(want.Sum(nil)) {
					t.Fatal("download mismatch")
				}
			})

			t.Run("more concurrent requests than streams", func(t *testing.T) {
				const n = 300
				var wg sync.WaitGroup
				errs := make(chan error, n)
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/slow?i=%d", i), nil)
						resp, err := p.client.Do(req)
						if err != nil {
							errs <- err
							return
						}
						body, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						if string(body) != fmt.Sprint(i) {
							errs <- fmt.Errorf("request %d got %q", i, body)
						}
					}(i)
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					t.Fatal(err)
				}
				if agent.maxInFlight > agentTunnelMaxStreams {
					t.Fatalf("%d requests in flight, limit %d", agent.maxInFlight, agentTunnelMaxStreams)
				}
				t.Logf("max in flight: %d", agent.maxInFlight)
			})

			t.Run("cancel reaches the agent", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/wait-cancel", nil)
				resp, err := p.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				resp.Body.Close()
				select {
				case <-agent.cancelSeen:
				case <-time.After(5 * time.Second):
					t.Fatal("agent handler was not cancelled")
				}
			})

			t.Run("dead carrier ends the tunnel", func(t *testing.T) {
				p.agentRaw.Close()
				select {
				case err := <-p.serveErr:
					if err != nil {
						t.Logf("serve ended with %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("agent side did not notice the closed carrier")
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					err := p.client.Ping(ctx)
					cancel()
					if err != nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("controller still reaches a closed tunnel")
					}
					time.Sleep(50 * time.Millisecond)
				}
				// The broken tunnel must not dial anything else.
				if err := p.client.Ping(context.Background()); !errors.Is(err, ErrAgentTunnelClosed) {
					t.Fatalf("expected ErrAgentTunnelClosed, got %v", err)
				}
				select {
				case <-p.client.Done():
				default:
					t.Fatal("client not marked as closed")
				}
			})

			t.Run("reconnect", func(t *testing.T) {
				p2 := openTunnel(t, cr, pki, agent.mux, 0)
				if code, _ := tunnelGet(t, p2.client, "/hello"); code != http.StatusOK {
					t.Fatalf("new tunnel: %d", code)
				}
			})
		})
	}
}

func TestAgentTunnelKeepAliveAndWatchdog(t *testing.T) {
	pki := newTunnelPKI(t)

	// With keepalive pings the watchdog never fires.
	p := openTunnel(t, directCarrier(), pki, http.NotFoundHandler(), 300*time.Millisecond)
	go p.client.KeepAlive(50*time.Millisecond, time.Second, 2)
	select {
	case <-p.serveErr:
		t.Fatal("watchdog closed a tunnel that was pinged")
	case <-time.After(time.Second):
	}

	// Without pings the agent closes the tunnel, and the controller's keepalive notices.
	q := openTunnel(t, directCarrier(), pki, http.NotFoundHandler(), 300*time.Millisecond)
	select {
	case <-q.serveErr:
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not close an idle tunnel")
	}
	go q.client.KeepAlive(50*time.Millisecond, 200*time.Millisecond, 2)
	select {
	case <-q.client.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("keepalive did not detect the closed tunnel")
	}
}

func TestAgentTunnelRejectsUntrustedPeers(t *testing.T) {
	pki := newTunnelPKI(t)
	otherCA, otherKey := newTestCA(t)
	foreignNode := newTestLeaf(t, otherCA, otherKey, "node-1", x509.ExtKeyUsageClientAuth, nil)
	foreignController := newTestLeaf(t, otherCA, otherKey, "controller", x509.ExtKeyUsageServerAuth, []string{AgentTunnelServerName})
	otherPool := x509.NewCertPool()
	otherPool.AddCert(otherCA)

	handshake := func(agentCfg, controllerCfg *tls.Config) (agentErr, controllerErr error) {
		a, c := net.Pipe()
		done := make(chan error, 1)
		go func() {
			_, _, err := AcceptAgentTunnel(context.Background(), c, controllerCfg)
			done <- err
		}()
		_, agentErr = DialAgentTunnel(context.Background(), a, agentCfg)
		a.Close()
		return agentErr, <-done
	}
	serverCfg := func(verify func(*x509.Certificate) error) *tls.Config {
		return AgentTunnelServerTLSConfig(pki.pool, pki.serverCert, verify)
	}

	t.Run("valid pair", func(t *testing.T) {
		if aErr, cErr := handshake(AgentTunnelClientTLSConfig(pki.pool, pki.nodeCert), serverCfg(nil)); aErr != nil || cErr != nil {
			t.Fatalf("agent %v, controller %v", aErr, cErr)
		}
	})
	t.Run("node certificate from another CA", func(t *testing.T) {
		if _, cErr := handshake(AgentTunnelClientTLSConfig(pki.pool, foreignNode), serverCfg(nil)); cErr == nil {
			t.Fatal("controller accepted a foreign node certificate")
		}
	})
	t.Run("no node certificate", func(t *testing.T) {
		cfg := AgentTunnelClientTLSConfig(pki.pool, pki.nodeCert)
		cfg.Certificates = nil
		if _, cErr := handshake(cfg, serverCfg(nil)); cErr == nil {
			t.Fatal("controller accepted an agent without certificate")
		}
	})
	t.Run("removed node", func(t *testing.T) {
		reject := func(*x509.Certificate) error { return errors.New("node not paired") }
		if _, cErr := handshake(AgentTunnelClientTLSConfig(pki.pool, pki.nodeCert), serverCfg(reject)); cErr == nil {
			t.Fatal("controller accepted a removed node")
		}
	})
	t.Run("impostor controller", func(t *testing.T) {
		impostor := AgentTunnelServerTLSConfig(otherPool, foreignController, nil)
		if aErr, _ := handshake(AgentTunnelClientTLSConfig(pki.pool, pki.nodeCert), impostor); aErr == nil {
			t.Fatal("agent accepted a controller certificate from another CA")
		}
	})
	t.Run("http/1.1 only peer", func(t *testing.T) {
		cfg := serverCfg(nil)
		cfg.NextProtos = []string{"http/1.1"}
		agentCfg := AgentTunnelClientTLSConfig(pki.pool, pki.nodeCert)
		agentCfg.NextProtos = []string{"h2", "http/1.1"}
		if aErr, _ := handshake(agentCfg, cfg); aErr == nil {
			t.Fatal("tunnel accepted without HTTP/2")
		}
	})
}
