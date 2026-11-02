package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The dashboard side of the proxy. It knows which node goes through which proxy and tells every
// machine its plan. Machines keep their plan and work without the dashboard. Without an entry here,
// no machine is ever contacted about the proxy.

const proxyConfigFile = "proxy.json"

// ProxyLocal names this dashboard's own machine as a proxy or as a node.
const ProxyLocal = "@local"

// ProxyLink connects a node to a proxy.
type ProxyLink struct {
	Proxy          string         `json:"proxy"`       // node ID or ProxyLocal
	Node           string         `json:"node"`        // node ID or ProxyLocal
	PublicHost     string         `json:"public_host"` // address players and the node use
	ListenPort     int            `json:"listen_port"` // WireGuard port of the proxy
	ProxyPublicKey string         `json:"proxy_public_key"`
	NodePublicKey  string         `json:"node_public_key"`
	TunnelIP       string         `json:"tunnel_ip"`
	Forwards       []ProxyForward `json:"forwards"`
	TargetIP       string         `json:"target_ip,omitempty"`
	// Warnings of the last setup, for the answer to the administrator.
	Warnings []ProxyCheck `json:"-"`
}

type proxyConfig struct {
	Links []ProxyLink `json:"links"`
	// Cleanup lists machines that still have to remove the proxy, for example while offline.
	Cleanup []string `json:"cleanup,omitempty"`
}

// ProxyEndpoint is a machine the controller talks to: this one or a node through its tunnel.
type ProxyEndpoint interface {
	Prepare(ctx context.Context, asProxy bool) (ProxyPrepared, error)
	Apply(ctx context.Context, plan ProxyPlan) error
	Status(ctx context.Context) (ProxyHostStatus, error)
}

// ProxySetupError names the checks that stopped a setup.
type ProxySetupError struct {
	Machine string       `json:"machine"`
	Checks  []ProxyCheck `json:"checks"`
}

func (e *ProxySetupError) Error() string {
	var parts []string
	for _, c := range e.Checks {
		if c.Fatal {
			parts = append(parts, c.Detail)
		}
	}
	return e.Machine + ": " + strings.Join(parts, " ")
}

// ProxyController stores the links and pushes plans.
type ProxyController struct {
	path     string
	endpoint func(id string) (ProxyEndpoint, error)

	mu  sync.Mutex
	cfg proxyConfig
}

// NewProxyController loads <configDir>/proxy.json; a missing file means the proxy is not in use.
func NewProxyController(configDir string, endpoint func(id string) (ProxyEndpoint, error)) (*ProxyController, error) {
	c := &ProxyController{path: filepath.Join(configDir, proxyConfigFile), endpoint: endpoint}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &c.cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", proxyConfigFile, err)
	}
	return c, nil
}

func (c *ProxyController) saveLocked() error {
	data, err := json.MarshalIndent(c.cfg, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(c.path, data, 0600)
}

// Links returns a copy of the links.
func (c *ProxyController) Links() []ProxyLink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ProxyLink{}, c.cfg.Links...)
}

// planFor builds the plan of one machine from a list of links.
func planFor(links []ProxyLink, id string) ProxyPlan {
	var plan ProxyPlan
	for _, l := range links {
		if l.Node == id {
			plan.Node = &NodeSide{
				TunnelIP:       l.TunnelIP,
				ProxyPublicKey: l.ProxyPublicKey,
				Endpoint:       net.JoinHostPort(l.PublicHost, strconv.Itoa(l.ListenPort)),
				Forwards:       l.Forwards,
				TargetIP:       l.TargetIP,
			}
		}
		if l.Proxy == id {
			if plan.Proxy == nil {
				plan.Proxy = &ProxySide{ListenPort: l.ListenPort}
			}
			plan.Proxy.Peers = append(plan.Proxy.Peers, ProxyPeer{PublicKey: l.NodePublicKey, TunnelIP: l.TunnelIP, Forwards: l.Forwards})
		}
	}
	return plan
}

func nextTunnelIP(links []ProxyLink, proxy string) (string, error) {
	used := map[string]bool{}
	for _, l := range links {
		if l.Proxy == proxy {
			used[l.TunnelIP] = true
		}
	}
	for i := 2; i <= 254; i++ {
		ip := "10.77.0." + strconv.Itoa(i)
		if !used[ip] {
			return ip, nil
		}
	}
	return "", errors.New("this proxy has no free tunnel address")
}

// ProxySetup asks for a new link.
type ProxySetup struct {
	Proxy      string         `json:"proxy"`
	Node       string         `json:"node"`
	PublicHost string         `json:"public_host"`
	ListenPort int            `json:"listen_port"`
	Forwards   []ProxyForward `json:"forwards"`
	TargetIP   string         `json:"target_ip"`
}

