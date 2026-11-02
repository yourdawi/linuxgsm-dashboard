package backend

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Game traffic proxy: players connect to the public address of a proxy machine, which forwards the
// game ports through WireGuard to a node that opens no port itself. The game server sees the real
// address of the player, so nothing about the game has to change.
//
// The feature is optional. Nothing here runs, and no package or rule exists, until an administrator
// sets up a proxy.
//
// This file holds the plan of a machine and turns it into configuration, without side effects.

const (
	proxyInterface   = "wg-lgsm"
	proxyTableProxy  = "lgsm_proxy"
	proxyTableNode   = "lgsm_node"
	proxyRouteTable  = "7701"
	proxyRulePrio    = "7701"
	proxyFlowMark    = "0x77"
	proxyTunnelMark  = "0x78" // outer WireGuard packets, which keep the owner of the inner packet
	proxyDefaultPort = 51820
	proxyNodePort    = 51821
	// The tunnel carries IPv4 inside IPv4 or IPv6 and has to fit a PPPoE line.
	proxyTunnelMTU = 1380
	proxySubnet    = "10.77.0.0/24"
	proxyProxyIP   = "10.77.0.1"
)

// ProxyForward is one game port a proxy offers to players.
type ProxyForward struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"` // udp or tcp
}

// ProxyPeer is a node as seen by its proxy.
type ProxyPeer struct {
	PublicKey string         `json:"public_key"`
	TunnelIP  string         `json:"tunnel_ip"`
	Forwards  []ProxyForward `json:"forwards"`
	// PresharedKey adds post-quantum protection; it reaches both ends over the hybrid TLS tunnel.
	PresharedKey string `json:"preshared_key,omitempty"`
	// Egress lets this node reach the internet through the proxy.
	Egress bool `json:"egress,omitempty"`
}

// ProxySide makes this machine a proxy.
type ProxySide struct {
	ListenPort int         `json:"listen_port"`
	Peers      []ProxyPeer `json:"peers"`
}

// NodeSide connects this machine to a proxy. It dials out, so it needs no open port.
type NodeSide struct {
	TunnelIP       string         `json:"tunnel_ip"`
	ProxyPublicKey string         `json:"proxy_public_key"`
	Endpoint       string         `json:"endpoint"` // host:port of the proxy
	Forwards       []ProxyForward `json:"forwards"`
	// TargetIP is the address the game server is bound to; empty is the machine's primary address.
	TargetIP     string `json:"target_ip,omitempty"`
	PresharedKey string `json:"preshared_key,omitempty"`
	// EgressUsers are the Linux users whose outgoing traffic leaves through the proxy, so that for
	// example Steam sees the proxy's address. Empty means incoming traffic only.
	EgressUsers []string `json:"egress_users,omitempty"`
}

// ProxyPlan is what one machine has to do. Without a side there is nothing to do.
type ProxyPlan struct {
	Proxy *ProxySide `json:"proxy,omitempty"`
	Node  *NodeSide  `json:"node,omitempty"`
}

// Empty reports whether the plan asks for nothing.
func (p ProxyPlan) Empty() bool { return p.Proxy == nil && p.Node == nil }

// proxyReservedPorts would lock out the machine or collide with its own services.
var proxyReservedPorts = map[int]bool{22: true, 53: true, 80: true, 443: true, 8080: true, proxyDefaultPort: true, proxyNodePort: true}

var reProxyHost = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func validProxyKey(key string) bool {
	raw, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(raw) == 32
}

func validTunnelIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil || ip == proxyProxyIP {
		return false
	}
	_, subnet, _ := net.ParseCIDR(proxySubnet)
	b := parsed.To4()
	return subnet.Contains(parsed) && b[3] >= 2 && b[3] <= 254
}

