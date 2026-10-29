package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"time"
)

// Server lists of the nodes. The controller polls every connected node itself and serves every
// browser from this cache, so the load on a node does not grow with the number of open
// dashboards. Entries keep the last known state while a node is offline.

const (
	// AgentServerPollInterval is how often the server list of a node is refreshed.
	AgentServerPollInterval = 5 * time.Second
	agentListTimeout        = 20 * time.Second
	// maxAgentListBytes bounds a node's server list answer.
	maxAgentListBytes = 8 << 20
	maxAgentServers   = 500
)

func (h *AgentHub) pollServers(nodeID string, client *AgentTunnelClient) {
	tick := time.NewTicker(AgentServerPollInterval)
	defer tick.Stop()
	for {
		if err := h.refreshServers(nodeID, client); err != nil {
			h.logf("[AGENT] Node %s: server list failed: %v", nodeID, err)
		}
		select {
		case <-client.Done():
			return
		case <-tick.C:
		}
	}
}

// RefreshServers fetches the server list of a node now (for example after a server was
// installed or deleted there).
func (h *AgentHub) RefreshServers(nodeID string) error {
	client, ok := h.Tunnel(nodeID)
	if !ok || !h.Status(nodeID).Compatible {
		return fmt.Errorf("node %s is not connected", nodeID)
	}
	return h.refreshServers(nodeID, client)
}

func (h *AgentHub) refreshServers(nodeID string, client *AgentTunnelClient) error {
	list, err := fetchNodeServers(client, nodeID)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tunnels[nodeID] == client {
		h.servers[nodeID] = list
	}
	return nil
}

func fetchNodeServers(client *AgentTunnelClient, nodeID string) ([]GameServerInstance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentListTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/servers", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	var raw []GameServerInstance
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAgentListBytes)).Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) > maxAgentServers {
		raw = raw[:maxAgentServers]
	}
	list := make([]GameServerInstance, 0, len(raw))
	seen := make(map[string]bool)
	for _, s := range raw {
		if clean, ok := sanitizeNodeServer(s, nodeID); ok && !seen[clean.ID] {
			seen[clean.ID] = true
			list = append(list, clean)
		}
	}
	return list, nil
}

// sanitizeNodeServer rebuilds a server entry received from a node. Nothing is passed through
// unchecked: the UI treats some locally generated fields (IDs, user and script names) as safe,
// so a compromised node must not be able to choose them freely.
func sanitizeNodeServer(s GameServerInstance, nodeID string) (GameServerInstance, bool) {
	if !IsValidLocalServerID(s.ID) {
		return GameServerInstance{}, false
	}
	text := func(v string, n int) string { return sanitizeDisplayText(v, n) }
	num := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return 0
		}
		return v
	}
	count := func(v int) int {
		if v < 0 || v > 1_000_000 {
			return 0
		}
		return v
	}
	port := func(v int) int {
		if v < 0 || v > 65535 {
			return 0
		}
		return v
	}

	out := GameServerInstance{
		ID:     GlobalServerID(nodeID, s.ID),
		Node:   nodeID,
		Name:   text(s.Name, 128),
		Dir:    text(s.Dir, 512),
		Status: text(s.Status, 16),
		Port:   port(s.Port),
		Game:   text(s.Game, 64),
		CPU:    num(s.CPU),
		RAM:    num(s.RAM),
	}
	if IsValidLinuxUsername(s.User) {
		out.User = s.User
	}
	if IsValidScriptName(s.Script) {
		out.Script = s.Script
	}
	for i, p := range s.ParsedPorts {
		if i == 32 {
			break
		}
		out.ParsedPorts = append(out.ParsedPorts, PortProbe{
			Port: port(p.Port), Protocol: text(p.Protocol, 8), Open: p.Open, Description: text(p.Description, 64),
		})
	}
	q := s.QueryData
	out.QueryData = QueryInfo{
		Online: q.Online, Protocol: text(q.Protocol, 16), Name: text(q.Name, 128), Map: text(q.Map, 128),
		NumPlayers: count(q.NumPlayers), MaxPlayers: count(q.MaxPlayers), Bots: count(q.Bots), Ping: count(q.Ping),
		Updated: q.Updated,
	}
	for i, p := range q.Players {
		if i == 256 {
			break
		}
		out.QueryData.Players = append(out.QueryData.Players, PlayerInfo{Name: text(p.Name, 64), Score: p.Score, Time: text(p.Time, 32)})
	}
	if d := s.Disk; d != nil {
		out.Disk = &DiskUsage{
			Used: max(d.Used, 0), Backups: max(d.Backups, 0), Files: max(d.Files, 0),
			FSTotal: max(d.FSTotal, 0), FSFree: max(d.FSFree, 0), FSMount: text(d.FSMount, 512),
			Shared: d.Shared, Updated: d.Updated,
		}
	}
	return out, true
}

// NodeServers returns the cached servers of all nodes with node name, offline flag and the
// node's capabilities. Nodes that are not compatible contribute nothing.
func (h *AgentHub) NodeServers() []GameServerInstance {
	names := make(map[string]string)
	modes := make(map[string]string)
	for _, n := range h.nodes.Nodes() {
		names[n.ID] = n.Name
		modes[n.ID] = n.Mode
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []GameServerInstance
	for nodeID, list := range h.servers {
		name, paired := names[nodeID]
		if !paired {
			continue
		}
		_, online := h.tunnels[nodeID]
		var caps *Capabilities
		mode := AgentModeReadOnly
		if st := h.status[nodeID]; st.Hello != nil {
			c := st.Hello.Capabilities
			caps = &c
			mode = LowerAgentMode(modes[nodeID], st.Hello.Mode)
		}
		for _, s := range list {
			s.NodeName = name
			s.NodeOffline = !online
			s.Capabilities = caps
			s.NodeMode = mode
			if !online {
				s.CPU, s.RAM = 0, 0
				s.QueryData.Online = false
			}
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NodeServer returns one cached server by global ID.
func (h *AgentHub) NodeServer(globalID string) (GameServerInstance, bool) {
	node, _ := SplitServerID(globalID)
	if node == "" {
		return GameServerInstance{}, false
	}
	for _, s := range h.NodeServers() {
		if s.ID == globalID {
			return s, true
		}
	}
	return GameServerInstance{}, false
}
