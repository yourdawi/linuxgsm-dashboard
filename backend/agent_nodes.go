package backend

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Node registry of the controller: the paired nodes (nodes.json) and the pending pairing tokens
// (memory only; a restart invalidates them).

// Access modes of a node. Read-only nodes allow viewing plus start, stop and restart.
const (
	AgentModeReadOnly  = "read-only"
	AgentModeReadWrite = "read-write"
)

const (
	nodesFileName = "nodes.json"
	// AgentPairingValidity is how long a pairing token can be used.
	AgentPairingValidity = 15 * time.Minute
	// maxPendingPairings bounds the tokens an administrator can have open at the same time.
	maxPendingPairings = 50
)

// IsValidAgentMode validates access modes.
func IsValidAgentMode(mode string) bool {
	return mode == AgentModeReadOnly || mode == AgentModeReadWrite
}

// LowerAgentMode returns the more restrictive of two access modes.
func LowerAgentMode(a, b string) string {
	if a == AgentModeReadWrite && b == AgentModeReadWrite {
		return AgentModeReadWrite
	}
	return AgentModeReadOnly
}

// Node is a paired agent as stored by the controller.
type Node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Mode is the access mode the controller allows. The node may restrict it further.
	Mode string `json:"mode"`
	// Keys are the public key fingerprints the node may connect with (PublicKeyFingerprint).
	Keys      []string  `json:"keys,omitempty"`
	MachineID string    `json:"machine_id,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	PairedAt  time.Time `json:"paired_at"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	Version   string    `json:"version,omitempty"`
}

type pendingPairing struct {
	nodeID  string
	name    string
	mode    string
	repair  bool
	expires time.Time
}

// NodeRegistry stores the paired nodes of the controller.
type NodeRegistry struct {
	path string
	now  func() time.Time

	mu       sync.Mutex
	nodes    map[string]*Node
	pairings map[string]*pendingPairing // key: hex SHA-256 of the normalized token
}

// NewNodeRegistry loads <configDir>/nodes.json (a missing file means no nodes).
func NewNodeRegistry(configDir string) (*NodeRegistry, error) {
	r := &NodeRegistry{
		path:     filepath.Join(configDir, nodesFileName),
		now:      time.Now,
		nodes:    make(map[string]*Node),
		pairings: make(map[string]*pendingPairing),
	}
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Node
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", r.path, err)
	}
	for _, n := range list {
		if n == nil || !IsValidNodeID(n.ID) || r.nodes[n.ID] != nil {
			return nil, fmt.Errorf("%s: invalid or duplicate node entry", r.path)
		}
		if !IsValidAgentMode(n.Mode) {
			n.Mode = AgentModeReadOnly
		}
		r.nodes[n.ID] = n
	}
	return r, nil
}

func (r *NodeRegistry) saveLocked() error {
	list := make([]*Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(r.path, data, 0600)
}

// Nodes returns copies of all nodes sorted by ID.
func (r *NodeRegistry) Nodes() []Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		c := *n
		c.Keys = append([]string(nil), n.Keys...)
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// Node returns a copy of one node.
func (r *NodeRegistry) Node(id string) (Node, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return Node{}, false
	}
	c := *n
	c.Keys = append([]string(nil), n.Keys...)
	return c, true
}

// nodeIDFromName derives a node ID ([a-z0-9-], max. 32) from a display name.
func nodeIDFromName(name string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b.WriteRune(c)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if len(id) > 28 {
		id = strings.Trim(id[:28], "-")
	}
	if id == "" {
		id = "node"
	}
	return id
}

func (r *NodeRegistry) idTakenLocked(id string) bool {
	if r.nodes[id] != nil {
		return true
	}
	for _, p := range r.pairings {
		if p.nodeID == id && !p.repair {
			return true
		}
	}
	return false
}

func (r *NodeRegistry) pruneLocked(now time.Time) {
	for k, p := range r.pairings {
		if !now.Before(p.expires) {
			delete(r.pairings, k)
		}
	}
}