// ValidateProxyForwards checks ports before they reach a rule. Reserved ports and duplicates are
// refused, as are ports below 1024, which no game server user can bind.
func ValidateProxyForwards(forwards []ProxyForward, listenPort int) error {
	if len(forwards) == 0 {
		return errors.New("no ports to forward")
	}
	if len(forwards) > 64 {
		return errors.New("too many ports")
	}
	seen := map[string]bool{}
	for _, f := range forwards {
		if f.Protocol != "udp" && f.Protocol != "tcp" {
			return fmt.Errorf("invalid protocol %q", f.Protocol)
		}
		if f.Port < 1024 || f.Port > 65535 {
			return fmt.Errorf("invalid port %d (1024 to 65535)", f.Port)
		}
		if proxyReservedPorts[f.Port] || f.Port == listenPort {
			return fmt.Errorf("port %d is reserved", f.Port)
		}
		key := f.Protocol + "/" + strconv.Itoa(f.Port)
		if seen[key] {
			return fmt.Errorf("port %s is listed twice", key)
		}
		seen[key] = true
	}
	return nil
}

// ValidateProxyEndpoint checks the host:port a node dials. A hostname, IPv4 or bracketed IPv6.
func ValidateProxyEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return errors.New("invalid proxy address (host:port)")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return errors.New("invalid proxy port")
	}
	if net.ParseIP(host) == nil && !reProxyHost.MatchString(host) {
		return errors.New("invalid proxy address")
	}
	return nil
}

// Validate checks a plan. Machines validate it themselves: it ends up in rules run as root.
func (p ProxyPlan) Validate() error {
	if p.Proxy != nil && p.Node != nil {
		return errors.New("a machine is either a proxy or connected to one")
	}
	if s := p.Proxy; s != nil {
		if s.ListenPort < 1024 || s.ListenPort > 65535 {
			return errors.New("invalid listen port")
		}
		if len(s.Peers) == 0 {
			return errors.New("a proxy needs at least one node")
		}
		seen := map[string]bool{}
		var all []ProxyForward
		for _, peer := range s.Peers {
			if !validProxyKey(peer.PublicKey) {
				return errors.New("invalid public key of a node")
			}
			if !validTunnelIP(peer.TunnelIP) || seen[peer.TunnelIP] {
				return errors.New("invalid tunnel address of a node")
			}
			if peer.PresharedKey != "" && !validProxyKey(peer.PresharedKey) {
				return errors.New("invalid preshared key of a node")
			}
			seen[peer.TunnelIP] = true
			all = append(all, peer.Forwards...)
		}
		// A port leads to one node, across all nodes of this proxy.
		if err := ValidateProxyForwards(all, s.ListenPort); err != nil {
			return err
		}
	}
	if s := p.Node; s != nil {
		if !validTunnelIP(s.TunnelIP) {
			return errors.New("invalid tunnel address")
		}
		if !validProxyKey(s.ProxyPublicKey) {
			return errors.New("invalid public key of the proxy")
		}
		if err := ValidateProxyEndpoint(s.Endpoint); err != nil {
			return err
		}
		if err := ValidateProxyForwards(s.Forwards, 0); err != nil {
			return err
		}
		if s.TargetIP != "" && net.ParseIP(s.TargetIP) == nil {
			return errors.New("invalid target address")
		}
		if s.PresharedKey != "" && !validProxyKey(s.PresharedKey) {
			return errors.New("invalid preshared key")
		}
		if err := validateEgressUsers(s.EgressUsers); err != nil {
			return err
		}
	}
	return nil
}

func validateEgressUsers(users []string) error {
	for _, name := range users {
		if !IsValidLinuxUsername(name) || name == "root" {
			return fmt.Errorf("invalid user for outgoing traffic: %q", name)
		}
	}
	return nil
}

// proxyNoEgress are addresses that never leave through the proxy: a node keeps its LAN, and a node
// cannot reach the private network or the metadata service of its proxy.
const proxyNoEgress = "{ 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4 }"

// Limits on the proxy. Players open few connections, floods and abuse of the proxy's address many.
const (
	proxyNewFlowLimit   = "rate over 30/second burst 60 packets"   // per source, towards the game ports
	proxyEgressNewLimit = "rate over 100/second burst 200 packets" // per node, towards the internet
)

