package backend

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The machine side of the proxy: packages, keys, the interface, routes and rules. Everything starts
// with an administrator setting up a proxy; before that this code runs no command at all.

const (
	proxyKeyFile   = "proxy-wg.key"
	proxyPlanFile  = "proxy-plan.json"
	proxyStateFile = "proxy-state.json"
)

// proxyPackages are installed on the first setup and removed with the last proxy, unless they were
// on the machine before.
var proxyPackages = []string{"wireguard-tools", "nftables"}

// proxyRunner runs a command as root and returns its output. The service sandbox has read-only
// kernel tunables and no module loading, so commands run outside of it. Tests replace it.
type proxyRunner func(ctx context.Context, stdin string, name string, args ...string) (string, error)

func defaultProxyRunner(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	name, args = outsideServiceSandbox(name, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	return out.String(), err
}

// ProxyCheck is one result of the check before anything is changed.
type ProxyCheck struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Fatal  bool   `json:"fatal,omitempty"` // setup cannot go on
	Detail string `json:"detail,omitempty"`
}

// ProxyPrepared answers a preparation.
type ProxyPrepared struct {
	PublicKey string       `json:"public_key"`
	Checks    []ProxyCheck `json:"checks"`
}

// ProxyHostStatus is the state of this machine.
type ProxyHostStatus struct {
	Active        bool   `json:"active"`
	Role          string `json:"role,omitempty"` // proxy or node
	InterfaceUp   bool   `json:"interface_up"`
	RulesLoaded   bool   `json:"rules_loaded"`
	LastHandshake int64  `json:"last_handshake,omitempty"` // Unix time, newest of all peers
	HandshakeAge  int64  `json:"handshake_age,omitempty"`  // seconds, by the clock of this machine
	RxBytes       int64  `json:"rx_bytes,omitempty"`
	TxBytes       int64  `json:"tx_bytes,omitempty"`
	Error         string `json:"error,omitempty"`
}

type proxyState struct {
	// Installed lists the packages installed for the proxy, so that only those are removed.
	Installed []string `json:"installed,omitempty"`
	// Sysctls holds values changed by the proxy with their previous value.
	Sysctls map[string]string `json:"sysctls,omitempty"`
	// Firewall lists the iptables rules the proxy added, so that an identical rule of the
	// administrator stays. Format: tool chain spec..., separated by spaces.
	Firewall []string `json:"firewall,omitempty"`
	// DisplayIP is the address written to displayip, needed to take it back.
	DisplayIP string `json:"display_ip,omitempty"`
}

// ProxyHost applies the proxy plan of this machine.
type ProxyHost struct {
	dir string
	run proxyRunner
	// NodePorts lists the game ports of this machine. A node refuses other ports, so a dashboard
	// cannot publish its other services. Nil means no check.
	NodePorts func() map[string]bool
	// NodeUsers lists the Linux users of the game servers of this machine. Only their traffic may
	// leave through the proxy. Nil means no check.
	NodeUsers func() map[string]bool
	lookupUID func(name string) (int, error)
	// SyncDisplayIP keeps LinuxGSM's displayip at the address of the proxy. Nil means no change.
	SyncDisplayIP func(ports map[int]bool, oldHost, newHost string) error

	mu       sync.Mutex
	lastLoad string // rules and config of the last load, to notice changed addresses
	lastErr  string
	fwErr    string
}

// NewProxyHost creates the host side; nothing happens until a plan arrives.
func NewProxyHost(configDir string) *ProxyHost {
	return &ProxyHost{dir: configDir, run: defaultProxyRunner, lookupUID: lookupProxyUID}
}