// normalizePairingToken removes grouping characters and case differences.
func normalizePairingToken(token string) string {
	token = strings.ToUpper(token)
	return strings.NewReplacer("-", "", " ", "").Replace(token)
}

func pairingKey(token string) string {
	sum := sha256.Sum256([]byte(normalizePairingToken(token)))
	return hex.EncodeToString(sum[:])
}

// PairingTicket is what an administrator gets for adding a node.
type PairingTicket struct {
	Token   string    `json:"token"`
	NodeID  string    `json:"node_id"`
	Name    string    `json:"name"`
	Mode    string    `json:"mode"`
	Expires time.Time `json:"expires"`
}

// CreatePairing creates a single-use pairing token for a new node called name, or, when repairID
// is set, for re-pairing that existing node (its ID, user assignments and tags are kept).
func (r *NodeRegistry) CreatePairing(name, mode, repairID string) (PairingTicket, error) {
	name = sanitizeDisplayText(strings.TrimSpace(name), 64)
	if !IsValidAgentMode(mode) {
		return PairingTicket{}, errors.New("invalid access mode")
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return PairingTicket{}, err
	}
	plain := base32NoPad.EncodeToString(raw) // 32 characters
	groups := make([]string, 0, 8)
	for i := 0; i < len(plain); i += 4 {
		groups = append(groups, plain[i:i+4])
	}
	token := strings.Join(groups, "-")

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.pruneLocked(now)
	if len(r.pairings) >= maxPendingPairings {
		return PairingTicket{}, errors.New("too many open pairing tokens")
	}

	p := &pendingPairing{mode: mode, expires: now.Add(AgentPairingValidity)}
	if repairID != "" {
		n, ok := r.nodes[repairID]
		if !ok {
			return PairingTicket{}, errors.New("node not found")
		}
		p.nodeID, p.name, p.repair = n.ID, n.Name, true
	} else {
		if name == "" {
			return PairingTicket{}, errors.New("node name is required")
		}
		base := nodeIDFromName(name)
		id := base
		for i := 2; r.idTakenLocked(id); i++ {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		p.nodeID, p.name = id, name
	}
	r.pairings[pairingKey(token)] = p
	return PairingTicket{Token: token, NodeID: p.nodeID, Name: p.name, Mode: p.mode, Expires: p.expires}, nil
}

// ErrInvalidPairingToken is returned for unknown, used and expired tokens alike.
var ErrInvalidPairingToken = errors.New("invalid or expired pairing token")

// EnrollRequest is sent by an agent that pairs with the controller.
type EnrollRequest struct {
	Token     string `json:"token"`
	CSR       []byte `json:"csr"`
	MachineID string `json:"machine_id"`
	Hostname  string `json:"hostname"`
	Version   string `json:"version"`
}

// EnrollResponse is the controller's answer to an EnrollRequest.
type EnrollResponse struct {
	NodeID string `json:"node_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Cert   []byte `json:"cert,omitempty"`
	CA     []byte `json:"ca,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Enroll consumes a pairing token and issues the node certificate. The token is consumed by the
// first attempt that names it, also when that attempt fails for another reason.
func (r *NodeRegistry) Enroll(req EnrollRequest, ca *ControllerCA) (EnrollResponse, error) {
	key := pairingKey(req.Token)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.pruneLocked(now)
	p, ok := r.pairings[key]
	if !ok {
		return EnrollResponse{}, ErrInvalidPairingToken
	}
	delete(r.pairings, key)

	certDER, err := ca.SignNodeCSR(req.CSR, p.nodeID, now)
	if err != nil {
		return EnrollResponse{}, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return EnrollResponse{}, err
	}

	n := r.nodes[p.nodeID]
	if p.repair {
		if n == nil {
			return EnrollResponse{}, errors.New("node was removed")
		}
	} else {
		if n != nil {
			return EnrollResponse{}, errors.New("node ID already in use")
		}
		n = &Node{ID: p.nodeID, Name: p.name}
	}
	// Re-pairing replaces the keys: the old installation can no longer connect.
	n.Keys = []string{PublicKeyFingerprint(cert)}
	n.Mode = p.mode
	n.MachineID = sanitizeDisplayText(req.MachineID, 64)
	n.Hostname = sanitizeDisplayText(req.Hostname, 64)
	n.Version = sanitizeDisplayText(req.Version, 64)
	n.PairedAt = now
	r.nodes[n.ID] = n
	if err := r.saveLocked(); err != nil {
		if !p.repair {
			delete(r.nodes, n.ID)
		}
		return EnrollResponse{}, err
	}
	return EnrollResponse{NodeID: n.ID, Name: n.Name, Mode: n.Mode, Cert: certDER, CA: ca.CACertDER()}, nil
}

// VerifyNodeCert accepts a verified client certificate only when its node exists and its public
// key is listed for that node.
func (r *NodeRegistry) VerifyNodeCert(leaf *x509.Certificate) error {
	id := leaf.Subject.CommonName
	fp := PublicKeyFingerprint(leaf)
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return fmt.Errorf("node %q is not paired", id)
	}
	for _, k := range n.Keys {
		if k == fp {
			return nil
		}
	}
	return fmt.Errorf("node %q connected with a key that is not paired", id)
}

// AddNodeKey allows an additional key for a node (certificate renewal). The previous keys stay
// valid until RetireNodeKeys is called after the node connected with the new one.
func (r *NodeRegistry) AddNodeKey(id, fingerprint string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return errors.New("node not found")
	}
	for _, k := range n.Keys {
		if k == fingerprint {
			return nil
		}
	}
	n.Keys = append(n.Keys, fingerprint)
	return r.saveLocked()
}

