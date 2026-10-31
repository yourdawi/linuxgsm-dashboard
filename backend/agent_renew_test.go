package backend

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// renewingAgent answers hello and the two renewal routes like a real agent (with the real
// renewal functions on dir).
func renewingAgent(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case AgentHelloPath:
			_ = json.NewEncoder(w).Encode(AgentHello{Version: "1.0.0", Protocol: AgentProtocolVersion, Mode: AgentModeReadWrite})
		case AgentCSRPath:
			csr, err := AgentPrepareRenewal(dir)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string][]byte{"csr": csr})
		case AgentCertPath:
			var p struct {
				Cert []byte `json:"cert"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if err := AgentInstallRenewal(dir, p.Cert); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func TestAgentCertificateRenewal(t *testing.T) {
	c := newTestController(t)
	hub := NewAgentHub(c.nodes, "1.0.0", t.Logf)
	hub.SetCertificateSigner(c.ca.SignNodeCSR)
	c.gw.OnTunnel = hub.Attach
	dir := t.TempDir()
	if _, err := AgentJoin(context.Background(), dir, joinOpts(c.wsURL, c.pair(t, "box", AgentModeReadWrite), c.ca.Fingerprint(), AgentModeReadWrite)); err != nil {
		t.Fatal(err)
	}
	before, err := LoadAgentIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := PublicKeyFingerprint(before.Leaf)

	// The first connection sees a clock 25 days ahead, so the 30-day certificate is due; after
	// that the clock is real again.
	var calls int32
	agentRenewNow = func() time.Time {
		if atomic.AddInt32(&calls, 1) == 1 {
			return time.Now().Add(25 * 24 * time.Hour)
		}
		return time.Now()
	}
	defer func() { agentRenewNow = time.Now }()

	runTestAgent(t, dir, renewingAgent(dir))

	deadline := time.Now().Add(15 * time.Second)
	for {
		node, _ := c.nodes.Node("box")
		st := hub.Status("box")
		if len(node.Keys) == 1 && node.Keys[0] != oldKey && st.Online && st.Hello != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("renewal did not complete: keys %v, status %+v", node.Keys, st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	after, err := LoadAgentIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Leaf.SerialNumber.Cmp(before.Leaf.SerialNumber) == 0 || PublicKeyFingerprint(after.Leaf) == oldKey {
		t.Fatal("the node still uses the old certificate")
	}
	if !after.Leaf.NotBefore.Before(time.Now()) {
		t.Fatalf("renewed certificate not valid yet: %v", after.Leaf.NotBefore)
	}
	for _, f := range []string{agentNextKeyFile, agentKeyFile + ".old"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s left behind", f)
		}
	}
	// The old identity is no longer accepted.
	if err := c.nodes.VerifyNodeCert(before.Leaf); err == nil {
		t.Fatal("old key still allowed")
	}
}

func TestAgentRenewalChecksAndCrashRecovery(t *testing.T) {
	c := newTestController(t)
	dir := t.TempDir()
	if _, err := AgentJoin(context.Background(), dir, joinOpts(c.wsURL, c.pair(t, "box", AgentModeReadWrite), c.ca.Fingerprint(), AgentModeReadWrite)); err != nil {
		t.Fatal(err)
	}
	orig, _ := LoadAgentIdentity(dir)

	if err := AgentInstallRenewal(dir, orig.Leaf.Raw); err == nil {
		t.Fatal("installed a certificate without a renewal in progress")
	}
	csr, err := AgentPrepareRenewal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("certificate for another node", func(t *testing.T) {
		der, _ := c.ca.SignNodeCSR(csr, "other", time.Now())
		if err := AgentInstallRenewal(dir, der); err == nil {
			t.Fatal("accepted a certificate for another node")
		}
	})
	t.Run("certificate for another key", func(t *testing.T) {
		der, _ := c.ca.SignNodeCSR(testCSR(t, "box", true), "box", time.Now())
		if err := AgentInstallRenewal(dir, der); err == nil {
			t.Fatal("accepted a certificate for another key")
		}
	})
	t.Run("certificate from another CA", func(t *testing.T) {
		other, err := LoadOrCreateControllerCA(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		der, _ := other.SignNodeCSR(csr, "box", time.Now())
		if err := AgentInstallRenewal(dir, der); err == nil {
			t.Fatal("accepted a certificate from another CA")
		}
	})
	if id, err := LoadAgentIdentity(dir); err != nil || id.Leaf.SerialNumber.Cmp(orig.Leaf.SerialNumber) != 0 {
		t.Fatalf("failed attempts changed the identity: %v", err)
	}

	// Crash after the new key replaced agent.key but before the certificate was written.
	keyPath := filepath.Join(dir, agentKeyFile)
	oldKey, _ := os.ReadFile(keyPath)
	nextKey, _ := os.ReadFile(filepath.Join(dir, agentNextKeyFile))
	if err := os.WriteFile(keyPath+".old", oldKey, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, nextKey, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := LoadAgentIdentity(dir)
	if err != nil || id.Leaf.SerialNumber.Cmp(orig.Leaf.SerialNumber) != 0 {
		t.Fatalf("no recovery with the old key: %v", err)
	}
	// The renewal can still be completed afterwards, and the kept old key is not overwritten.
	der, err := c.ca.SignNodeCSR(csr, "box", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := AgentInstallRenewal(dir, der); err != nil {
		t.Fatal(err)
	}
	id, err = LoadAgentIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cert, _ := x509.ParseCertificate(der); id.Leaf.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Fatal("renewed certificate not in use")
	}
}

func TestRenewIfDueThreshold(t *testing.T) {
	c := newTestController(t)
	hub := NewAgentHub(c.nodes, "1.0.0", nil)
	// Without a signer nothing happens, even when due.
	if hub.renewIfDue("box", &AgentTunnelClient{}, time.Now()) {
		t.Fatal("renewed without a signer")
	}
	if agentRenewBefore != 10*24*time.Hour {
		t.Fatalf("renewal window %v", agentRenewBefore)
	}
}