func lookupProxyUID(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

// egressUIDs resolves the users on this machine. Root would send the tunnel itself into the tunnel.
func (h *ProxyHost) egressUIDs(users []string) ([]int, error) {
	uids := make([]int, 0, len(users))
	for _, name := range users {
		uid, err := h.lookupUID(name)
		if err != nil {
			return nil, fmt.Errorf("user %s for outgoing traffic: %w", name, err)
		}
		if uid == 0 {
			return nil, fmt.Errorf("user %s for outgoing traffic is root", name)
		}
		uids = append(uids, uid)
	}
	sort.Ints(uids)
	return uids, nil
}

func (h *ProxyHost) path(name string) string { return filepath.Join(h.dir, name) }

// HasPlan reports whether this machine has been set up. Without a plan the proxy does nothing.
func (h *ProxyHost) HasPlan() bool {
	_, err := os.Stat(h.path(proxyPlanFile))
	return err == nil
}

// PublishedHost returns the proxy's address when this machine publishes the port through a proxy.
func (h *ProxyHost) PublishedHost(port int, protocol string) string {
	plan, err := h.loadPlan()
	if err != nil || plan.Node == nil {
		return ""
	}
	for _, f := range plan.Node.Forwards {
		if f.Port == port && strings.EqualFold(f.Protocol, protocol) {
			host, _, err := net.SplitHostPort(plan.Node.Endpoint)
			if err != nil {
				return ""
			}
			return host
		}
	}
	return ""
}

func (h *ProxyHost) loadPlan() (ProxyPlan, error) {
	var plan ProxyPlan
	data, err := os.ReadFile(h.path(proxyPlanFile))
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return plan, fmt.Errorf("%s: %w", proxyPlanFile, err)
	}
	return plan, plan.Validate()
}

