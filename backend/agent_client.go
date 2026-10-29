package backend

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Agent side: the files an agent keeps in its config directory, pairing (agent join) and the
// connection loop (agent run).
//
//	agent.json         controller URL, node ID, name
//	agent-policy.json  access mode and local restrictions (only changed locally)
//	agent.key          node private key (never leaves the machine)
//	agent.crt          node certificate
//	ca.crt             controller CA

const (
	agentConfigFile = "agent.json"
	agentPolicyFile = "agent-policy.json"
	agentKeyFile    = "agent.key"
	agentCertFile   = "agent.crt"
	agentCAFile     = "ca.crt"

	// AgentConnectPath is the WebSocket endpoint of the controller.
	AgentConnectPath = "/agent/connect"
)

// AgentConfig is stored in agent.json.
type AgentConfig struct {
	// ControllerURL is the dashboard URL (http/https: WebSocket carrier) or tls://host:port
	// (direct carrier).
	ControllerURL string `json:"controller_url"`
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`
	// InsecureProxyTLS skips the verification of the outer TLS certificate of an https URL (for
	// example a self-signed reverse proxy). The tunnel itself is always verified.
	InsecureProxyTLS bool `json:"insecure_proxy_tls,omitempty"`
}

// AgentPolicy is stored in agent-policy.json. The controller can read it but never change it.
type AgentPolicy struct {
	Mode            string   `json:"mode"`
	AllowFirewall   bool     `json:"allow_firewall"`
	AllowSelfUpdate bool     `json:"allow_self_update"`
	Servers         []string `json:"servers"`
}

// LoadAgentConfig reads agent.json.
func LoadAgentConfig(configDir string) (AgentConfig, error) {
	var cfg AgentConfig
	data, err := os.ReadFile(filepath.Join(configDir, agentConfigFile))
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", agentConfigFile, err)
	}
	if !IsValidNodeID(cfg.NodeID) {
		return cfg, fmt.Errorf("%s: invalid node ID", agentConfigFile)
	}
	return cfg, nil
}

// LoadAgentPolicy reads agent-policy.json. A missing or invalid file means read-only.
func LoadAgentPolicy(configDir string) AgentPolicy {
	policy := AgentPolicy{Mode: AgentModeReadOnly}
	data, err := os.ReadFile(filepath.Join(configDir, agentPolicyFile))
	if err != nil {
		return policy
	}
	if err := json.Unmarshal(data, &policy); err != nil || !IsValidAgentMode(policy.Mode) {
		return AgentPolicy{Mode: AgentModeReadOnly}
	}
	return policy
}

// SaveAgentPolicy writes agent-policy.json.
func SaveAgentPolicy(configDir string, policy AgentPolicy) error {
	if !IsValidAgentMode(policy.Mode) {
		return errors.New("invalid access mode")
	}
	if policy.Servers == nil {
		policy.Servers = []string{}
	}
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(configDir, agentPolicyFile), data, 0600)
}

// MachineID returns the systemd machine ID, or "" when the system has none (some containers;
// the Docker setup mounts the host's /etc/machine-id).
func MachineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if data, err := os.ReadFile(p); err == nil {
			if id := strings.TrimSpace(string(data)); len(id) >= 16 && len(id) <= 64 {
				return id
			}
		}
	}
	return ""
}

// carrierTarget maps a controller URL to a dial function for its carrier.
func carrierTarget(controllerURL string, insecureProxyTLS bool) (func(ctx context.Context) (net.Conn, error), error) {
	u, err := url.Parse(strings.TrimSpace(controllerURL))
	if err != nil {
		return nil, fmt.Errorf("invalid controller URL: %w", err)
	}
	if u.Host == "" {
		return nil, errors.New("invalid controller URL: missing host")
	}
	switch u.Scheme {
	case "http", "https":
		ws := *u
		ws.Scheme = "ws"
		if u.Scheme == "https" {
			ws.Scheme = "wss"
		}
		ws.Path = strings.TrimRight(u.Path, "/") + AgentConnectPath
		ws.RawQuery, ws.Fragment = "", ""
		var outer *tls.Config
		if insecureProxyTLS {
			outer = &tls.Config{InsecureSkipVerify: true} // the tunnel inside is verified
		}
		target := ws.String()
		return func(ctx context.Context) (net.Conn, error) { return DialWebSocket(ctx, target, outer) }, nil
	case "tls":
		if u.Port() == "" {
			return nil, errors.New("invalid controller URL: tls:// needs a port")
		}
		addr := u.Host
		return func(ctx context.Context) (net.Conn, error) {
			d := &net.Dialer{KeepAlive: 15 * time.Second}
			return d.DialContext(ctx, "tcp", addr)
		}, nil
	default:
		return nil, fmt.Errorf("invalid controller URL: unsupported scheme %q (http, https or tls)", u.Scheme)
	}
}

// AgentJoinOptions are the arguments of `agent join`.
type AgentJoinOptions struct {
	ControllerURL    string
	Token            string
	CAFingerprint    string
	Mode             string
	Version          string
	InsecureProxyTLS bool
}

// AgentJoin pairs this machine with a controller and stores the identity in configDir. The
// private key is created here and never sent anywhere.
func AgentJoin(ctx context.Context, configDir string, opts AgentJoinOptions) (AgentConfig, error) {
	if !IsValidAgentMode(opts.Mode) {
		return AgentConfig{}, errors.New("invalid access mode (read-only or read-write)")
	}
	fp := strings.ToLower(strings.TrimSpace(opts.CAFingerprint))
	if !strings.HasPrefix(fp, "sha256:") || len(fp) != len("sha256:")+64 {
		return AgentConfig{}, errors.New("invalid CA fingerprint (expected sha256:<64 hex characters>)")
	}
	dial, err := carrierTarget(opts.ControllerURL, opts.InsecureProxyTLS)
	if err != nil {
		return AgentConfig{}, err
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return AgentConfig{}, err
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return AgentConfig{}, err
	}
	hostname, _ := os.Hostname()
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: hostname},
	}, key)
	if err != nil {
		return AgentConfig{}, err
	}

	raw, err := dial(ctx)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("connect to %s: %w", opts.ControllerURL, err)
	}
	resp, err := enrollOverCarrier(ctx, raw, fp, EnrollRequest{
		Token:     opts.Token,
		CSR:       csr,
		MachineID: MachineID(),
		Hostname:  hostname,
		Version:   opts.Version,
	})
	if err != nil {
		return AgentConfig{}, err
	}

	// Check what the controller issued before storing it.
	ca, err := x509.ParseCertificate(resp.CA)
	if err != nil || CertFingerprint(resp.CA) != fp {
		return AgentConfig{}, errors.New("the controller returned an unexpected CA certificate")
	}
	cert, err := x509.ParseCertificate(resp.Cert)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("invalid node certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return AgentConfig{}, fmt.Errorf("invalid node certificate: %w", err)
	}
	if !pub.Equal(cert.PublicKey) || cert.Subject.CommonName != resp.NodeID || !IsValidNodeID(resp.NodeID) {
		return AgentConfig{}, errors.New("the node certificate does not match the pairing request")
	}

	if err := saveCertAndKey(filepath.Join(configDir, agentCertFile), filepath.Join(configDir, agentKeyFile), resp.Cert, key); err != nil {
		return AgentConfig{}, err
	}
	if err := WriteFileAtomic(filepath.Join(configDir, agentCAFile), pemCert(resp.CA), 0644); err != nil {
		return AgentConfig{}, err
	}
	policy := LoadAgentPolicy(configDir)
	policy.Mode = LowerAgentMode(opts.Mode, resp.Mode)
	if _, err := os.Stat(filepath.Join(configDir, agentPolicyFile)); errors.Is(err, os.ErrNotExist) {
		policy.AllowSelfUpdate = true
	}
	if err := SaveAgentPolicy(configDir, policy); err != nil {
		return AgentConfig{}, err
	}
	cfg := AgentConfig{
		ControllerURL:    strings.TrimSpace(opts.ControllerURL),
		NodeID:           resp.NodeID,
		Name:             sanitizeDisplayText(resp.Name, 64),
		InsecureProxyTLS: opts.InsecureProxyTLS,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return AgentConfig{}, err
	}
	if err := WriteFileAtomic(filepath.Join(configDir, agentConfigFile), data, 0600); err != nil {
		return AgentConfig{}, err
	}
	return cfg, nil
}

// AgentIdentity is what an agent needs to open its tunnel.
type AgentIdentity struct {
	Config AgentConfig
	Cert   tls.Certificate
	Leaf   *x509.Certificate
	CAPool *x509.CertPool
}

// LoadAgentIdentity reads the pairing data from configDir.
func LoadAgentIdentity(configDir string) (AgentIdentity, error) {
	cfg, err := LoadAgentConfig(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AgentIdentity{}, errors.New("this machine is not paired yet, run `agent join` first")
		}
		return AgentIdentity{}, err
	}
	leaf, key, err := loadCertAndKey(filepath.Join(configDir, agentCertFile), filepath.Join(configDir, agentKeyFile))
	if err != nil {
		return AgentIdentity{}, err
	}
	ca, err := loadCertPEM(filepath.Join(configDir, agentCAFile))
	if err != nil {
		return AgentIdentity{}, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return AgentIdentity{
		Config: cfg,
		Cert:   tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf},
		Leaf:   leaf,
		CAPool: pool,
	}, nil
}

// ConnectAgentTunnel opens one tunnel to the controller.
func ConnectAgentTunnel(ctx context.Context, id AgentIdentity) (*tls.Conn, error) {
	dial, err := carrierTarget(id.Config.ControllerURL, id.Config.InsecureProxyTLS)
	if err != nil {
		return nil, err
	}
	raw, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	return DialAgentTunnel(ctx, raw, AgentTunnelClientTLSConfig(id.CAPool, id.Cert))
}

// Agent tunnel timing, see the design notes in agent_tunnel.go.
const (
	AgentPingInterval = 20 * time.Second
	AgentPingTimeout  = 30 * time.Second
	AgentPingFailures = 2
	AgentIdleTimeout  = 90 * time.Second

	agentBackoffMin = time.Second
	agentBackoffMax = time.Minute
)

// RunAgent keeps the tunnel to the controller open and serves h on it until ctx ends. It
// reconnects with exponential backoff and jitter. load is called before every connection, so a
// renewed certificate is picked up.
func RunAgent(ctx context.Context, load func() (AgentIdentity, error), h http.Handler, logf func(string, ...interface{})) error {
	// The controller pings right after accepting a tunnel. In TLS 1.3 the client finishes its
	// handshake before the server has checked the client certificate, so a rejected node only
	// notices that no request ever arrives and the connection closes.
	var served int64
	var current atomic.Value // AgentConfig of the current connection, for the log line
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&served, 1) == 1 {
			cfg, _ := current.Load().(AgentConfig)
			logf("[AGENT] Connected to %s as node %s", cfg.ControllerURL, cfg.NodeID)
		}
		h.ServeHTTP(w, r)
	})
	attempt := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		id, err := load()
		if err != nil {
			return err
		}
		if time.Now().After(id.Leaf.NotAfter) {
			return errors.New("the node certificate has expired, run `agent join` again")
		}
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		conn, err := ConnectAgentTunnel(dialCtx, id)
		cancel()
		if err == nil {
			atomic.StoreInt64(&served, 0)
			current.Store(id.Config)
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			err = ServeAgentTunnel(conn, counted, AgentIdleTimeout)
			stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if atomic.LoadInt64(&served) > 0 {
				// The tunnel worked; start over with short waits.
				attempt = 0
				logf("[AGENT] Connection to %s closed%s, reconnecting", id.Config.ControllerURL, errSuffix(err))
			} else {
				logf("[AGENT] The controller at %s closed the connection without using it%s. "+
					"If this repeats, the node was removed or paired again elsewhere; run `agent join` with a new pairing command.",
					id.Config.ControllerURL, errSuffix(err))
			}
		} else {
			logf("[AGENT] Connecting to %s failed: %v", id.Config.ControllerURL, err)
		}
		attempt++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agentBackoff(attempt)):
		}
	}
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

// agentBackoff returns the wait before reconnect attempt n (1-based): exponential from 1 s up
// to 1 min, with ±20 % jitter so agents do not reconnect all at once after a controller restart.
func agentBackoff(n int) time.Duration {
	d := float64(agentBackoffMin) * math.Pow(2, float64(n-1))
	if d > float64(agentBackoffMax) {
		d = float64(agentBackoffMax)
	}
	return time.Duration(d * (0.8 + 0.4*mrand.Float64()))
}
