package backend

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Private CA of the controller for the agent tunnel. It signs the controller's own tunnel
// certificate and the node certificates. Nothing outside the tunnel trusts it.

const (
	agentCADirName = "agent-ca"

	agentCAValidity       = 10 * 365 * 24 * time.Hour
	agentServerValidity   = 365 * 24 * time.Hour
	agentServerRenewAfter = 2 * agentServerValidity / 3
	// AgentNodeCertValidity is the lifetime of node certificates; the controller renews them
	// through the tunnel after two thirds of it.
	AgentNodeCertValidity = 30 * 24 * time.Hour
	// agentCertBackdate tolerates clocks that are a few minutes behind.
	agentCertBackdate = 5 * time.Minute
)

// ControllerCA holds the CA and the controller's tunnel certificate.
type ControllerCA struct {
	dir    string
	caCert *x509.Certificate
	caKey  ed25519.PrivateKey
	pool   *x509.CertPool

	mu         sync.Mutex
	serverCert tls.Certificate
	serverLeaf *x509.Certificate
}

// LoadOrCreateControllerCA loads the CA from <configDir>/agent-ca or creates it on first use.
// The tunnel certificate is renewed when two thirds of its lifetime have passed.
func LoadOrCreateControllerCA(configDir string) (*ControllerCA, error) {
	dir := filepath.Join(configDir, agentCADirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	ca := &ControllerCA{dir: dir}

	caCert, caKey, err := loadCertAndKey(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		caCert, caKey, err = ca.createCA()
		if err != nil {
			return nil, fmt.Errorf("create agent CA: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("load agent CA: %w", err)
	}
	if !caCert.IsCA {
		return nil, errors.New("load agent CA: ca.crt is not a CA certificate")
	}
	ca.caCert, ca.caKey = caCert, caKey
	ca.pool = x509.NewCertPool()
	ca.pool.AddCert(caCert)

	if err := ca.loadOrRenewServerCert(time.Now()); err != nil {
		return nil, err
	}
	return ca, nil
}

func (ca *ControllerCA) createCA() (*x509.Certificate, ed25519.PrivateKey, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "LinuxGSM Dashboard agent CA"},
		NotBefore:             now.Add(-agentCertBackdate),
		NotAfter:              now.Add(agentCAValidity),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	if err := saveCertAndKey(filepath.Join(ca.dir, "ca.crt"), filepath.Join(ca.dir, "ca.key"), der, key); err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// loadOrRenewServerCert keeps the tunnel certificate valid. It is called on start and can be
// called periodically.
func (ca *ControllerCA) loadOrRenewServerCert(now time.Time) error {
	certPath, keyPath := filepath.Join(ca.dir, "server.crt"), filepath.Join(ca.dir, "server.key")
	leaf, key, err := loadCertAndKey(certPath, keyPath)
	if err == nil && leaf.CheckSignatureFrom(ca.caCert) == nil && now.Before(leaf.NotBefore.Add(agentServerRenewAfter)) {
		ca.setServerCert(leaf, key)
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Printf("[AGENT] Replacing unreadable tunnel certificate: %v\n", err)
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: AgentTunnelServerName},
		DNSNames:     []string{AgentTunnelServerName},
		NotBefore:    now.Add(-agentCertBackdate),
		NotAfter:     now.Add(agentServerValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.caCert, pub, ca.caKey)
	if err != nil {
		return fmt.Errorf("create tunnel certificate: %w", err)
	}
	if err := saveCertAndKey(certPath, keyPath, der, key); err != nil {
		return err
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	ca.setServerCert(leaf, key)
	return nil
}

func (ca *ControllerCA) setServerCert(leaf *x509.Certificate, key ed25519.PrivateKey) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	// The CA certificate is sent along, so an agent that only knows the CA fingerprint (pairing)
	// can verify the chain.
	ca.serverCert = tls.Certificate{Certificate: [][]byte{leaf.Raw, ca.caCert.Raw}, PrivateKey: key, Leaf: leaf}
	ca.serverLeaf = leaf
}

// RenewIfDue renews the tunnel certificate when it is due.
func (ca *ControllerCA) RenewIfDue() error {
	return ca.loadOrRenewServerCert(time.Now())
}

// CACertDER returns the CA certificate.
func (ca *ControllerCA) CACertDER() []byte { return ca.caCert.Raw }

// Fingerprint identifies the CA in pairing commands ("sha256:<hex>").
func (ca *ControllerCA) Fingerprint() string { return CertFingerprint(ca.caCert.Raw) }

// CertFingerprint returns "sha256:<hex>" of a DER certificate.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PublicKeyFingerprint returns "sha256:<hex>" of the DER encoded SubjectPublicKeyInfo. Nodes are
// allowlisted by this value, so a certificate only counts while its key is listed.
func PublicKeyFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ServerTLSConfig is the TLS configuration of the controller's tunnel endpoint. Agents with a
// node certificate are checked with verifyNode; agents without one may only pair (ALPN
// AgentEnrollALPN), which the gateway enforces after the handshake.
func (ca *ControllerCA) ServerTLSConfig(verifyNode func(leaf *x509.Certificate) error) *tls.Config {
	cfg := AgentTunnelServerTLSConfig(ca.pool, tls.Certificate{}, verifyNode)
	cfg.Certificates = nil
	cfg.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		cert := ca.serverCert
		return &cert, nil
	}
	cfg.NextProtos = []string{agentTunnelALPN, AgentEnrollALPN}
	cfg.ClientAuth = tls.VerifyClientCertIfGiven
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || verifyNode == nil {
			return nil
		}
		return verifyNode(cs.PeerCertificates[0])
	}
	return cfg
}

// SignNodeCSR issues a node certificate for nodeID. Only the public key of the CSR is used;
// subject and extensions requested by the agent are ignored.
func (ca *ControllerCA) SignNodeCSR(csrDER []byte, nodeID string, now time.Time) ([]byte, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, fmt.Errorf("invalid certificate request: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid certificate request signature: %w", err)
	}
	pub, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("certificate request must use an Ed25519 key")
	}
	if !IsValidNodeID(nodeID) {
		return nil, errors.New("invalid node ID")
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    now.Add(-agentCertBackdate),
		NotAfter:     now.Add(AgentNodeCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca.caCert, pub, ca.caKey)
}

// -------------------------------------------------------------
// PEM files
// -------------------------------------------------------------

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func saveCertAndKey(certPath, keyPath string, certDER []byte, key ed25519.PrivateKey) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return err
	}
	return WriteFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0644)
}

func loadCertAndKey(certPath, keyPath string) (*x509.Certificate, ed25519.PrivateKey, error) {
	cert, err := loadCertPEM(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, nil, fmt.Errorf("%s: no PEM private key", keyPath)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", keyPath, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("%s: not an Ed25519 key", keyPath)
	}
	if !key.Public().(ed25519.PublicKey).Equal(cert.PublicKey) {
		return nil, nil, fmt.Errorf("%s does not belong to %s", keyPath, certPath)
	}
	return cert, key, nil
}

func loadCertPEM(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: no PEM certificate", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// IsValidNodeID validates node IDs ([a-z0-9-], 1-32 characters, no leading or trailing '-').
func IsValidNodeID(id string) bool {
	if len(id) == 0 || len(id) > 32 || strings.HasPrefix(id, "-") || strings.HasSuffix(id, "-") {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