func (h *ProxyHost) loadState() proxyState {
	var st proxyState
	if data, err := os.ReadFile(h.path(proxyStateFile)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func (h *ProxyHost) saveState(st proxyState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(h.path(proxyStateFile), data, 0600)
}

// privateKey returns the key of this machine and creates it on first use. It never leaves the
// machine; the dashboard only sees the public key.
func (h *ProxyHost) privateKey() (string, string, error) {
	curve := ecdh.X25519()
	data, err := os.ReadFile(h.path(proxyKeyFile))
	if errors.Is(err, os.ErrNotExist) {
		key, err := curve.GenerateKey(rand.Reader)
		if err != nil {
			return "", "", err
		}
		data = []byte(base64.StdEncoding.EncodeToString(key.Bytes()))
		if err := WriteFileAtomic(h.path(proxyKeyFile), data, 0600); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	encoded := strings.TrimSpace(string(data))
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", proxyKeyFile, err)
	}
	key, err := curve.NewPrivateKey(raw)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", proxyKeyFile, err)
	}
	return encoded, base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func (h *ProxyHost) sysctlGet(ctx context.Context, key string) string {
	out, err := h.run(ctx, "", "sysctl", "-n", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// setSysctl changes a value and remembers the previous one for the teardown.
func (h *ProxyHost) setSysctl(ctx context.Context, st *proxyState, key, value string) error {
	old := h.sysctlGet(ctx, key)
	if old == value {
		return nil
	}
	if out, err := h.run(ctx, "", "sysctl", "-q", "-w", key+"="+value); err != nil {
		return fmt.Errorf("sysctl %s: %s", key, strings.TrimSpace(out))
	}
	if st.Sysctls == nil {
		st.Sysctls = map[string]string{}
	}
	if _, known := st.Sysctls[key]; !known && old != "" {
		st.Sysctls[key] = old
	}
	return nil
}

var reRouteDev = regexp.MustCompile(`\bdev (\S+)`)
var reRouteSrc = regexp.MustCompile(`\bsrc ([0-9.]+)`)
var reIfaceName = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,15}$`)

// defaultRoute returns the interface and source address of the route to the internet.
func (h *ProxyHost) defaultRoute(ctx context.Context) (string, string, error) {
	out, err := h.run(ctx, "", "ip", "-4", "route", "get", "1.1.1.1")
	if err != nil {
		return "", "", errors.New("this machine has no IPv4 route to the internet")
	}
	dev, src := reRouteDev.FindStringSubmatch(out), reRouteSrc.FindStringSubmatch(out)
	if dev == nil || src == nil || !reIfaceName.MatchString(dev[1]) || net.ParseIP(src[1]) == nil {
		return "", "", errors.New("could not read the route to the internet")
	}
	return dev[1], src[1], nil
}

// Check looks at what the machine offers and changes nothing, except a throwaway interface that
// tells whether the kernel can do WireGuard.
func (h *ProxyHost) Check(ctx context.Context, asProxy bool) []ProxyCheck {
	var checks []ProxyCheck
	add := func(id string, ok, fatal bool, detail string) {
		checks = append(checks, ProxyCheck{ID: id, OK: ok, Fatal: fatal && !ok, Detail: detail})
	}
	const probe = "wg-lgsm-probe"
	_, _ = h.run(ctx, "", "ip", "link", "del", probe)
	out, err := h.run(ctx, "", "ip", "link", "add", probe, "type", "wireguard")
	_, _ = h.run(ctx, "", "ip", "link", "del", probe)
	if err != nil {
		add("wireguard", false, true, "The kernel has no WireGuard: "+strings.TrimSpace(out)+". On a container host, load the module there (modprobe wireguard).")
	} else {
		add("wireguard", true, true, "")
	}
	_, _, routeErr := h.defaultRoute(ctx)
	if routeErr != nil {
		add("route", false, true, routeErr.Error())
	} else {
		add("route", true, true, "")
	}
	// The tunnel network must not collide with a network the machine already has.
	if out, err := h.run(ctx, "", "ip", "-4", "route", "show", "to", "match", proxyProxyIP); err == nil {
		clash := ""
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "default") && !strings.Contains(line, proxyInterface) {
				clash = line
			}
		}
		add("subnet", clash == "", true, map[bool]string{true: "", false: "The tunnel network " + proxySubnet + " is already in use here: " + clash}[clash == ""])
	}
	// ufw, Docker and plain iptables are handled by syncFirewallLocked. firewalld has its own table,
	// and an accept elsewhere does not override its reject.
	if out, err := h.run(ctx, "", "firewall-cmd", "--state"); err == nil && strings.Contains(out, "running") {
		detail := "firewalld is running and blocks the tunnel. Run: firewall-cmd --permanent --zone=trusted --add-interface=" + proxyInterface
		if asProxy {
			detail += ", then allow the WireGuard port and run firewall-cmd --reload."
		} else {
			detail += " and firewall-cmd --reload."
		}
		add("firewall", false, false, detail)
	} else {
		add("firewall", true, false, "")
	}
	return checks
}

// installPackages installs what is missing and remembers it. Called by Prepare, so only when an
// administrator sets up a proxy.
func (h *ProxyHost) installPackages(ctx context.Context, st *proxyState) error {
	id, _ := osRelease("/etc/os-release")
	manager := ""
	switch id {
	case "debian", "ubuntu", "raspbian":
		manager = "apt"
	case "rhel", "centos", "almalinux", "rocky", "fedora":
		manager = "dnf"
	}
	need := map[string]string{"wireguard-tools": "wg", "nftables": "nft"}
	var missing []string
	for _, pkg := range proxyPackages {
		if _, err := h.run(ctx, "", "sh", "-c", "command -v "+need[pkg]); err != nil {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if manager == "" {
		return fmt.Errorf("please install %s on this machine, this distribution is not handled automatically", strings.Join(missing, " and "))
	}
	if manager == "apt" {
		_, _ = h.run(ctx, "", "apt-get", "update", "-qq")
		if out, err := h.run(ctx, "", "apt-get", append([]string{"install", "-y", "-qq", "--no-install-recommends"}, missing...)...); err != nil {
			return fmt.Errorf("installing %s failed: %s", strings.Join(missing, ", "), lastLines(out, 3))
		}
	} else if out, err := h.run(ctx, "", "dnf", append([]string{"install", "-y", "-q"}, missing...)...); err != nil {
		return fmt.Errorf("installing %s failed: %s", strings.Join(missing, ", "), lastLines(out, 3))
	}
	for _, pkg := range missing {
		st.Installed = appendUniqueString(st.Installed, pkg)
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " ")
}

// Prepare installs the packages, creates the key and checks the machine. It loads no rule, so a
// failed setup leaves no tunnel behind.
func (h *ProxyHost) Prepare(ctx context.Context, asProxy bool) (ProxyPrepared, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.loadState()
	err := h.installPackages(ctx, &st)
	if saveErr := h.saveState(st); err == nil {
		err = saveErr
	}
	if err != nil {
		return ProxyPrepared{}, err
	}
	_, public, err := h.privateKey()
	if err != nil {
		return ProxyPrepared{}, err
	}
	return ProxyPrepared{PublicKey: public, Checks: h.Check(ctx, asProxy)}, nil
}

// Apply stores and loads a plan. An empty plan removes everything.
func (h *ProxyHost) Apply(ctx context.Context, plan ProxyPlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if plan.Empty() {
		return h.teardownLocked(ctx)
	}
	if s := plan.Node; s != nil && h.NodePorts != nil {
		allowed := h.NodePorts()
		for _, f := range s.Forwards {
			if !allowed[f.Protocol+"/"+strconv.Itoa(f.Port)] {
				return fmt.Errorf("%s port %d does not belong to a game server on this machine", f.Protocol, f.Port)
			}
		}
	}
	if s := plan.Node; s != nil && h.NodeUsers != nil && len(s.EgressUsers) > 0 {
		allowed := h.NodeUsers()
		for _, name := range s.EgressUsers {
			if !allowed[name] {
				return fmt.Errorf("user %s runs no game server on this machine", name)
			}
		}
	}
	if s := plan.Proxy; s != nil {
		if err := h.checkPortsFree(ctx, s); err != nil {
			return err
		}
	}
	if err := h.loadLocked(ctx, plan); err != nil {
		return err
	}
	h.syncFirewallLocked(ctx, plan)
	h.syncDisplayIPLocked(plan)
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(h.path(proxyPlanFile), data, 0600)
}

// checkPortsFree refuses ports a service of the proxy machine listens on: their traffic would go
// to the node instead.
func (h *ProxyHost) checkPortsFree(ctx context.Context, s *ProxySide) error {
	out, err := h.run(ctx, "", "ss", "-H", "-lntu")
	if err != nil {
		return nil // no ss: the check is a courtesy, not a guarantee
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		local := fields[4]
		portAt := strings.LastIndexByte(local, ':')
		if portAt < 0 {
			continue
		}
		port, err := strconv.Atoi(local[portAt+1:])
		if err != nil {
			continue
		}
		for _, peer := range s.Peers {
			for _, f := range peer.Forwards {
				if f.Port == port && f.Protocol == fields[0] {
					return fmt.Errorf("%s port %d is already used by a service on the proxy", f.Protocol, f.Port)
				}
			}
		}
	}
	return nil
}

// loadLocked brings the machine to the plan. Unchanged state is left alone, so a running tunnel is
// not touched.
func (h *ProxyHost) loadLocked(ctx context.Context, plan ProxyPlan) error {
	privateKey, _, err := h.privateKey()
	if err != nil {
		return err
	}
	iface, primary, err := h.defaultRoute(ctx)
	if err != nil {
		return err
	}
	var rules string
	if plan.Proxy != nil {
		rules = renderProxyRules(plan.Proxy, iface)
	} else {
		target := plan.Node.TargetIP
		if target == "" {
			target = primary
		}
		uids, err := h.egressUIDs(plan.Node.EgressUsers)
		if err != nil {
			return err
		}
		rules = renderNodeRules(plan.Node, target, uids)
	}
	config := renderWGConfig(plan, privateKey)
	wanted := rules + config
	if wanted == h.lastLoad && h.loadedLocked(ctx, plan) {
		return nil
	}

	// The interface first: nft rules name it, and a tunnel without rules has no way in.
	if _, err := h.run(ctx, "", "ip", "link", "show", "dev", proxyInterface); err != nil {
		if out, err := h.run(ctx, "", "ip", "link", "add", proxyInterface, "type", "wireguard"); err != nil {
			return fmt.Errorf("creating the tunnel failed: %s", lastLines(out, 2))
		}
	}
	if out, err := h.run(ctx, config, "wg", "syncconf", proxyInterface, "/dev/stdin"); err != nil {
		return fmt.Errorf("configuring the tunnel failed: %s", lastLines(out, 2))
	}
	address := proxyProxyIP
	if plan.Node != nil {
		address = plan.Node.TunnelIP
	}
	if out, err := h.run(ctx, "", "ip", "addr", "replace", address+"/24", "dev", proxyInterface); err != nil {
		return fmt.Errorf("setting the tunnel address failed: %s", lastLines(out, 2))
	}
	if out, err := h.run(ctx, "", "ip", "link", "set", proxyInterface, "mtu", strconv.Itoa(proxyTunnelMTU), "up"); err != nil {
		return fmt.Errorf("starting the tunnel failed: %s", lastLines(out, 2))
	}

	// Drop the table of the other role in case this machine changed its role.
	other := proxyTableNode
	if plan.Node != nil {
		other = proxyTableProxy
	}
	_, _ = h.run(ctx, "", "nft", "delete", "table", "inet", other)
	if out, err := h.run(ctx, rules, "nft", "-f", "-"); err != nil {
		return fmt.Errorf("loading the firewall rules failed: %s", lastLines(out, 3))
	}

	st := h.loadState()
	var sysctlErr error
	if plan.Node != nil {
		// Answers arrive from a player whose route points to the home line: strict reverse path
		// filtering would drop them.
		sysctlErr = h.setSysctl(ctx, &st, "net.ipv4.conf."+proxyInterface+".rp_filter", "2")
		if sysctlErr == nil {
			if err := h.ensureNodeRoute(ctx); err != nil {
				sysctlErr = err
			}
		}
	} else {
		h.removeNodeRoute(ctx)
		// Forwarding per interface, so the machine does not become a router in general.
		sysctlErr = h.setSysctl(ctx, &st, "net.ipv4.conf."+iface+".forwarding", "1")
		if sysctlErr == nil {
			sysctlErr = h.setSysctl(ctx, &st, "net.ipv4.conf."+proxyInterface+".forwarding", "1")
		}
	}
	if err := h.saveState(st); err != nil && sysctlErr == nil {
		sysctlErr = err
	}
	if sysctlErr != nil {
		return sysctlErr
	}
	h.lastLoad, h.lastErr = wanted, ""
	return nil
}

// proxyFirewallRules lists what iptables has to let through for this plan. A packet has to pass every
// firewall of the machine, and an accept in our nft table does not override a drop of ufw, Docker
// or a cloud image's iptables rules. These rules accept the whole tunnel because our nft table
// decides what may use it: it drops every port that is not forwarded.
func proxyFirewallRules(plan ProxyPlan) []string {
	if plan.Proxy != nil {
		port := strconv.Itoa(plan.Proxy.ListenPort)
		return []string{
			"iptables INPUT -p udp --dport " + port + " -j ACCEPT",
			"ip6tables INPUT -p udp --dport " + port + " -j ACCEPT",
			"iptables FORWARD -i " + proxyInterface + " -j ACCEPT",
			"iptables FORWARD -o " + proxyInterface + " -j ACCEPT",
		}
	}
	return []string{"iptables INPUT -i " + proxyInterface + " -j ACCEPT"}
}

func (h *ProxyHost) runFirewallRule(ctx context.Context, action, rule string) (string, error) {
	fields := strings.Fields(rule)
	args := append([]string{"-w", "5", action, fields[1]}, fields[2:]...)
	return h.run(ctx, "", fields[0], args...)
}

// syncFirewallLocked puts the rules at the top of their chains and removes rules of an old plan. It
// also runs on every round, because ufw or Docker may arrive after the setup. A failure is reported
// in the status and does not stop the tunnel: without another firewall nothing is missing.
func (h *ProxyHost) syncFirewallLocked(ctx context.Context, plan ProxyPlan) {
	st := h.loadState()
	wanted := proxyFirewallRules(plan)
	kept := st.Firewall[:0:0]
	changed := false
	for _, rule := range st.Firewall {
		if containsString(wanted, rule) {
			kept = append(kept, rule)
		} else {
			_, _ = h.runFirewallRule(ctx, "-D", rule)
			changed = true
		}
	}
	st.Firewall = kept
	problem := ""
	for _, rule := range wanted {
		tool := strings.Fields(rule)[0]
		if _, err := h.run(ctx, "", "sh", "-c", "command -v "+tool); err != nil {
			continue // no such firewall here
		}
		if _, err := h.runFirewallRule(ctx, "-C", rule); err == nil {
			continue
		}
		if out, err := h.runFirewallRule(ctx, "-I", rule); err != nil {
			// Without IPv6 in the kernel ip6tables fails, and nothing is missing.
			if tool == "iptables" {
				problem = "opening the firewall failed: " + lastLines(out, 2)
			}
			continue
		}
		st.Firewall = appendUniqueString(st.Firewall, rule)
		changed = true
	}
	if changed {
		if err := h.saveState(st); err != nil && problem == "" {
			problem = err.Error()
		}
	}
	if problem != "" && problem != h.fwErr {
		fmt.Printf("[PROXY] %s\n", problem)
	}
	h.fwErr = problem
}

// syncDisplayIPLocked also runs on every round, for servers that are added later. A failure is reported
// in the status: it only changes what alerts show.
func (h *ProxyHost) syncDisplayIPLocked(plan ProxyPlan) {
	if h.SyncDisplayIP == nil {
		return
	}
	st := h.loadState()
	ports := map[int]bool{}
	host := ""
	if s := plan.Node; s != nil {
		if parsed, _, err := net.SplitHostPort(s.Endpoint); err == nil {
			host = parsed
		}
		for _, f := range s.Forwards {
			ports[f.Port] = true
		}
	}
	if host == "" && st.DisplayIP == "" {
		return
	}
	err := h.SyncDisplayIP(ports, st.DisplayIP, host)
	if err == nil && st.DisplayIP != host {
		st.DisplayIP = host
		err = h.saveState(st)
	}
	if err != nil && h.fwErr == "" {
		h.fwErr = "updating displayip failed: " + err.Error()
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ensureNodeRoute sends marked connections, and only those, through the tunnel.
func (h *ProxyHost) ensureNodeRoute(ctx context.Context) error {
	h.removeNodeRoute(ctx)
	if out, err := h.run(ctx, "", "ip", "route", "replace", "default", "dev", proxyInterface, "table", proxyRouteTable); err != nil {
		return fmt.Errorf("setting the tunnel route failed: %s", lastLines(out, 2))
	}
	if out, err := h.run(ctx, "", "ip", "rule", "add", "fwmark", proxyFlowMark, "lookup", proxyRouteTable, "priority", proxyRulePrio); err != nil {
		return fmt.Errorf("setting the routing rule failed: %s", lastLines(out, 2))
	}
	return nil
}

func (h *ProxyHost) removeNodeRoute(ctx context.Context) {
	_, _ = h.run(ctx, "", "ip", "rule", "del", "priority", proxyRulePrio)
	_, _ = h.run(ctx, "", "ip", "route", "flush", "table", proxyRouteTable)
}

func (h *ProxyHost) loadedLocked(ctx context.Context, plan ProxyPlan) bool {
	if _, err := h.run(ctx, "", "ip", "link", "show", "dev", proxyInterface); err != nil {
		return false
	}
	table := proxyTableProxy
	if plan.Node != nil {
		table = proxyTableNode
	}
	_, err := h.run(ctx, "", "nft", "list", "table", "inet", table)
	return err == nil
}

// Reconcile keeps the machine at its plan: after a boot, a lost rule or a new address. It works
// without the dashboard. Without a plan it does nothing, not even run a command.
func (h *ProxyHost) Reconcile(ctx context.Context) {
	if !h.HasPlan() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	plan, err := h.loadPlan()
	if err != nil {
		h.lastErr = err.Error()
		return
	}
	if plan.Empty() {
		return
	}
	if err := h.loadLocked(ctx, plan); err != nil {
		if err.Error() != h.lastErr {
			fmt.Printf("[PROXY] %v\n", err)
		}
		h.lastErr = err.Error()
		return
	}
	h.syncFirewallLocked(ctx, plan)
	h.syncDisplayIPLocked(plan)
}

// Teardown removes everything of the proxy from this machine.
func (h *ProxyHost) Teardown(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.teardownLocked(ctx)
}

func (h *ProxyHost) teardownLocked(ctx context.Context) error {
	st := h.loadState()
	_, planErr := os.Stat(h.path(proxyPlanFile))
	_, keyErr := os.Stat(h.path(proxyKeyFile))
	if planErr != nil && keyErr != nil && len(st.Installed) == 0 && len(st.Sysctls) == 0 && len(st.Firewall) == 0 {
		return nil // never set up: run nothing
	}
	h.removeNodeRoute(ctx)
	for _, rule := range st.Firewall {
		_, _ = h.runFirewallRule(ctx, "-D", rule)
	}
	if h.SyncDisplayIP != nil && st.DisplayIP != "" {
		if err := h.SyncDisplayIP(nil, st.DisplayIP, ""); err != nil {
			fmt.Printf("[PROXY] updating displayip failed: %v\n", err)
		}
	}
	_, _ = h.run(ctx, "", "nft", "delete", "table", "inet", proxyTableProxy)
	_, _ = h.run(ctx, "", "nft", "delete", "table", "inet", proxyTableNode)
	_, _ = h.run(ctx, "", "ip", "link", "del", proxyInterface)
	for key, value := range st.Sysctls {
		// Values of the tunnel interface went with it.
		if !strings.Contains(key, "."+proxyInterface+".") {
			_, _ = h.run(ctx, "", "sysctl", "-q", "-w", key+"="+value)
		}
	}
	if len(st.Installed) > 0 {
		id, _ := osRelease("/etc/os-release")
		if id == "debian" || id == "ubuntu" || id == "raspbian" {
			_, _ = h.run(ctx, "", "apt-get", append([]string{"remove", "-y", "-qq"}, st.Installed...)...)
		} else {
			_, _ = h.run(ctx, "", "dnf", append([]string{"remove", "-y", "-q"}, st.Installed...)...)
		}
	}
	h.lastLoad, h.lastErr, h.fwErr = "", "", ""
	var firstErr error
	for _, name := range []string{proxyPlanFile, proxyKeyFile, proxyStateFile} {
		if err := os.Remove(h.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Status reads the state of this machine.
func (h *ProxyHost) Status(ctx context.Context) ProxyHostStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := ProxyHostStatus{Error: h.lastErr}
	if st.Error == "" {
		st.Error = h.fwErr
	}
	plan, err := h.loadPlan()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if plan.Empty() {
		return st
	}
	st.Active = true
	table := proxyTableProxy
	st.Role = "proxy"
	if plan.Node != nil {
		table = proxyTableNode
		st.Role = "node"
	}
	if _, err := h.run(ctx, "", "ip", "link", "show", "dev", proxyInterface); err == nil {
		st.InterfaceUp = true
	}
	if _, err := h.run(ctx, "", "nft", "list", "table", "inet", table); err == nil {
		st.RulesLoaded = true
	}
	if out, err := h.run(ctx, "", "wg", "show", proxyInterface, "dump"); err == nil {
		parseWGDump(out, &st)
	}
	if st.LastHandshake > 0 {
		st.HandshakeAge = time.Now().Unix() - st.LastHandshake
	}
	return st
}

// parseWGDump reads `wg show <if> dump`. The first line is the interface and holds its private key,
// which is never kept; peer lines: key, psk, endpoint, ips, handshake, rx, tx, keepalive.
func parseWGDump(out string, st *ProxyHostStatus) {
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\t")
		if i == 0 || len(fields) < 7 {
			continue
		}
		if hs, err := strconv.ParseInt(fields[4], 10, 64); err == nil && hs > st.LastHandshake {
			st.LastHandshake = hs
		}
		if rx, err := strconv.ParseInt(fields[5], 10, 64); err == nil {
			st.RxBytes += rx
		}
		if tx, err := strconv.ParseInt(fields[6], 10, 64); err == nil {
			st.TxBytes += tx
		}
	}
}