// RetireNodeKeys keeps only the given key of a node.
func (r *NodeRegistry) RetireNodeKeys(id, keep string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return errors.New("node not found")
	}
	if len(n.Keys) == 1 && n.Keys[0] == keep {
		return nil
	}
	n.Keys = []string{keep}
	return r.saveLocked()
}

// RemoveNode deletes a node; its keys stop working immediately.
func (r *NodeRegistry) RemoveNode(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[id]; !ok {
		return errors.New("node not found")
	}
	delete(r.nodes, id)
	for k, p := range r.pairings {
		if p.nodeID == id {
			delete(r.pairings, k)
		}
	}
	return r.saveLocked()
}

// UpdateNode changes name and access mode of a node.
func (r *NodeRegistry) UpdateNode(id, name, mode string) error {
	name = sanitizeDisplayText(strings.TrimSpace(name), 64)
	if name == "" {
		return errors.New("node name is required")
	}
	if !IsValidAgentMode(mode) {
		return errors.New("invalid access mode")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return errors.New("node not found")
	}
	n.Name, n.Mode = name, mode
	return r.saveLocked()
}

// ErrNodeMachineMismatch means a node identity is used on another machine than the one it was
// paired on, typically a cloned VM or LXC template.
var ErrNodeMachineMismatch = errors.New("this node identity is used by another machine; run `agent join` again on the cloned machine")

// CheckMachineID compares the machine ID an agent reports with the one stored at pairing. Agents
// without a machine ID (some containers) are not checked.
func (r *NodeRegistry) CheckMachineID(id, machineID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return errors.New("node not found")
	}
	if n.MachineID != "" && machineID != "" && n.MachineID != sanitizeDisplayText(machineID, 64) {
		return ErrNodeMachineMismatch
	}
	return nil
}

// Seen records a connection of a node; an empty version keeps the known one. The file is
// written at most every minute.
func (r *NodeRegistry) Seen(id, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return
	}
	now := r.now()
	version = sanitizeDisplayText(version, 64)
	if version == "" {
		version = n.Version
	}
	if now.Sub(n.LastSeen) < time.Minute && n.Version == version {
		return
	}
	n.LastSeen = now
	n.Version = version
	_ = r.saveLocked()
}
