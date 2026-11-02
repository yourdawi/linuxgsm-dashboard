package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testWGKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

func testProxyPlan() ProxyPlan {
	return ProxyPlan{Proxy: &ProxySide{ListenPort: 51820, Peers: []ProxyPeer{{
		PublicKey: testWGKey, TunnelIP: "10.77.0.2",
		Forwards: []ProxyForward{{27015, "udp"}, {27015, "tcp"}, {27020, "udp"}},
	}}}}
}

func testNodePlan() ProxyPlan {
	return ProxyPlan{Node: &NodeSide{
		TunnelIP: "10.77.0.2", ProxyPublicKey: testWGKey, Endpoint: "203.0.113.5:51820",
		Forwards: []ProxyForward{{27015, "udp"}, {27015, "tcp"}},
	}}
}

func TestProxyPlanValidation(t *testing.T) {
	if err := testProxyPlan().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := testNodePlan().Validate(); err != nil {
		t.Fatal(err)
	}
	both := testProxyPlan()
	both.Node = testNodePlan().Node
	mod := func(f func(*ProxyPlan)) ProxyPlan { p := testProxyPlan(); f(&p); return p }
	modNode := func(f func(*NodeSide)) ProxyPlan { p := testNodePlan(); f(p.Node); return p }
	for name, plan := range map[string]ProxyPlan{
		"both roles":      both,
		"no peers":        mod(func(p *ProxyPlan) { p.Proxy.Peers = nil }),
		"bad key":         mod(func(p *ProxyPlan) { p.Proxy.Peers[0].PublicKey = "x\" accept" }),
		"tunnel of proxy": mod(func(p *ProxyPlan) { p.Proxy.Peers[0].TunnelIP = proxyProxyIP }),
		"foreign address": mod(func(p *ProxyPlan) { p.Proxy.Peers[0].TunnelIP = "192.168.0.1" }),
		"ssh":             mod(func(p *ProxyPlan) { p.Proxy.Peers[0].Forwards = []ProxyForward{{22, "tcp"}} }),
		"low port":        mod(func(p *ProxyPlan) { p.Proxy.Peers[0].Forwards = []ProxyForward{{999, "udp"}} }),
		"listen port":     mod(func(p *ProxyPlan) { p.Proxy.Peers[0].Forwards = []ProxyForward{{51820, "udp"}} }),
		"bad protocol":    mod(func(p *ProxyPlan) { p.Proxy.Peers[0].Forwards = []ProxyForward{{27015, "udp; drop"}} }),
		"twice":           mod(func(p *ProxyPlan) { p.Proxy.Peers[0].Forwards = []ProxyForward{{27015, "udp"}, {27015, "udp"}} }),
		"no ports":        modNode(func(s *NodeSide) { s.Forwards = nil }),
		"bad endpoint":    modNode(func(s *NodeSide) { s.Endpoint = "a b:51820" }),
		"endpoint inject": modNode(func(s *NodeSide) { s.Endpoint = "host\nPostUp = reboot:51820" }),
		"bad target":      modNode(func(s *NodeSide) { s.TargetIP = "x" }),
	} {
		if err := plan.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ValidateProxyEndpoint("[2a02:8071::1]:51820"); err != nil {
		t.Errorf("IPv6 endpoint: %v", err)
	}
	if err := ValidateProxyEndpoint("vps.example.com:51820"); err != nil {
		t.Errorf("hostname: %v", err)
	}
}

func TestProxyRules(t *testing.T) {
	proxy := renderProxyRules(testProxyPlan().Proxy, "eth0")
	for _, want := range []string{
		"table inet lgsm_proxy {}\ndelete table inet lgsm_proxy\n", // atomic replace
		`iifname "eth0" udp dport { 27015, 27020 } dnat ip to 10.77.0.2`,
		`iifname "eth0" tcp dport { 27015 } dnat ip to 10.77.0.2`,
		`iifname "eth0" oifname "wg-lgsm" ip daddr 10.77.0.2 udp dport { 27015, 27020 } accept`,
		`iifname "wg-lgsm" drop`,
		`oifname "wg-lgsm" drop`,
		"maxseg size set rt mtu",
	} {
		if !strings.Contains(proxy, want) {
			t.Errorf("proxy rules lack %q:\n%s", want, proxy)
		}
	}
	// The player's address must stay, and foreign forwarding must not be touched.
	for _, bad := range []string{"masquerade", "snat", "policy drop"} {
		if strings.Contains(proxy, bad) {
			t.Errorf("proxy rules contain %q:\n%s", bad, proxy)
		}
	}

	node := renderNodeRules(testNodePlan().Node, "192.168.0.177", nil)
	for _, want := range []string{
		`iifname "wg-lgsm" udp dport { 27015 } dnat ip to 192.168.0.177`, // answers of 0.0.0.0 sockets
		`iifname "wg-lgsm" ct state new ct mark set 0x77`,
		"ct mark 0x77 meta mark set 0x77",
		`iifname "wg-lgsm" drop`,
	} {
		if !strings.Contains(node, want) {
			t.Errorf("node rules lack %q:\n%s", want, node)
		}
	}
	if strings.Contains(node, "masquerade") {
		t.Errorf("node rules hide the player:\n%s", node)
	}
}

func TestProxyEgressRules(t *testing.T) {
	plan := testNodePlan()
	node := renderNodeRules(plan.Node, "192.168.0.177", []int{1000, 1001})
	for _, want := range []string{
		"meta mark 0x78 return", // the outer packets of WireGuard keep the owner of the inner ones
		"ct state new meta skuid { 1000, 1001 } ip daddr != { 0.0.0.0/8, 10.0.0.0/8",
		"ct mark set 0x77",
		`oifname "wg-lgsm" ip saddr != 10.77.0.2 snat ip to 10.77.0.2`,
		"meta nfproto ipv6 meta skuid { 1000, 1001 }",
		`oifname "wg-lgsm" tcp flags syn tcp option maxseg size set rt mtu`,
		`iifname "wg-lgsm" udp dport { 27015 } dnat ip to 192.168.0.177`, // incoming stays the same
	} {
		if !strings.Contains(node, want) {
			t.Errorf("node rules lack %q:\n%s", want, node)
		}
	}
	// Mark first, so that a connection of these users is routed by its own new mark.
	if strings.Index(node, "meta skuid { 1000, 1001 } ip daddr") > strings.Index(node, "ct mark 0x77 meta mark set") {
		t.Errorf("the mark comes too late:\n%s", node)
	}
	plain := renderNodeRules(plan.Node, "192.168.0.177", nil)
	for _, bad := range []string{"skuid", "snat", "ipv6"} {
		if strings.Contains(plain, bad) {
			t.Errorf("rules without outgoing traffic contain %q:\n%s", bad, plain)
		}
	}

	side := testProxyPlan().Proxy
	if rules := renderProxyRules(side, "eth0"); strings.Contains(rules, "masquerade") {
		t.Errorf("a node without outgoing traffic got an exit:\n%s", rules)
	}
	side.Peers[0].Egress = true
	proxy := renderProxyRules(side, "eth0")
	for _, want := range []string{
		`oifname "eth0" ip saddr 10.77.0.2 masquerade`,
		`iifname "wg-lgsm" oifname "eth0" ip saddr 10.77.0.2 ip daddr { 0.0.0.0/8, 10.0.0.0/8`, // no way into private networks
		`iifname "wg-lgsm" oifname "eth0" ip saddr 10.77.0.2 ct state new limit rate over`,
	} {
		if !strings.Contains(proxy, want) {
			t.Errorf("proxy rules lack %q:\n%s", want, proxy)
		}
	}
	if strings.Index(proxy, "ip daddr { 0.0.0.0/8") > strings.Index(proxy, "ip saddr 10.77.0.2 accept") {
		t.Errorf("private networks are checked too late:\n%s", proxy)
	}
}

func TestProxyRulesProtectTheProxy(t *testing.T) {
	proxy := renderProxyRules(testProxyPlan().Proxy, "eth0")
	for _, want := range []string{
		"chain c_in",
		`iifname "eth0" oifname "wg-lgsm" ct state new add @s_flows { ip saddr limit rate over`,
		"flags dynamic,timeout",
	} {
		if !strings.Contains(proxy, want) {
			t.Errorf("proxy rules lack %q:\n%s", want, proxy)
		}
	}
	// Otherwise a flood passes before the limit.
	if strings.Index(proxy, "add @s_flows") > strings.Index(proxy, `ip daddr 10.77.0.2 udp dport`) {
		t.Errorf("the limit comes too late:\n%s", proxy)
	}
}

func TestProxyEgressUsersOnTheNode(t *testing.T) {
	h, f := newTestProxyHost(t)
	ctx := context.Background()
	h.lookupUID = func(name string) (int, error) {
		switch name {
		case "game":
			return 1000, nil
		case "toor":
			return 0, nil
		}
		return 0, errors.New("unknown user")
	}
	h.NodeUsers = func() map[string]bool { return map[string]bool{"game": true, "toor": true, "gone": true} }
	plan := testNodePlan()
	plan.Node.EgressUsers = []string{"game"}
	if err := h.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdin["nft -f -"], "skuid { 1000 }") {
		t.Errorf("rules: %s", f.stdin["nft -f -"])
	}
	for _, c := range []struct{ user, want string }{
		{"other", "no game server"}, // the dashboard cannot route a service of this machine
		{"gone", "unknown user"},
		{"toor", "is root"}, // another name for root would catch the tunnel and the agent
		{"root", "invalid user"},
		{"bad;name", "invalid user"},
	} {
		plan.Node.EgressUsers = []string{c.user}
		if err := h.Apply(ctx, plan); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.user, err, c.want)
		}
	}
}

