package backend

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestPortTestFollowsTheQueryProtocol(t *testing.T) {
	udp := func(d string) PortProbe { return PortProbe{Protocol: "UDP", Description: d} }
	cases := []struct {
		name      string
		port      PortProbe
		querytype string
		hasQuery  bool
		want      string
	}{
		{"tcp is always testable", PortProbe{Protocol: "TCP", Description: "RCON"}, "", false, portTestTCP},
		{"valve query port", udp("Query"), "protocol-valve", true, portTestA2S},
		{"valve game port with query port", udp("Game"), "protocol-valve", true, ""},
		{"valve game port is the query port", udp("Game"), "protocol-valve", false, portTestA2S},
		{"source tv", udp("SourceTV"), "protocol-valve", true, ""},
		{"quake3", udp("Query"), "protocol-quake3", true, ""},
		{"teamspeak voice", udp("Voice"), "teamspeak3", false, ""},
		{"bedrock", udp("Game"), "mbe", false, portTestMCB},
		{"no querytype", udp("Game"), "", false, ""},
	}
	for _, c := range cases {
		if got := portTestFor(c.port, c.querytype, c.hasQuery); got != c.want {
			t.Errorf("%s: test = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestProbePortStates(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	if got := probePort(portTestTCP, "127.0.0.1", nil, port); got != portStatusOpen {
		t.Errorf("public answer: %s", got)
	}
	// No hairpin NAT, or a closed forwarding: the public address does not answer, the machine does.
	if got := probePort(portTestTCP, "127.0.0.2", []string{"127.0.0.1"}, port); got != portStatusLocal {
		t.Errorf("local answer: %s", got)
	}
	if got := probePort(portTestTCP, "", []string{"127.0.0.1"}, port); got != portStatusLocal {
		t.Errorf("unknown public IP: %s", got)
	}
	l.Close()
	if got := probePort(portTestTCP, "127.0.0.1", []string{"127.0.0.1"}, port); got != portStatusClosed {
		t.Errorf("no answer: %s", got)
	}
	if got := probePort("", "127.0.0.1", nil, port); got != portStatusUntestable {
		t.Errorf("no test: %s", got)
	}
}

func TestUniquePortsKeepsProtocolsAndDescriptions(t *testing.T) {
	got := uniquePorts([]PortProbe{
		{Port: 27015, Protocol: "UDP", Description: "Game"},
		{Port: 27015, Protocol: "udp", Description: "Query"},
		{Port: 27015, Protocol: "TCP", Description: "RCON"},
		{Port: 27015, Protocol: "UDP", Description: "Game"},
	})
	if len(got) != 2 || got[0].Description != "Game, Query" || got[1].Protocol != "TCP" {
		t.Fatalf("got %+v", got)
	}
}

func TestPortsFromConfigInventNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "lgsm", "config-lgsm")
	mustWrite(t, filepath.Join(cfg, "vhserver", "_default.cfg"), "port=\"2456\"\n")
	probes := parseServerPortsFromConfig(dir, cfg, "vhserver", "vh")
	if len(probes) != 1 || probes[0].Port != 2456 {
		t.Fatalf("probes = %+v", probes)
	}

	mustWrite(t, filepath.Join(cfg, "pmcserver", "_default.cfg"), "port=\"25565\"\n")
	probes = parseServerPortsFromConfig(dir, cfg, "pmcserver", "pmc")
	if len(probes) != 1 || probes[0].Protocol != "TCP" {
		t.Fatalf("PaperMC port = %+v", probes)
	}
}

func TestCheckServerPortsReport(t *testing.T) {
	publicIPCache.Lock()
	publicIPCache.ip, publicIPCache.fetched = "127.0.0.2", time.Now()
	publicIPCache.Unlock()
	t.Cleanup(func() {
		publicIPCache.Lock()
		publicIPCache.ip = ""
		publicIPCache.Unlock()
	})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	tcpPort := l.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "lgsm", "config-lgsm", "qlserver", "_default.cfg"), "querytype=\"protocol-quake3\"\n")
	srv := GameServerInstance{ID: "ql", Script: "qlserver", Dir: dir, ParsedPorts: []PortProbe{
		{Port: 27960, Protocol: "UDP", Description: "Game"},
		{Port: tcpPort, Protocol: "TCP", Description: "RCON"},
	}}
	im := NewStaticInstanceManager(t.TempDir(), srv)

	rec := httptest.NewRecorder()
	im.CheckServerPorts(rec, httptest.NewRequest("GET", "/", nil), "ql")
	var out struct {
		PublicIP string      `json:"public_ip"`
		Probes   []PortProbe `json:"probes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	if out.PublicIP != "127.0.0.2" || len(out.Probes) != 2 {
		t.Fatalf("report = %+v", out)
	}
	if out.Probes[0].Status != portStatusUntestable || out.Probes[0].Open {
		t.Errorf("quake3 UDP port: %+v", out.Probes[0])
	}
	if out.Probes[1].Status != portStatusLocal || out.Probes[1].Port != tcpPort {
		t.Errorf("RCON port: %+v", out.Probes[1])
	}
	if stored, _ := im.getServer("ql"); stored.ParsedPorts[0].Status != "" {
		t.Error("the check changed the stored ports")
	}
}
