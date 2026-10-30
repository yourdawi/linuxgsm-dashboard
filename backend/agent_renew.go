package backend

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Renewal of node certificates. The controller drives it through the tunnel, so the agent never
// has to call the controller: it asks the node for a request for a new key (/agent/v1/csr),
// allows the new key, and delivers the certificate (/agent/v1/cert). Then it closes the tunnel;
// the node reconnects with the new identity and the old key is retired. A node that fails to
// install the certificate reconnects with the old one, and the unused new key is retired instead.

// agentRenewBefore is how long before expiry a node certificate is renewed (a third of its
// lifetime, so a node that is offline for a while still gets renewed in time).
const agentRenewBefore = AgentNodeCertValidity / 3

// agentRenewNow is the clock for deciding whether a renewal is due (tests move it forward).
// Certificates are always signed with the real time.
var agentRenewNow = time.Now

// SetCertificateSigner lets the hub renew node certificates (normally ControllerCA.SignNodeCSR).
func (h *AgentHub) SetCertificateSigner(sign func(csrDER []byte, nodeID string, now time.Time) ([]byte, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.signCSR = sign
}

// retireOldKeys keeps only the key the node connected with, once a renewal has taken effect.
func (h *AgentHub) retireOldKeys(nodeID string, client *AgentTunnelClient) {
	leaf := client.PeerCertificate()
	if leaf == nil {
		return
	}
	node, ok := h.nodes.Node(nodeID)
	if !ok || len(node.Keys) <= 1 {
		return
	}
	if err := h.nodes.RetireNodeKeys(nodeID, PublicKeyFingerprint(leaf)); err == nil {
		h.logf("[AGENT] Node %s connected with its renewed certificate; old key retired", nodeID)
	}
}

// renewIfDue renews the node certificate of the tunnel when it expires within agentRenewBefore.
// It reports whether a renewal took place (the tunnel is closed then).
func (h *AgentHub) renewIfDue(nodeID string, client *AgentTunnelClient, now time.Time) bool {
	leaf := client.PeerCertificate()
	h.mu.Lock()
	sign := h.signCSR
	h.mu.Unlock()
	if leaf == nil || sign == nil || leaf.NotAfter.Sub(now) > agentRenewBefore {
		return false
	}
	if err := h.renew(nodeID, client, sign); err != nil {
		h.logf("[AGENT] Node %s: certificate renewal failed: %v", nodeID, err)
		return false
	}
	h.logf("[AGENT] Node %s: certificate renewed (the old one expires %s)", nodeID, leaf.NotAfter.Format("2006-01-02"))
	// Reconnect with the new identity, so the old key can be retired right away.
	client.Close()
	return true
}

func (h *AgentHub) renew(nodeID string, client *AgentTunnelClient, sign func([]byte, string, time.Time) ([]byte, error)) error {
	var csr struct {
		CSR []byte `json:"csr"`
	}
	if err := agentPostJSON(client, AgentCSRPath, nil, &csr); err != nil {
		return fmt.Errorf("certificate request: %w", err)
	}
	certDER, err := sign(csr.CSR, nodeID, time.Now())
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return err
	}
	// The new key must be allowed before the node can use it; the old one stays valid until the
	// node has reconnected.
	if err := h.nodes.AddNodeKey(nodeID, PublicKeyFingerprint(cert)); err != nil {
		return err
	}
	if err := agentPostJSON(client, AgentCertPath, map[string][]byte{"cert": certDER}, nil); err != nil {
		return fmt.Errorf("installing the certificate: %w", err)
	}
	return nil
}

// agentPostJSON posts body as JSON to the agent and decodes the answer into out (if not nil).
func agentPostJSON(client *AgentTunnelClient, path string, body, out interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), agentHelloTimeout)
	defer cancel()
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("status %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
}