func TestProxyPresharedKey(t *testing.T) {
	c, _ := newTestProxyController(t)
	ctx := context.Background()
	link, err := c.Setup(ctx, testSetup("lxc", 27015))
	if err != nil {
		t.Fatal(err)
	}
	if link.PresharedKey != "" {
		t.Error("the answer to the browser holds the key")
	}
	stored := c.Links()[0]
	if !validProxyKey(stored.PresharedKey) {
		t.Fatalf("key: %q", stored.PresharedKey)
	}
	if planFor(c.Links(), "lxc").Node.PresharedKey != stored.PresharedKey || planFor(c.Links(), "vps").Proxy.Peers[0].PresharedKey != stored.PresharedKey {
		t.Error("both ends need the same key")
	}
	for _, v := range c.View(ctx) {
		if v.PresharedKey != "" {
			t.Error("the view holds the key")
		}
	}
	conf := renderWGConfig(planFor(c.Links(), "lxc"), "PRIV")
	if !strings.Contains(conf, "PresharedKey = "+stored.PresharedKey) || !strings.Contains(conf, "FwMark = 0x78") {
		t.Errorf("config: %s", conf)
	}
}

func TestProxySetEgress(t *testing.T) {
	c, _ := newTestProxyController(t)
	ctx := context.Background()
	if _, err := c.Setup(ctx, testSetup("lxc", 27015)); err != nil {
		t.Fatal(err)
	}
	if planFor(c.Links(), "vps").Proxy.Peers[0].Egress {
		t.Error("outgoing traffic is on by default")
	}
	if err := c.SetEgress(ctx, "lxc", []string{"game"}); err != nil {
		t.Fatal(err)
	}
	if !planFor(c.Links(), "vps").Proxy.Peers[0].Egress || len(planFor(c.Links(), "lxc").Node.EgressUsers) != 1 {
		t.Errorf("plans: %+v", c.Links())
	}
	if err := c.SetEgress(ctx, "lxc", []string{"root"}); err == nil {
		t.Error("root accepted")
	}
	if err := c.SetEgress(ctx, "unknown", nil); err == nil {
		t.Error("unknown node accepted")
	}
}