// portSet renders the ports of one protocol as an nft set, "" when there are none.
func portSet(forwards []ProxyForward, protocol string) string {
	var ports []int
	for _, f := range forwards {
		if f.Protocol == protocol {
			ports = append(ports, f.Port)
		}
	}
	if len(ports) == 0 {
		return ""
	}
	sort.Ints(ports)
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// replaceTable makes loading atomic: an existing table is dropped and rebuilt in one transaction.
func replaceTable(name, body string) string {
	return fmt.Sprintf("table inet %s {}\ndelete table inet %s\ntable inet %s {\n%s}\n", name, name, name, body)
}

// renderProxyRules forwards the game ports from the public interface to the nodes. The player's
// address stays: no SNAT. Traffic of other interfaces is none of these rules' business; a host with
// Docker or a VPN keeps working.
func renderProxyRules(s *ProxySide, publicIface string) string {
	var pre, fwd, egress, snat strings.Builder
	for _, peer := range s.Peers {
		if peer.Egress {
			fmt.Fprintf(&egress, "    iifname %q oifname %q ip saddr %s ip daddr %s drop\n", proxyInterface, publicIface, peer.TunnelIP, proxyNoEgress)
			fmt.Fprintf(&egress, "    iifname %q oifname %q ip saddr %s ct state new limit %s drop\n", proxyInterface, publicIface, peer.TunnelIP, proxyEgressNewLimit)
			fmt.Fprintf(&egress, "    iifname %q oifname %q ip saddr %s accept\n", proxyInterface, publicIface, peer.TunnelIP)
			fmt.Fprintf(&snat, "    oifname %q ip saddr %s masquerade\n", publicIface, peer.TunnelIP)
		}
		for _, proto := range []string{"udp", "tcp"} {
			set := portSet(peer.Forwards, proto)
			if set == "" {
				continue
			}
			fmt.Fprintf(&pre, "    iifname %q %s dport %s dnat ip to %s\n", publicIface, proto, set, peer.TunnelIP)
			fmt.Fprintf(&fwd, "    iifname %q oifname %q ip daddr %s %s dport %s accept\n", publicIface, proxyInterface, peer.TunnelIP, proto, set)
		}
	}
	body := "  set s_flows {\n    type ipv4_addr; size 65535; flags dynamic,timeout; timeout 1m;\n  }\n" +
		// The tunnel gives a node no way to the services of its proxy.
		"  chain c_in {\n    type filter hook input priority filter; policy accept;\n" +
		fmt.Sprintf("    iifname %q ct state established,related accept\n", proxyInterface) +
		fmt.Sprintf("    iifname %q meta l4proto icmp accept\n    iifname %q drop\n  }\n", proxyInterface, proxyInterface) +
		"  chain c_pre {\n    type nat hook prerouting priority dstnat; policy accept;\n" + pre.String() + "  }\n" +
		"  chain c_fwd {\n    type filter hook forward priority filter; policy accept;\n" +
		fmt.Sprintf("    iifname %q tcp flags syn tcp option maxseg size set rt mtu\n", proxyInterface) +
		fmt.Sprintf("    oifname %q tcp flags syn tcp option maxseg size set rt mtu\n", proxyInterface) +
		fmt.Sprintf("    iifname %q ct state established,related accept\n", proxyInterface) +
		fmt.Sprintf("    oifname %q ct state established,related accept\n", proxyInterface) +
		fmt.Sprintf("    iifname %q oifname %q ct state new add @s_flows { ip saddr limit %s } drop\n", publicIface, proxyInterface, proxyNewFlowLimit) +
		fwd.String() + egress.String() +
		fmt.Sprintf("    iifname %q drop\n    oifname %q drop\n  }\n", proxyInterface, proxyInterface)
	if snat.Len() > 0 {
		body += "  chain c_snat {\n    type nat hook postrouting priority srcnat; policy accept;\n" + snat.String() + "  }\n"
	}
	return replaceTable(proxyTableProxy, body)
}

// renderNodeRules accepts only the forwarded ports from the tunnel and sends the answers back
// through it.
//
// The DNAT to the node's own address matters. A game server socket on 0.0.0.0 picks the address of
// the route to the player as its source, which is the LAN address. Without the DNAT that answer
// would not belong to the connection, would not get the mark and would leave through the home line,
// revealing its address to the player.
//
// With egressUIDs, new connections of these users get the same mark and leave through the tunnel
// with the tunnel address as their source. Answers to players keep working as above, also for other
// users on this machine and for players in its LAN, because they belong to an incoming connection.
func renderNodeRules(s *NodeSide, targetIP string, egressUIDs []int) string {
	var in, nat strings.Builder
	for _, proto := range []string{"udp", "tcp"} {
		set := portSet(s.Forwards, proto)
		if set == "" {
			continue
		}
		fmt.Fprintf(&in, "    iifname %q %s dport %s accept\n", proxyInterface, proto, set)
		fmt.Fprintf(&nat, "    iifname %q %s dport %s dnat ip to %s\n", proxyInterface, proto, set, targetIP)
	}
	body := "  chain c_in {\n    type filter hook input priority filter; policy accept;\n" +
		fmt.Sprintf("    iifname %q ct state established,related accept\n", proxyInterface) +
		in.String() +
		fmt.Sprintf("    iifname %q meta l4proto icmp accept\n    iifname %q drop\n  }\n", proxyInterface, proxyInterface) +
		"  chain c_mangle {\n    type filter hook prerouting priority mangle; policy accept;\n" +
		fmt.Sprintf("    iifname %q ct state new ct mark set %s\n  }\n", proxyInterface, proxyFlowMark) +
		"  chain c_out {\n    type route hook output priority mangle; policy accept;\n" +
		fmt.Sprintf("    meta mark %s return\n", proxyTunnelMark)
	if len(egressUIDs) > 0 {
		uids := make([]string, len(egressUIDs))
		for i, uid := range egressUIDs {
			uids[i] = strconv.Itoa(uid)
		}
		users := "{ " + strings.Join(uids, ", ") + " }"
		body += fmt.Sprintf("    ct state new meta skuid %s ip daddr != %s ct mark set %s\n", users, proxyNoEgress, proxyFlowMark)
		body += fmt.Sprintf("    ct mark %s meta mark set %s\n  }\n", proxyFlowMark, proxyFlowMark)
		// IPv6 would pass the tunnel, which only carries IPv4, and show the home address.
		body += "  chain c_out6 {\n    type filter hook output priority filter; policy accept;\n" +
			fmt.Sprintf("    meta mark %s return\n", proxyTunnelMark) +
			fmt.Sprintf("    meta nfproto ipv6 meta skuid %s ip6 daddr != { ::1, fe80::/10, fc00::/7 } reject\n  }\n", users)
		body += fmt.Sprintf("  chain c_snat {\n    type nat hook postrouting priority srcnat; policy accept;\n    oifname %q ip saddr != %s snat ip to %s\n  }\n", proxyInterface, s.TunnelIP, s.TunnelIP)
		// The socket chose its packet size for the LAN route.
		body += fmt.Sprintf("  chain c_mss {\n    type filter hook postrouting priority mangle; policy accept;\n    oifname %q tcp flags syn tcp option maxseg size set rt mtu\n  }\n", proxyInterface)
	} else {
		body += fmt.Sprintf("    ct mark %s meta mark set %s\n  }\n", proxyFlowMark, proxyFlowMark)
	}
	body += "  chain c_nat {\n    type nat hook prerouting priority dstnat; policy accept;\n" + nat.String() + "  }\n"
	return replaceTable(proxyTableNode, body)
}

// renderWGConfig renders the configuration for `wg syncconf`. It holds the private key, so it only
// ever travels through a pipe.
func renderWGConfig(plan ProxyPlan, privateKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\n", privateKey)
	if s := plan.Proxy; s != nil {
		fmt.Fprintf(&b, "ListenPort = %d\n", s.ListenPort)
		for _, peer := range s.Peers {
			fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n", peer.PublicKey, peer.TunnelIP)
			if peer.PresharedKey != "" {
				fmt.Fprintf(&b, "PresharedKey = %s\n", peer.PresharedKey)
			}
		}
	}
	if s := plan.Node; s != nil {
		// Without a fixed port every syncconf binds a new one, and the proxy sends to the old one
		// until the next keepalive of the node. Players lose their packets in that gap.
		fmt.Fprintf(&b, "ListenPort = %d\nFwMark = %s\n", proxyNodePort, proxyTunnelMark)
		// Replies go to any player, so the proxy may send any source. This does not route the
		// machine through the tunnel: only marked connections use it.
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 25\n", s.ProxyPublicKey, s.Endpoint)
		if s.PresharedKey != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", s.PresharedKey)
		}
	}
	return b.String()
}