func checkProxyPorts(links []ProxyLink, proxy, node string, forwards []ProxyForward, listenPort int) error {
	if err := ValidateProxyForwards(forwards, listenPort); err != nil {
		return err
	}
	for _, l := range links {
		if l.Proxy != proxy || l.Node == node {
			continue
		}
		for _, have := range l.Forwards {
			for _, f := range forwards {
				if f == have {
					return fmt.Errorf("%s port %d is already forwarded on this proxy", f.Protocol, f.Port)
				}
			}
		}
	}
	return nil
}

// Setup is the click on "set up proxy": it prepares both machines, which installs the packages
// there, checks them and only then loads the plans. A failure leaves the proxy as it was.
func (c *ProxyController) Setup(ctx context.Context, req ProxySetup) (ProxyLink, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.ListenPort == 0 {
		req.ListenPort = proxyDefaultPort
	}
	if req.Proxy == req.Node || req.Proxy == "" || req.Node == "" {
		return ProxyLink{}, errors.New("choose a proxy and another machine as its node")
	}
	for _, l := range c.cfg.Links {
		if l.Node == req.Node {
			return ProxyLink{}, errors.New("this machine already uses a proxy")
		}
		if l.Node == req.Proxy {
			return ProxyLink{}, errors.New("this machine uses a proxy itself and cannot be one")
		}
		if l.Proxy == req.Node {
			return ProxyLink{}, errors.New("this machine is a proxy and cannot use one")
		}
		if l.Proxy == req.Proxy && l.PublicHost != req.PublicHost {
			return ProxyLink{}, fmt.Errorf("this proxy already uses the address %s", l.PublicHost)
		}
		if l.Proxy == req.Proxy {
			req.ListenPort = l.ListenPort
		}
	}
	if err := ValidateProxyEndpoint(net.JoinHostPort(req.PublicHost, strconv.Itoa(req.ListenPort))); err != nil {
		return ProxyLink{}, err
	}
	if err := checkProxyPorts(c.cfg.Links, req.Proxy, req.Node, req.Forwards, req.ListenPort); err != nil {
		return ProxyLink{}, err
	}
	if req.TargetIP != "" && net.ParseIP(req.TargetIP) == nil {
		return ProxyLink{}, errors.New("invalid target address")
	}
	proxyEP, err := c.endpoint(req.Proxy)
	if err != nil {
		return ProxyLink{}, fmt.Errorf("proxy: %w", err)
	}
	nodeEP, err := c.endpoint(req.Node)
	if err != nil {
		return ProxyLink{}, fmt.Errorf("node: %w", err)
	}

	proxyInfo, err := proxyEP.Prepare(ctx, true)
	if err != nil {
		return ProxyLink{}, fmt.Errorf("proxy: %w", err)
	}
	nodeInfo, err := nodeEP.Prepare(ctx, false)
	if err != nil {
		return ProxyLink{}, fmt.Errorf("node: %w", err)
	}
	for _, res := range []struct {
		machine string
		checks  []ProxyCheck
	}{{"proxy", proxyInfo.Checks}, {"node", nodeInfo.Checks}} {
		for _, check := range res.checks {
			if check.Fatal {
				return ProxyLink{}, &ProxySetupError{Machine: res.machine, Checks: res.checks}
			}
		}
	}

	ip, err := nextTunnelIP(c.cfg.Links, req.Proxy)
	if err != nil {
		return ProxyLink{}, err
	}
	link := ProxyLink{
		Proxy: req.Proxy, Node: req.Node, PublicHost: req.PublicHost, ListenPort: req.ListenPort,
		ProxyPublicKey: proxyInfo.PublicKey, NodePublicKey: nodeInfo.PublicKey,
		TunnelIP: ip, Forwards: req.Forwards, TargetIP: req.TargetIP,
	}
	links := append(append([]ProxyLink{}, c.cfg.Links...), link)
	if err := c.pushLocked(ctx, links, proxyEP, nodeEP, req.Proxy, req.Node); err != nil {
		return ProxyLink{}, err
	}
	c.cfg.Links = links
	c.cfg.Cleanup = removeString(removeString(c.cfg.Cleanup, req.Proxy), req.Node)
	if err := c.saveLocked(); err != nil {
		return ProxyLink{}, err
	}
	for _, check := range append(proxyInfo.Checks, nodeInfo.Checks...) {
		if !check.OK {
			link.Warnings = append(link.Warnings, check)
		}
	}
	return link, nil
}