// A setup can take minutes; the server list asks for addresses all the time.
func TestProxyAddressesDoNotWaitForASetup(t *testing.T) {
	c, _ := newTestProxyController(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		c.ServerAddress("lxc", 27015)
		c.Links()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked by a running setup")
	}
}

func TestProxyPublishedHost(t *testing.T) {
	h := NewProxyHost(t.TempDir())
	if host := h.PublishedHost(27015, "UDP"); host != "" {
		t.Errorf("without a plan: %q", host)
	}
	data, err := json.Marshal(testNodePlan())
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, h.path(proxyPlanFile), string(data))
	if host := h.PublishedHost(27015, "UDP"); host != "203.0.113.5" {
		t.Errorf("published port: %q", host)
	}
	if host := h.PublishedHost(27016, "UDP"); host != "" {
		t.Errorf("other port: %q", host)
	}
}

func TestProxyServerAddress(t *testing.T) {
	c, _ := newTestProxyController(t)
	if _, err := c.Setup(context.Background(), testSetup("lxc", 27015)); err != nil {
		t.Fatal(err)
	}
	if got, has := c.ServerAddress("lxc", 27015); got != "203.0.113.5:27015" || !has {
		t.Errorf("published: %q %v", got, has)
	}
	if got, has := c.ServerAddress("lxc", 27016); got != "" || !has {
		t.Errorf("not published: %q %v", got, has)
	}
	if got, has := c.ServerAddress("lxc2", 27015); got != "" || has {
		t.Errorf("no proxy: %q %v", got, has)
	}
}

func TestProxyDisplayIP(t *testing.T) {
	dir := t.TempDir()
	cfg := func(script string) string {
		return filepath.Join(dir, script, "lgsm", "config-lgsm", script, script+".cfg")
	}
	mustWrite(t, cfg("one"), "port=\"27015\"\n")
	mustWrite(t, cfg("two"), "port=\"27016\"\n")
	mustWrite(t, cfg("own"), "displayip=\"play.example.com\"\n")
	im := NewStaticInstanceManager(t.TempDir(),
		GameServerInstance{ID: "one", User: "game", Script: "one", Dir: filepath.Join(dir, "one"), Port: 27015},
		GameServerInstance{ID: "two", User: "game", Script: "two", Dir: filepath.Join(dir, "two"), Port: 27016},
		GameServerInstance{ID: "own", User: "game", Script: "own", Dir: filepath.Join(dir, "own"), Port: 27017},
	)
	read := func(script string) string {
		data, _ := os.ReadFile(cfg(script))
		return string(data)
	}
	if err := im.ProxySyncDisplayIP(map[int]bool{27015: true, 27017: true}, "", "203.0.113.5"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read("one"), `displayip="203.0.113.5"`) {
		t.Errorf("published server: %s", read("one"))
	}
	if strings.Contains(read("two"), "displayip") {
		t.Errorf("a server that is not published changed: %s", read("two"))
	}
	if !strings.Contains(read("own"), "play.example.com") || strings.Contains(read("own"), "203.0.113.5") {
		t.Errorf("the administrator's value changed: %s", read("own"))
	}
	// The proxy moves to another address.
	if err := im.ProxySyncDisplayIP(map[int]bool{27015: true}, "203.0.113.5", "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read("one"), "198.51.100.7") || strings.Contains(read("one"), "203.0.113.5") {
		t.Errorf("moved: %s", read("one"))
	}
	// The proxy is removed: only its own value goes.
	if err := im.ProxySyncDisplayIP(nil, "198.51.100.7", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read("one"), "displayip") || !strings.Contains(read("one"), "port=") {
		t.Errorf("removed: %s", read("one"))
	}
	if !strings.Contains(read("own"), "play.example.com") {
		t.Errorf("the administrator's value went: %s", read("own"))
	}
}

