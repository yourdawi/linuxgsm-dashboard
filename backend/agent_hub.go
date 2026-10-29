package backend

import "sync"

// AgentHub keeps the live tunnels of the controller, at most one per node.
type AgentHub struct {
	nodes *NodeRegistry
	logf  func(format string, args ...interface{})

	mu      sync.Mutex
	tunnels map[string]*AgentTunnelClient
}

// NewAgentHub creates an empty hub. logf may be nil.
func NewAgentHub(nodes *NodeRegistry, logf func(format string, args ...interface{})) *AgentHub {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &AgentHub{nodes: nodes, logf: logf, tunnels: make(map[string]*AgentTunnelClient)}
}

// Attach takes over a new tunnel of nodeID. A previous tunnel of the same node is closed, so a
// reconnecting agent replaces its stale connection.
func (h *AgentHub) Attach(nodeID string, client *AgentTunnelClient) {
	h.mu.Lock()
	old := h.tunnels[nodeID]
	h.tunnels[nodeID] = client
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
	h.nodes.Seen(nodeID, "")
	h.logf("[AGENT] Node %s connected from %s", nodeID, client.RemoteAddr())

	go client.KeepAlive(AgentPingInterval, AgentPingTimeout, AgentPingFailures)
	go func() {
		<-client.Done()
		h.mu.Lock()
		current := h.tunnels[nodeID] == client
		if current {
			delete(h.tunnels, nodeID)
		}
		h.mu.Unlock()
		if current {
			h.logf("[AGENT] Node %s disconnected", nodeID)
		}
	}()
}

// Tunnel returns the live tunnel of a node.
func (h *AgentHub) Tunnel(nodeID string) (*AgentTunnelClient, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.tunnels[nodeID]
	return c, ok
}

// Online reports whether a node is connected.
func (h *AgentHub) Online(nodeID string) bool {
	_, ok := h.Tunnel(nodeID)
	return ok
}

// Disconnect closes the tunnel of a node (for example after it was removed).
func (h *AgentHub) Disconnect(nodeID string) {
	h.mu.Lock()
	c := h.tunnels[nodeID]
	delete(h.tunnels, nodeID)
	h.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