// pushLocked loads the plans of both ends. When the node refuses, the proxy goes back to its old
// plan, so no half link stays.
func (c *ProxyController) pushLocked(ctx context.Context, links []ProxyLink, proxyEP, nodeEP ProxyEndpoint, proxy, node string) error {
	if err := proxyEP.Apply(ctx, planFor(links, proxy)); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	if err := nodeEP.Apply(ctx, planFor(links, node)); err != nil {
		_ = proxyEP.Apply(ctx, planFor(c.cfg.Links, proxy))
		return fmt.Errorf("node: %w", err)
	}
	return nil
}

// SetForwards changes the ports of a node.
func (c *ProxyController) SetForwards(ctx context.Context, node string, forwards []ProxyForward) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	links := append([]ProxyLink{}, c.cfg.Links...)
	idx := -1
	for i, l := range links {
		if l.Node == node {
			idx = i
		}
	}
	if idx < 0 {
		return errors.New("this machine uses no proxy")
	}
	if err := checkProxyPorts(links, links[idx].Proxy, node, forwards, links[idx].ListenPort); err != nil {
		return err
	}
	links[idx].Forwards = forwards
	proxyEP, err := c.endpoint(links[idx].Proxy)
	if err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	nodeEP, err := c.endpoint(node)
	if err != nil {
		return fmt.Errorf("node: %w", err)
	}
	if err := c.pushLocked(ctx, links, proxyEP, nodeEP, links[idx].Proxy, node); err != nil {
		return err
	}
	c.cfg.Links = links
	return c.saveLocked()
}

// Remove ends the link of a node. A machine with nothing left removes tunnel, rules and the packages
// that were installed for it. Machines that are offline do it as soon as they come back.
func (c *ProxyController) Remove(ctx context.Context, node string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var removed *ProxyLink
	links := []ProxyLink{}
	for i, l := range c.cfg.Links {
		if l.Node == node {
			removed = &c.cfg.Links[i]
			continue
		}
		links = append(links, l)
	}
	if removed == nil {
		return errors.New("this machine uses no proxy")
	}
	proxy := removed.Proxy
	c.cfg.Links = links
	c.cfg.Cleanup = appendUniqueString(appendUniqueString(c.cfg.Cleanup, proxy), node)
	if err := c.saveLocked(); err != nil {
		return err
	}
	c.syncLocked(ctx)
	return nil
}

// Sync brings machines to their plan, for example a node that was offline. With no link and no
// open cleanup it contacts nothing.
func (c *ProxyController) Sync(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncLocked(ctx)
}

func (c *ProxyController) syncLocked(ctx context.Context) {
	if len(c.cfg.Links) == 0 && len(c.cfg.Cleanup) == 0 {
		return
	}
	ids := map[string]bool{}
	for _, l := range c.cfg.Links {
		ids[l.Proxy], ids[l.Node] = true, true
	}
	var open []string
	for _, id := range c.cfg.Cleanup {
		ids[id] = true
		open = append(open, id)
	}
	changed := false
	for id := range ids {
		ep, err := c.endpoint(id)
		if err != nil {
			continue // offline: next time
		}
		plan := planFor(c.cfg.Links, id)
		if err := ep.Apply(ctx, plan); err != nil {
			fmt.Printf("[PROXY] %s: %v\n", id, err)
			continue
		}
		for i, other := range open {
			if other == id {
				open = append(open[:i], open[i+1:]...)
				changed = true
				break
			}
		}
	}
	if changed {
		c.cfg.Cleanup = open
		_ = c.saveLocked()
	}
}

// ForgetNode ends the link of a node that is removed from the dashboard, or that served as proxy.
func (c *ProxyController) ForgetNode(ctx context.Context, id string) {
	for _, l := range c.Links() {
		if l.Node == id || l.Proxy == id {
			_ = c.Remove(ctx, l.Node)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.Cleanup = removeString(c.cfg.Cleanup, id)
	_ = c.saveLocked()
}

func removeString(list []string, s string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// ProxyLinkView is a link with the state of both ends.
type ProxyLinkView struct {
	ProxyLink
	PlayerAddress string           `json:"player_address"` // what players type in
	ProxyStatus   *ProxyHostStatus `json:"proxy_status,omitempty"`
	NodeStatus    *ProxyHostStatus `json:"node_status,omitempty"`
}

// View returns the links with their state. Offline machines have no state.
func (c *ProxyController) View(ctx context.Context) []ProxyLinkView {
	links := c.Links()
	views := make([]ProxyLinkView, 0, len(links))
	read := func(id string) *ProxyHostStatus {
		ep, err := c.endpoint(id)
		if err != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		st, err := ep.Status(ctx)
		if err != nil {
			return nil
		}
		return &st
	}
	for _, l := range links {
		v := ProxyLinkView{ProxyLink: l, ProxyStatus: read(l.Proxy), NodeStatus: read(l.Node)}
		v.PlayerAddress = l.PublicHost
		views = append(views, v)
	}
	return views
}