func TestProxyHostKeepsDisplayIP(t *testing.T) {
	h, _ := newTestProxyHost(t)
	ctx := context.Background()
	type call struct {
		ports         map[int]bool
		oldHost, host string
	}
	var calls []call
	h.SyncDisplayIP = func(ports map[int]bool, oldHost, newHost string) error {
		calls = append(calls, call{ports, oldHost, newHost})
		return nil
	}
	if err := h.Apply(ctx, testNodePlan()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].host != "203.0.113.5" || !calls[0].ports[27015] {
		t.Fatalf("calls: %+v", calls)
	}
	h.Reconcile(ctx)
	if last := calls[len(calls)-1]; last.oldHost != "203.0.113.5" || last.host != "203.0.113.5" {
		t.Errorf("reconcile: %+v", last)
	}
	if err := h.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
	if last := calls[len(calls)-1]; last.oldHost != "203.0.113.5" || last.host != "" || len(last.ports) != 0 {
		t.Errorf("teardown: %+v", last)
	}
}

func TestProxyWGConfig(t *testing.T) {
	conf := renderWGConfig(testNodePlan(), "PRIV")
	for _, want := range []string{"PrivateKey = PRIV", "Endpoint = 203.0.113.5:51820", "AllowedIPs = 0.0.0.0/0", "PersistentKeepalive = 25", "ListenPort = 51821"} {
		if !strings.Contains(conf, want) {
			t.Errorf("node config lacks %q:\n%s", want, conf)
		}
	}
	conf = renderWGConfig(testProxyPlan(), "PRIV")
	for _, want := range []string{"ListenPort = 51820", "AllowedIPs = 10.77.0.2/32"} {
		if !strings.Contains(conf, want) {
			t.Errorf("proxy config lacks %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "0.0.0.0/0") {
		t.Error("a node may only send from its own tunnel address at the proxy")
	}
}

// fakeRunner records commands and answers like a machine with WireGuard.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	stdin map[string]string
	fail  map[string]string // command prefix -> output of a failure
	out   map[string]string // command prefix -> output
}

func (f *fakeRunner) run(_ context.Context, stdin string, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, cmd)
	if f.stdin == nil {
		f.stdin = map[string]string{}
	}
	f.stdin[cmd] = stdin
	for prefix, out := range f.fail {
		if strings.HasPrefix(cmd, prefix) {
			return out, errors.New("failed")
		}
	}
	for prefix, out := range f.out {
		if strings.HasPrefix(cmd, prefix) {
			return out, nil
		}
	}
	if strings.HasPrefix(cmd, "ip -4 route get") {
		return "1.1.1.1 via 192.168.0.1 dev eth0 src 192.168.0.177 uid 0\n", nil
	}
	return "", nil
}

func (f *fakeRunner) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newTestProxyHost(t *testing.T) (*ProxyHost, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{}
	h := NewProxyHost(t.TempDir())
	h.run = f.run
	return h, f
}

