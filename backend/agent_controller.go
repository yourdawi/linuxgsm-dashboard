package backend

import (
	"net"
	"sync"
)

// AgentController bundles the controller side of multi-node support. The CA is created on first
// use (the first pairing), so dashboards without nodes keep no agent key material.
type AgentController struct {
	Nodes *NodeRegistry
	Hub   *AgentHub

	configDir string
	logf      func(format string, args ...interface{})

	mu      sync.Mutex
	gateway *AgentGateway
}

// NewAgentController loads the node registry. When nodes exist, the CA is loaded right away so
// they can reconnect.
func NewAgentController(configDir, version string, logf func(format string, args ...interface{})) (*AgentController, error) {
	nodes, err := NewNodeRegistry(configDir)
	if err != nil {
		return nil, err
	}
	c := &AgentController{Nodes: nodes, Hub: NewAgentHub(nodes, version, logf), configDir: configDir, logf: logf}
	if len(nodes.Nodes()) > 0 {
		if _, err := c.Gateway(true); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Gateway returns the agent gateway. With create it loads or creates the CA first; without it
// returns nil while no CA exists.
func (c *AgentController) Gateway(create bool) (*AgentGateway, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gateway != nil || !create {
		return c.gateway, nil
	}
	ca, err := LoadOrCreateControllerCA(c.configDir)
	if err != nil {
		return nil, err
	}
	gw := NewAgentGateway(ca, c.Nodes)
	gw.Limiter = NewLoginLimiter()
	gw.Logf = c.logf
	gw.OnTunnel = c.Hub.Attach
	c.gateway = gw
	return gw, nil
}

// ServeDirect accepts direct carrier connections on l (the --agent-port listener).
func (c *AgentController) ServeDirect(l net.Listener) error {
	gw, err := c.Gateway(true)
	if err != nil {
		return err
	}
	return gw.Serve(l)
}

// RemoveNode deletes a node and closes its tunnel.
func (c *AgentController) RemoveNode(id string) error {
	if err := c.Nodes.RemoveNode(id); err != nil {
		return err
	}
	c.Hub.Disconnect(id)
	return nil
}
