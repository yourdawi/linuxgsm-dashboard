package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// agentHelloTimeout limits the hello request right after a tunnel was accepted.
const agentHelloTimeout = 15 * time.Second

// NodeStatus is the live state of a node as the controller sees it.
type NodeStatus struct {
	Online bool `json:"online"`
	// Hello is the last answer of the node (nil before the first one).
	Hello *AgentHello `json:"hello,omitempty"`
	// Compatible is false when controller and agent versions do not fit together; such a node
	// only gets hello and update requests.
	Compatible bool `json:"compatible"`
	// EffectiveMode is the lower of the controller's and the node's access mode.
	EffectiveMode string `json:"effective_mode,omitempty"`
	// Host holds CPU, RAM and disk of the node while it is online.
	Host *HostStats `json:"host,omitempty"`
	// Problem explains why a node is not usable ("" when it is).
	Problem string `json:"problem,omitempty"`
}

// AgentHub keeps the live tunnels of the controller, at most one per node.
type AgentHub struct {
	nodes   *NodeRegistry
	version string // controller version, for the compatibility check
	logf    func(format string, args ...interface{})

	mu      sync.Mutex
	tunnels map[string]*AgentTunnelClient
	status  map[string]NodeStatus
	servers map[string][]GameServerInstance                 // last known servers per node, global IDs
	hosts   map[string]HostStats                            // last host statistics per node
	signCSR func([]byte, string, time.Time) ([]byte, error) // renews node certificates (agent_renew.go)
}

// NewAgentHub creates an empty hub. logf may be nil.
func NewAgentHub(nodes *NodeRegistry, controllerVersion string, logf func(format string, args ...interface{})) *AgentHub {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &AgentHub{
		nodes:   nodes,
		version: controllerVersion,
		logf:    logf,
		tunnels: make(map[string]*AgentTunnelClient),
		status:  make(map[string]NodeStatus),
		servers: make(map[string][]GameServerInstance),
		hosts:   make(map[string]HostStats),
	}
}

// Attach takes over a new tunnel of nodeID. A previous tunnel of the same node is closed, so a
// reconnecting agent replaces its stale connection. The node is asked for hello right away: the
// agent counts a connection without any request as rejected.
func (h *AgentHub) Attach(nodeID string, client *AgentTunnelClient) {
	h.mu.Lock()
	old := h.tunnels[nodeID]
	h.tunnels[nodeID] = client
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
	h.logf("[AGENT] Node %s connected from %s", nodeID, client.RemoteAddr())

	go func() {
		if !h.handshake(nodeID, client) {
			client.Close()
		} else {
			h.retireOldKeys(nodeID, client)
			if st := h.Status(nodeID); st.Hello != nil && st.Hello.Protocol == AgentProtocolVersion &&
				h.renewIfDue(nodeID, client, agentRenewNow()) {
				return // reconnects with the renewed certificate
			}
			if h.Status(nodeID).Compatible {
				go h.pollServers(nodeID, client)
			}
			client.KeepAlive(AgentPingInterval, AgentPingTimeout, AgentPingFailures)
		}
	}()
	go func() {
		<-client.Done()
		h.mu.Lock()
		current := h.tunnels[nodeID] == client
		if current {
			delete(h.tunnels, nodeID)
			st := h.status[nodeID]
			st.Online = false
			h.status[nodeID] = st
		}
		h.mu.Unlock()
		if current {
			h.logf("[AGENT] Node %s disconnected", nodeID)
		}
	}()
}

// handshake fetches hello and checks the node. It returns false when the tunnel must be closed.
func (h *AgentHub) handshake(nodeID string, client *AgentTunnelClient) bool {
	hello, err := fetchAgentHello(client)
	if err != nil {
		h.setStatus(nodeID, client, NodeStatus{Problem: "no valid hello: " + err.Error()})
		h.logf("[AGENT] Node %s: hello failed: %v", nodeID, err)
		return false
	}
	if err := h.nodes.CheckMachineID(nodeID, hello.MachineID); err != nil {
		h.setStatus(nodeID, client, NodeStatus{Hello: &hello, Problem: err.Error()})
		h.logf("[AGENT] Node %s rejected: %v", nodeID, err)
		return false
	}
	h.nodes.Seen(nodeID, hello.Version)

	node, _ := h.nodes.Node(nodeID)
	st := NodeStatus{
		Online:        true,
		Hello:         &hello,
		Compatible:    AgentVersionsCompatible(h.version, hello.Version) && hello.Protocol == AgentProtocolVersion,
		EffectiveMode: LowerAgentMode(node.Mode, hello.Mode),
	}
	if !st.Compatible {
		st.Problem = fmt.Sprintf("update required: the dashboard runs %s, the node %s", h.version, hello.Version)
		h.logf("[AGENT] Node %s: %s", nodeID, st.Problem)
	}
	h.setStatus(nodeID, client, st)
	return true
}

// setStatus stores st if client is still the node's current tunnel.
func (h *AgentHub) setStatus(nodeID string, client *AgentTunnelClient, st NodeStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tunnels[nodeID] == client || !st.Online {
		h.status[nodeID] = st
	}
}

// fetchAgentHello asks the agent for hello and validates the answer, which comes from the node
// and is untrusted.
func fetchAgentHello(client *AgentTunnelClient) (AgentHello, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentHelloTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, AgentHelloPath, nil)
	if err != nil {
		return AgentHello{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return AgentHello{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AgentHello{}, fmt.Errorf("status %s", resp.Status)
	}
	var hello AgentHello
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&hello); err != nil {
		return AgentHello{}, err
	}
	hello.Version = sanitizeDisplayText(hello.Version, 64)
	hello.Hostname = sanitizeDisplayText(hello.Hostname, 64)
	hello.MachineID = sanitizeDisplayText(hello.MachineID, 64)
	if !IsValidAgentMode(hello.Mode) {
		hello.Mode = AgentModeReadOnly
	}
	servers := hello.Servers[:0]
	for _, id := range hello.Servers {
		if IsValidLocalServerID(id) {
			servers = append(servers, id)
		}
	}
	hello.Servers = servers
	hello.Capabilities.Runtime = sanitizeDisplayText(hello.Capabilities.Runtime, 32)
	if u, err := url.Parse(hello.FastDLURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(hello.FastDLURL) > 256 {
		hello.FastDLURL = ""
	}
	return hello, nil
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

// Status returns the live state of a node. The effective mode follows changes of the node's mode
// on the controller immediately.
func (h *AgentHub) Status(nodeID string) NodeStatus {
	h.mu.Lock()
	st := h.status[nodeID]
	_, st.Online = h.tunnels[nodeID]
	if host, ok := h.hosts[nodeID]; ok && st.Online {
		st.Host = &host
	}
	h.mu.Unlock()
	if !st.Online {
		st.Compatible = false
	}
	if st.Hello != nil {
		node, _ := h.nodes.Node(nodeID)
		st.EffectiveMode = LowerAgentMode(node.Mode, st.Hello.Mode)
	}
	return st
}

// Disconnect closes the tunnel of a node (for example after it was removed).
func (h *AgentHub) Disconnect(nodeID string) {
	h.mu.Lock()
	c := h.tunnels[nodeID]
	delete(h.tunnels, nodeID)
	delete(h.status, nodeID)
	delete(h.servers, nodeID)
	delete(h.hosts, nodeID)
	h.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