// The feature is optional: a machine that was never set up runs nothing and has no files.
func TestProxyDoesNothingWithoutASetup(t *testing.T) {
	h, f := newTestProxyHost(t)
	h.Reconcile(context.Background())
	if err := h.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := h.Status(context.Background()); st.Active {
		t.Errorf("status = %+v", st)
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %q", f.calls)
	}
	entries, _ := os.ReadDir(h.dir)
	if len(entries) != 0 {
		t.Errorf("files appeared: %v", entries)
	}

	c, err := NewProxyController(t.TempDir(), func(string) (ProxyEndpoint, error) {
		t.Error("a machine was contacted")
		return nil, errors.New("no")
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Sync(context.Background())
	if v := c.View(context.Background()); len(v) != 0 {
		t.Errorf("view = %+v", v)
	}
}

func TestProxyKeyStaysOnTheMachine(t *testing.T) {
	h, _ := newTestProxyHost(t)
	priv, pub, err := h.privateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !validProxyKey(priv) || !validProxyKey(pub) || priv == pub {
		t.Fatalf("keys: %q %q", priv, pub)
	}
	st, err := os.Stat(filepath.Join(h.dir, proxyKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 && os.PathSeparator == '/' {
		t.Errorf("key mode %o", st.Mode().Perm())
	}
	if _, again, _ := h.privateKey(); again != pub {
		t.Error("the key changed")
	}
	prep, err := h.Prepare(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if prep.PublicKey != pub || strings.Contains(fmt.Sprintf("%+v", prep), priv) {
		t.Errorf("prepare = %+v", prep)
	}
}

func TestProxyApplyNode(t *testing.T) {
	h, f := newTestProxyHost(t)
	if err := h.Apply(context.Background(), testNodePlan()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdin["nft -f -"], "dnat ip to 192.168.0.177") {
		t.Errorf("rules: %s", f.stdin["nft -f -"])
	}
	conf := f.stdin["wg syncconf wg-lgsm /dev/stdin"]
	if !strings.Contains(conf, "PrivateKey") {
		t.Errorf("config: %s", conf)
	}
	for _, c := range f.calls {
		if strings.Contains(c, conf[len("[Interface]\nPrivateKey = "):len("[Interface]\nPrivateKey = ")+20]) {
			t.Errorf("the private key is on a command line: %s", c)
		}
	}
	for _, want := range []string{
		"ip link set wg-lgsm mtu 1380 up",
		"ip addr replace 10.77.0.2/24 dev wg-lgsm",
		"sysctl -q -w net.ipv4.conf.wg-lgsm.rp_filter=2",
		"ip route replace default dev wg-lgsm table 7701",
		"ip rule add fwmark 0x77 lookup 7701 priority 7701",
	} {
		if !f.called(want) {
			t.Errorf("not run: %s\n%q", want, f.calls)
		}
	}
	if !h.HasPlan() {
		t.Error("plan not stored")
	}
}

func TestProxyApplyProxy(t *testing.T) {
	h, f := newTestProxyHost(t)
	if err := h.Apply(context.Background(), testProxyPlan()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdin["nft -f -"], `iifname "eth0"`) {
		t.Errorf("rules: %s", f.stdin["nft -f -"])
	}
	for _, want := range []string{"sysctl -q -w net.ipv4.conf.eth0.forwarding=1", "sysctl -q -w net.ipv4.conf.wg-lgsm.forwarding=1"} {
		if !f.called(want) {
			t.Errorf("not run: %s", want)
		}
	}
	// Not the machine as a whole.
	if f.called("sysctl -q -w net.ipv4.ip_forward") {
		t.Error("global forwarding switched on")
	}
}

func TestProxyRefusesWhatItMustNot(t *testing.T) {
	h, f := newTestProxyHost(t)
	h.NodePorts = func() map[string]bool { return map[string]bool{"udp/27015": true} }
	if err := h.Apply(context.Background(), testNodePlan()); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Errorf("port of no game server: %v", err)
	}
	if f.called("nft") || h.HasPlan() {
		t.Error("loaded anyway")
	}

	h, f = newTestProxyHost(t)
	f.out = map[string]string{"ss -H -lntu": "udp UNCONN 0 0 0.0.0.0:27020 0.0.0.0:*\n"}
	if err := h.Apply(context.Background(), testProxyPlan()); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("used port: %v", err)
	}
	if f.called("nft -f") || h.HasPlan() {
		t.Error("loaded anyway")
	}

	// A failed load stores no plan, so nothing retries it after a reboot.
	h, f = newTestProxyHost(t)
	f.fail = map[string]string{"nft -f -": "syntax error"}
	if err := h.Apply(context.Background(), testNodePlan()); err == nil {
		t.Error("failed load accepted")
	}
	if h.HasPlan() {
		t.Error("plan stored after a failed load")
	}
}

func TestProxyReconcile(t *testing.T) {
	h, f := newTestProxyHost(t)
	ctx := context.Background()
	if err := h.Apply(ctx, testNodePlan()); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	h.Reconcile(ctx)
	if f.called("nft -f") || f.called("wg syncconf") {
		t.Errorf("reloaded without a change: %q", f.calls)
	}

	// A reboot: interface gone.
	f.fail = map[string]string{"ip link show": "does not exist"}
	f.calls = nil
	h.Reconcile(ctx)
	if !f.called("ip link add wg-lgsm type wireguard") || !f.called("nft -f") {
		t.Errorf("not restored: %q", f.calls)
	}
	f.fail = nil

	// The node got a new address: the DNAT target has to follow.
	f.out = map[string]string{"ip -4 route get": "1.1.1.1 via 192.168.0.1 dev eth0 src 192.168.0.200\n"}
	f.calls = nil
	h.Reconcile(ctx)
	if !strings.Contains(f.stdin["nft -f -"], "dnat ip to 192.168.0.200") {
		t.Errorf("rules: %s", f.stdin["nft -f -"])
	}
}

// The dashboard sends the plan every minute. Loading it again would disturb a running tunnel.
func TestProxyApplyLeavesAnUnchangedPlanAlone(t *testing.T) {
	h, f := newTestProxyHost(t)
	ctx := context.Background()
	plan := testNodePlan()
	if err := h.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := h.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if f.called("nft -f") || f.called("wg syncconf") {
		t.Errorf("reloaded without a change: %q", f.calls)
	}

	plan.Node.Endpoint = "203.0.113.6:51820"
	if err := h.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if !f.called("wg syncconf") {
		t.Errorf("a changed plan was not loaded: %q", f.calls)
	}
}

// ufw, Docker and cloud images drop what our nft table accepts, so the tunnel needs its own rules.
func TestProxyOpensOtherFirewalls(t *testing.T) {
	h, f := newTestProxyHost(t)
	ctx := context.Background()
	f.fail = map[string]string{"iptables -w 5 -C": "Bad rule", "ip6tables -w 5 -C": "Bad rule"}
	if err := h.Apply(ctx, testProxyPlan()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"iptables -w 5 -I INPUT -p udp --dport 51820 -j ACCEPT",
		"ip6tables -w 5 -I INPUT -p udp --dport 51820 -j ACCEPT",
		"iptables -w 5 -I FORWARD -i wg-lgsm -j ACCEPT",
		"iptables -w 5 -I FORWARD -o wg-lgsm -j ACCEPT",
	} {
		if !f.called(want) {
			t.Errorf("missing %q in %q", want, f.calls)
		}
	}
	// ufw or Docker arrive later and the rule is gone: the next round puts it back.
	f.calls = nil
	h.Reconcile(ctx)
	if !f.called("iptables -w 5 -I FORWARD -i wg-lgsm") {
		t.Errorf("not restored: %q", f.calls)
	}
	f.calls, f.fail = nil, nil
	if err := h.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.called("iptables -w 5 -D FORWARD -i wg-lgsm -j ACCEPT") || !f.called("ip6tables -w 5 -D INPUT") {
		t.Errorf("rules stayed: %q", f.calls)
	}
}

func TestProxyLeavesRulesOfTheAdministrator(t *testing.T) {
	h, f := newTestProxyHost(t)
	ctx := context.Background()
	if err := h.Apply(ctx, testProxyPlan()); err != nil { // every check finds its rule
		t.Fatal(err)
	}
	if f.called("iptables -w 5 -I") {
		t.Errorf("added a rule that exists: %q", f.calls)
	}
	f.calls = nil
	if err := h.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
	if f.called("iptables -w 5 -D") {
		t.Errorf("removed a rule of the administrator: %q", f.calls)
	}
}

func TestProxyFirewallProblems(t *testing.T) {
	h, f := newTestProxyHost(t)
	ctx := context.Background()
	f.fail = map[string]string{"sh -c command -v iptables": "", "sh -c command -v ip6tables": ""}
	if err := h.Apply(ctx, testProxyPlan()); err != nil {
		t.Fatal(err)
	}
	if f.called("iptables -w") || f.called("ip6tables -w") {
		t.Errorf("no iptables here, still used: %q", f.calls)
	}
	if st := h.Status(ctx); st.Error != "" {
		t.Errorf("error without a reason: %q", st.Error)
	}

	// A rule that cannot be set shows in the status and does not stop the proxy.
	f.fail = map[string]string{"iptables -w 5 -C": "", "iptables -w 5 -I": "no chain"}
	if err := h.Apply(ctx, testProxyPlan()); err != nil {
		t.Fatal(err)
	}
	if st := h.Status(ctx); !strings.Contains(st.Error, "no chain") {
		t.Errorf("status: %+v", st)
	}
}

func TestProxyLinkHealth(t *testing.T) {
	up := func(age int64) *ProxyHostStatus {
		return &ProxyHostStatus{InterfaceUp: true, RulesLoaded: true, LastHandshake: 1000, HandshakeAge: age}
	}
	for _, c := range []struct {
		name        string
		proxy, node *ProxyHostStatus
		want        string
	}{
		{"both fresh", up(30), up(40), ProxyHealthConnected},
		{"offline", nil, up(30), ProxyHealthUnknown},
		{"old handshake", up(30), up(proxyHandshakeMaxAge + 1), ProxyHealthWaiting},
		{"never", &ProxyHostStatus{InterfaceUp: true, RulesLoaded: true}, up(30), ProxyHealthWaiting},
		{"rules gone", &ProxyHostStatus{InterfaceUp: true, LastHandshake: 1000}, up(30), ProxyHealthBroken},
	} {
		if got := proxyLinkHealth(c.proxy, c.node); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestProxyTeardownLeavesNothing(t *testing.T) {
	h, f := newTestProxyHost(t)
	ctx := context.Background()
	f.fail = map[string]string{"sh -c command -v wg": "", "sh -c command -v nft": ""}
	st := proxyState{Installed: []string{"wireguard-tools"}, Sysctls: map[string]string{
		"net.ipv4.conf.eth0.forwarding": "0", "net.ipv4.conf.wg-lgsm.forwarding": "0",
	}}
	if err := h.saveState(st); err != nil {
		t.Fatal(err)
	}
	f.fail = nil
	if err := h.Apply(ctx, testProxyPlan()); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := h.Apply(ctx, ProxyPlan{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ip link del wg-lgsm", "nft delete table inet lgsm_proxy", "nft delete table inet lgsm_node",
		"ip rule del priority 7701", "sysctl -q -w net.ipv4.conf.eth0.forwarding=0",
	} {
		if !f.called(want) {
			t.Errorf("not run: %s\n%q", want, f.calls)
		}
	}
	if f.called("sysctl -q -w net.ipv4.conf.wg-lgsm") {
		t.Error("restored a value of the removed interface")
	}
	entries, _ := os.ReadDir(h.dir)
	if len(entries) != 0 {
		t.Errorf("files left: %v", entries)
	}
}

func TestProxyChecks(t *testing.T) {
	h, f := newTestProxyHost(t)
	f.fail = map[string]string{"ip link add wg-lgsm-probe": "Unknown device type"}
	f.out = map[string]string{
		"firewall-cmd --state":                "running\n",
		"ip -4 route show to match 10.77.0.1": "10.77.0.0/16 dev tun0\ndefault via 1.2.3.4 dev eth0\n",
	}
	got := map[string]ProxyCheck{}
	for _, c := range h.Check(context.Background(), true) {
		got[c.ID] = c
	}
	if c := got["wireguard"]; c.OK || !c.Fatal || !strings.Contains(c.Detail, "modprobe") {
		t.Errorf("wireguard: %+v", c)
	}
	if c := got["subnet"]; c.OK || !c.Fatal || !strings.Contains(c.Detail, "tun0") {
		t.Errorf("subnet: %+v", c)
	}
	if c := got["firewall"]; c.OK || c.Fatal || !strings.Contains(c.Detail, "firewall-cmd") {
		t.Errorf("firewall: %+v", c)
	}
	if !f.called("ip link del wg-lgsm-probe") {
		t.Error("probe not removed")
	}
}

func TestParseWGDump(t *testing.T) {
	var st ProxyHostStatus
	parseWGDump("PRIVATE\tPUB\t51820\toff\nPEER1\t(none)\t1.2.3.4:5\t10.77.0.2/32\t1700000000\t100\t200\t25\nPEER2\t(none)\t(none)\t10.77.0.3/32\t0\t1\t2\t25\n", &st)
	if st.LastHandshake != 1700000000 || st.RxBytes != 101 || st.TxBytes != 202 {
		t.Errorf("%+v", st)
	}
	if strings.Contains(fmt.Sprintf("%+v", st), "PRIVATE") {
		t.Error("private key kept")
	}
}

// fakeEndpoint is a machine for the controller.
type fakeEndpoint struct {
	key      string
	checks   []ProxyCheck
	prepErr  error
	applyErr error
	plans    []ProxyPlan
	prepared int
}

func (e *fakeEndpoint) Prepare(context.Context, bool) (ProxyPrepared, error) {
	e.prepared++
	return ProxyPrepared{PublicKey: e.key, Checks: e.checks}, e.prepErr
}
func (e *fakeEndpoint) Apply(_ context.Context, plan ProxyPlan) error {
	if e.applyErr != nil {
		return e.applyErr
	}
	e.plans = append(e.plans, plan)
	return nil
}
func (e *fakeEndpoint) Status(context.Context) (ProxyHostStatus, error) {
	return ProxyHostStatus{Active: true}, nil
}
func (e *fakeEndpoint) last() ProxyPlan { return e.plans[len(e.plans)-1] }

func wgKeyOf(b byte) string {
	raw := make([]byte, 32)
	raw[0] = b
	return base64.StdEncoding.EncodeToString(raw)
}

func newTestProxyController(t *testing.T) (*ProxyController, map[string]*fakeEndpoint) {
	t.Helper()
	eps := map[string]*fakeEndpoint{
		"vps":  {key: wgKeyOf(1)},
		"lxc":  {key: wgKeyOf(2)},
		"lxc2": {key: wgKeyOf(3)},
	}
	c, err := NewProxyController(t.TempDir(), func(id string) (ProxyEndpoint, error) {
		ep, ok := eps[id]
		if !ok {
			return nil, errors.New("offline")
		}
		return ep, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, eps
}

func testSetup(node string, ports ...int) ProxySetup {
	var f []ProxyForward
	for _, p := range ports {
		f = append(f, ProxyForward{p, "udp"})
	}
	return ProxySetup{Proxy: "vps", Node: node, PublicHost: "203.0.113.5", Forwards: f}
}

func TestProxySetup(t *testing.T) {
	c, eps := newTestProxyController(t)
	ctx := context.Background()
	link, err := c.Setup(ctx, testSetup("lxc", 27015))
	if err != nil {
		t.Fatal(err)
	}
	if link.TunnelIP != "10.77.0.2" || link.ListenPort != proxyDefaultPort {
		t.Errorf("link = %+v", link)
	}
	if eps["vps"].prepared != 1 || eps["lxc"].prepared != 1 {
		t.Error("a machine was not prepared")
	}
	node := eps["lxc"].last().Node
	if node == nil || node.ProxyPublicKey != wgKeyOf(1) || node.Endpoint != "203.0.113.5:51820" {
		t.Errorf("node plan = %+v", node)
	}
	proxy := eps["vps"].last().Proxy
	if proxy == nil || len(proxy.Peers) != 1 || proxy.Peers[0].PublicKey != wgKeyOf(2) {
		t.Errorf("proxy plan = %+v", proxy)
	}

	// A second node: another address, the first stays on the proxy.
	if link, err = c.Setup(ctx, testSetup("lxc2", 27016)); err != nil || link.TunnelIP != "10.77.0.3" {
		t.Fatalf("second: %+v %v", link, err)
	}
	if n := len(eps["vps"].last().Proxy.Peers); n != 2 {
		t.Errorf("peers = %d", n)
	}
	if err := eps["vps"].last().Validate(); err != nil {
		t.Errorf("plan: %v", err)
	}
	// Stored across a restart.
	again, err := NewProxyController(filepath.Dir(c.path), c.endpoint)
	if err != nil || len(again.Links()) != 2 {
		t.Fatalf("reload: %d %v", len(again.Links()), err)
	}
}

func TestProxySetupRefusals(t *testing.T) {
	c, eps := newTestProxyController(t)
	ctx := context.Background()
	if _, err := c.Setup(ctx, ProxySetup{Proxy: "vps", Node: "vps", PublicHost: "1.2.3.4", Forwards: []ProxyForward{{27015, "udp"}}}); err == nil {
		t.Error("a machine as its own proxy")
	}
	if _, err := c.Setup(ctx, ProxySetup{Proxy: "vps", Node: "lxc", PublicHost: "bad host", Forwards: []ProxyForward{{27015, "udp"}}}); err == nil {
		t.Error("bad address")
	}
	if _, err := c.Setup(ctx, testSetup("lxc", 22)); err == nil {
		t.Error("reserved port")
	}
	if eps["vps"].prepared != 0 {
		t.Error("packages were installed for an invalid request")
	}
	if _, err := c.Setup(ctx, testSetup("offline", 27015)); err == nil {
		t.Error("offline machine")
	}
	if _, err := c.Setup(ctx, testSetup("lxc", 27015)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Setup(ctx, testSetup("lxc", 27016)); err == nil {
		t.Error("two proxies for one node")
	}
	if _, err := c.Setup(ctx, testSetup("lxc2", 27015)); err == nil || !strings.Contains(err.Error(), "already forwarded") {
		t.Errorf("port taken: %v", err)
	}
	if _, err := c.Setup(ctx, ProxySetup{Proxy: "lxc", Node: "lxc2", PublicHost: "1.2.3.4", Forwards: []ProxyForward{{27030, "udp"}}}); err == nil {
		t.Error("a node as a proxy")
	}
	if _, err := c.Setup(ctx, ProxySetup{Proxy: "lxc2", Node: "vps", PublicHost: "1.2.3.4", Forwards: []ProxyForward{{27030, "udp"}}}); err == nil {
		t.Error("a proxy as a node")
	}
}

func TestProxySetupStopsOnChecksAndRollsBack(t *testing.T) {
	c, eps := newTestProxyController(t)
	ctx := context.Background()
	eps["lxc"].checks = []ProxyCheck{{ID: "wireguard", Fatal: true, Detail: "no kernel module"}}
	_, err := c.Setup(ctx, testSetup("lxc", 27015))
	var setupErr *ProxySetupError
	if !errors.As(err, &setupErr) || setupErr.Machine != "node" {
		t.Fatalf("err = %v", err)
	}
	if len(c.Links()) != 0 {
		t.Error("link stored despite the check")
	}
	// Prepare installed packages on both machines; an empty plan takes them back.
	for _, id := range []string{"vps", "lxc"} {
		if len(eps[id].plans) == 0 || !eps[id].last().Empty() {
			t.Errorf("%s not cleaned up after the refusal: %+v", id, eps[id].plans)
		}
	}

	// The node refuses after the proxy has loaded: the proxy goes back.
	eps["lxc"].checks = nil
	eps["lxc"].applyErr = errors.New("nft failed")
	if _, err := c.Setup(ctx, testSetup("lxc", 27015)); err == nil {
		t.Fatal("accepted")
	}
	if !eps["vps"].last().Empty() {
		t.Errorf("proxy keeps a half link: %+v", eps["vps"].last())
	}
	if len(c.Links()) != 0 {
		t.Error("link stored")
	}
	if _, err := os.Stat(c.path); err == nil {
		t.Error("config written although nothing was set up")
	}
}

func TestProxyRemoveAndOfflineCleanup(t *testing.T) {
	c, eps := newTestProxyController(t)
	ctx := context.Background()
	if _, err := c.Setup(ctx, testSetup("lxc", 27015)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Setup(ctx, testSetup("lxc2", 27016)); err != nil {
		t.Fatal(err)
	}
	// The proxy keeps its other node; the removed one tears down.
	if err := c.Remove(ctx, "lxc"); err != nil {
		t.Fatal(err)
	}
	if !eps["lxc"].last().Empty() {
		t.Errorf("node plan: %+v", eps["lxc"].last())
	}
	if p := eps["vps"].last().Proxy; p == nil || len(p.Peers) != 1 || p.Peers[0].TunnelIP != "10.77.0.3" {
		t.Errorf("proxy plan: %+v", p)
	}

	// The last node goes while it is offline: it tears down when it comes back.
	offline := eps["lxc2"]
	delete(eps, "lxc2")
	if err := c.Remove(ctx, "lxc2"); err != nil {
		t.Fatal(err)
	}
	if !eps["vps"].last().Empty() {
		t.Errorf("proxy plan: %+v", eps["vps"].last())
	}
	n := len(offline.plans)
	c.Sync(ctx)
	if len(offline.plans) != n {
		t.Error("offline machine contacted")
	}
	eps["lxc2"] = offline
	c.Sync(ctx)
	if !offline.last().Empty() {
		t.Errorf("no cleanup after coming back: %+v", offline.last())
	}
	// Done: no machine is contacted any more.
	calls := len(eps["vps"].plans)
	c.Sync(ctx)
	if len(eps["vps"].plans) != calls {
		t.Error("still contacting machines")
	}
}

func TestProxySetForwards(t *testing.T) {
	c, eps := newTestProxyController(t)
	ctx := context.Background()
	if err := c.SetForwards(ctx, "lxc", []ProxyForward{{27015, "udp"}}); err == nil {
		t.Error("no link")
	}
	if _, err := c.Setup(ctx, testSetup("lxc", 27015)); err != nil {
		t.Fatal(err)
	}
	if err := c.SetForwards(ctx, "lxc", []ProxyForward{{22, "tcp"}}); err == nil {
		t.Error("reserved port")
	}
	if err := c.SetForwards(ctx, "lxc", []ProxyForward{{27015, "udp"}, {27020, "udp"}}); err != nil {
		t.Fatal(err)
	}
	if n := len(eps["vps"].last().Proxy.Peers[0].Forwards); n != 2 {
		t.Errorf("forwards = %d", n)
	}
	if n := len(eps["lxc"].last().Node.Forwards); n != 2 {
		t.Errorf("node forwards = %d", n)
	}
}
