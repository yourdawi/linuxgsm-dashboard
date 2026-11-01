package backend

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func cstr(s string) []byte { return append([]byte(s), 0) }

// fakeA2SServer answers like a Source server that requires challenges for INFO and PLAYER and
// splits the player response into two packets.
func fakeA2SServer(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	challenge := []byte{0x11, 0x22, 0x33, 0x44}
	header := []byte{0xFF, 0xFF, 0xFF, 0xFF}

	info := append([]byte{}, header...)
	info = append(info, 'I', 17)
	info = append(info, cstr("My <b>Server</b>\x07")...)
	info = append(info, cstr("de_dust2")...)
	info = append(info, cstr("csgo")...)
	info = append(info, cstr("Counter-Strike")...)
	info = append(info, 0xDA, 0x02) // app id
	info = append(info, 2, 16, 1)   // players, max, bots
	info = append(info, 'd', 'l', 0, 1)

	players := append([]byte{}, header...)
	players = append(players, 'D', 3)
	addPlayer := func(name string, score int32, dur float32) {
		players = append(players, 0)
		players = append(players, cstr(name)...)
		players = binary.LittleEndian.AppendUint32(players, uint32(score))
		players = binary.LittleEndian.AppendUint32(players, math.Float32bits(dur))
	}
	addPlayer("alice", 10, 125)
	addPlayer("", 0, 3) // connecting player without name
	addPlayer("bob", 25, 3725)

	split := func(payload []byte) [][]byte {
		mid := len(payload) / 2
		var out [][]byte
		for i, part := range [][]byte{payload[:mid], payload[mid:]} {
			p := []byte{0xFE, 0xFF, 0xFF, 0xFF, 1, 0, 0, 0, 2, byte(i), 0xE0, 0x04}
			out = append(out, append(p, part...))
		}
		return out
	}

	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			switch {
			case n >= 5 && req[4] == 'T':
				if !bytes.HasSuffix(req, challenge) {
					conn.WriteTo(append(append([]byte{}, header...), append([]byte{0x41}, challenge...)...), addr)
					continue
				}
				conn.WriteTo(info, addr)
			case n >= 9 && req[4] == 'U':
				if !bytes.Equal(req[5:9], challenge) {
					conn.WriteTo(append(append([]byte{}, header...), append([]byte{0x41}, challenge...)...), addr)
					continue
				}
				for _, p := range split(players) {
					conn.WriteTo(p, addr)
				}
			}
		}
	}()
	return conn.LocalAddr().String(), func() { conn.Close() }
}

func TestQueryA2S(t *testing.T) {
	addr, stop := fakeA2SServer(t)
	defer stop()

	info, err := QueryA2S(addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Online || info.Map != "de_dust2" || info.NumPlayers != 2 || info.MaxPlayers != 16 || info.Bots != 1 {
		t.Fatalf("unexpected info %+v", info)
	}
	if strings.ContainsRune(info.Name, 0x07) {
		t.Fatal("control characters must be stripped from the server name")
	}
	if len(info.Players) != 2 || info.Players[0].Name != "bob" || info.Players[0].Time != "1h 02m" || info.Players[1].Time != "2m 05s" {
		t.Fatalf("unexpected players %+v", info.Players)
	}
}

func TestQueryA2SNoServer(t *testing.T) {
	conn, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := conn.LocalAddr().String()
	conn.Close()
	if _, err := QueryA2S(addr, 300*time.Millisecond); err == nil {
		t.Fatal("expected error without server")
	}
}

func TestParseA2SRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{{}, {'I'}, {'I', 17, 'a'}, {'X', 1, 2, 3}, {'D', 5, 0, 'a'}} {
		if _, err := parseA2SInfo(b); err == nil && len(b) > 0 && b[0] != 'D' {
			t.Errorf("garbage accepted: %v", b)
		}
	}
}

func fakeMinecraftServer(t *testing.T, status string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := &limitedByteReader{r: c, left: 4096}
				// handshake
				l, _ := readVarInt(br)
				io.CopyN(io.Discard, br, int64(l))
				// status request
				l, _ = readVarInt(br)
				io.CopyN(io.Discard, br, int64(l))
				var body []byte
				body = appendVarInt(body, 0)
				body = appendVarInt(body, int32(len(status)))
				body = append(body, status...)
				c.Write(append(appendVarInt(nil, int32(len(body))), body...))
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestQueryMinecraft(t *testing.T) {
	status := `{"version":{"name":"1.21"},"players":{"max":20,"online":2,"sample":[{"name":"Steve","id":"x"},{"name":"Alex","id":"y"}]},"description":{"text":"§aHello ","extra":[{"text":"World"}]}}`
	addr, stop := fakeMinecraftServer(t, status)
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	info, err := QueryMinecraft(addr, host, port, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if info.NumPlayers != 2 || info.MaxPlayers != 20 || info.Name != "Hello World" || len(info.Players) != 2 {
		t.Fatalf("unexpected info %+v", info)
	}
}

func TestParseBedrockPong(t *testing.T) {
	motd := "MCPE;Bedrock Test;622;1.20.40;3;10;123456;Survival World;Survival;1;19132;19133;"
	b := []byte{0x1C}
	b = append(b, make([]byte, 16)...)
	b = append(b, raknetMagic...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(motd)))
	b = append(b, motd...)
	info, err := parseBedrockPong(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.NumPlayers != 3 || info.MaxPlayers != 10 || info.Map != "Survival World" || info.Name != "Bedrock Test" {
		t.Fatalf("unexpected info %+v", info)
	}
	if _, err := parseBedrockPong(b[:20]); err == nil {
		t.Fatal("truncated pong accepted")
	}
}

func TestDiskScanStoresResultsAndTimeout(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.bin"), "data")
	im := NewStaticInstanceManager(t.TempDir(), GameServerInstance{ID: "a", Dir: dir, UID: -1}, GameServerInstance{ID: "b", Dir: dir, UID: -1})
	im.scanDiskUsage(false)
	u := im.cachedDisk(dir)
	if u == nil || u.Scanning || u.Updated.IsZero() || !u.Shared {
		t.Fatalf("unexpected cache entry %+v", u)
	}

	// A walk that exceeds its deadline reports an error instead of staying in "measuring".
	for i := 0; i < 5000; i++ {
		mustWrite(t, filepath.Join(dir, "many", strconv.Itoa(i)), "")
	}
	r := MeasureServerDisk(GameServerInstance{Dir: dir, UID: -1}, nil, time.Now().Add(-time.Second))
	if r.Error == "" {
		t.Fatal("expired deadline not reported")
	}
}

func TestQueryTargets(t *testing.T) {
	srv := GameServerInstance{Game: "cs2", Port: 27015, ParsedPorts: []PortProbe{
		{Port: 27015, Description: "Game"}, {Port: 27020, Description: "Query"}, {Port: 27050, Description: "RCON"},
	}}
	targets := queryTargets(srv)
	if len(targets) == 0 || targets[0].Port != 27020 || targets[0].Protocol != QueryProtoA2S || targets[0].Host != "127.0.0.1" {
		t.Fatalf("unexpected targets %+v", targets)
	}
	mc := queryTargets(GameServerInstance{Game: "mc", Port: 25565})
	if len(mc) != 1 || mc[0].Protocol != QueryProtoMinecraft || mc[0].Port != 25565 {
		t.Fatalf("unexpected minecraft targets %+v", mc)
	}
	if bed := queryTargets(GameServerInstance{Game: "mcb"}); len(bed) != 1 || bed[0].Port != 19132 || bed[0].Protocol != QueryProtoBedrock {
		t.Fatalf("unexpected bedrock targets %+v", bed)
	}
	if ts := queryTargets(GameServerInstance{Game: "ts3", Port: 9987}); len(ts) != 0 {
		t.Fatal("ts3 must not be queried")
	}
	withIP := queryTargets(GameServerInstance{Game: "vh", Port: 2456, BindIP: "10.0.0.5"})
	found := false
	for _, tg := range withIP {
		if tg.Host == "10.0.0.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("bind IP not used as query host")
	}
}

func TestParseDetailsOutputPorts(t *testing.T) {
	out := "Server name:  Test\nDESCRIPTION  PORT   PROTOCOL  LISTEN\nGame: Master  10999  udp  1\nQuery (http)  8080  tcp  0\nVoice (unused)  9988  udp  0\nGame+2  27017  udp  1\n"
	name, ports := parseDetailsOutput(out)
	if name != "Test" {
		t.Errorf("name = %q", name)
	}
	want := []string{"Game: Master/10999/UDP", "Query (http)/8080/TCP", "Voice (unused)/9988/UDP", "Game+2/27017/UDP"}
	if len(ports) != len(want) {
		t.Fatalf("ports = %+v", ports)
	}
	for i, p := range ports {
		if got := fmt.Sprintf("%s/%d/%s", p.Description, p.Port, p.Protocol); got != want[i] {
			t.Errorf("port %d = %s, want %s", i, got, want[i])
		}
	}
}

func TestParseDetailsIP(t *testing.T) {
	out := "Server name:  Test\nServer IP:     10.1.2.3:27015\nInternet IP:   1.2.3.4:27015\n"
	if ip := parseDetailsIP(out); ip != "10.1.2.3" {
		t.Fatalf("got %q", ip)
	}
	if ip := parseDetailsIP("Server IP: not-an-ip"); ip != "" {
		t.Fatalf("got %q", ip)
	}
}

func TestQueryDataOnlyForRunningServers(t *testing.T) {
	im := NewStaticInstanceManager(t.TempDir(),
		GameServerInstance{ID: "a", Status: "running", Dir: "/x"},
		GameServerInstance{ID: "b", Status: "stopped", Dir: "/y"},
	)
	im.setQuery("a", queryState{info: QueryInfo{Online: true, NumPlayers: 4}})
	im.setQuery("b", queryState{info: QueryInfo{Online: true, NumPlayers: 9}})
	for _, s := range im.GetInstances() {
		if s.ID == "a" && s.QueryData.NumPlayers != 4 {
			t.Error("running server lost query data")
		}
		if s.ID == "b" && s.QueryData.Online {
			t.Error("stopped server must not report stale query data")
		}
	}
	data, _ := json.Marshal(QueryInfo{NumPlayers: 1})
	if !strings.Contains(string(data), `"num_players":1`) {
		t.Fatalf("unexpected json %s", data)
	}
}

func TestMeasureServerDisk(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "serverfiles", "big.bin"), strings.Repeat("a", 64*1024))
	mustWrite(t, filepath.Join(dir, "lgsm", "backup", "x-2024.tar.gz"), strings.Repeat("b", 32*1024))
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "huge.bin"), strings.Repeat("c", 512*1024))
	// a symlink pointing elsewhere (not serverfiles) must not be followed
	_ = os.Symlink(outside, filepath.Join(dir, "link-outside"))

	u := MeasureServerDisk(GameServerInstance{Dir: dir, UID: -1}, []string{filepath.Join(dir, "lgsm", "backup")}, time.Time{})
	if u.Error != "" || u.Updated.IsZero() || u.Files < 3 {
		t.Fatalf("unexpected measurement %+v", u)
	}
	if u.Used < 96*1024 {
		t.Fatalf("used too small: %d", u.Used)
	}
	if u.Used >= 512*1024 {
		t.Fatalf("symlinked directory was counted: %d", u.Used)
	}
	if u.Backups < 32*1024 || u.Backups > u.Used {
		t.Fatalf("unexpected backup size %d (used %d)", u.Backups, u.Used)
	}
}

func TestServerName(t *testing.T) {
	// LinuxGSM prints "NOT SET" when it cannot read the name from the game config.
	if name, _ := parseDetailsOutput("Server name:      NOT SET\nGame  27015  udp  0\n"); name != "" {
		t.Errorf("NOT SET taken as name: %q", name)
	}
	// Only the "Server name:" row counts, not text that merely contains it.
	if name, _ := parseDetailsOutput("Steam Server name: x\nServer name:\t\x1b[0mMy \"Best\" <Server>\n"); name != "My \"Best\" <Server>" {
		t.Errorf("name = %q", name)
	}

	rt := &fakeRuntime{}
	im := NewStaticInstanceManager(t.TempDir())
	im.static = false
	srv := GameServerInstance{ID: "cssserver", Script: "cssserver", User: "css", Dir: "/home/css", Status: "stopped", ParsedPorts: noDetails}
	im.rt = discoverRuntime{rt, []GameServerInstance{srv}}
	im.ScanInstances()
	im.mu.Lock()
	im.instances["cssserver"].Name = "From details" // no ports parsed
	im.mu.Unlock()
	im.ScanInstances()
	if s, _ := im.getServer("cssserver"); s.Name != "From details" {
		t.Errorf("name lost on rescan: %q", s.Name)
	}

	// A running server's query name is the current name.
	im.applyQueryName(srv, QueryInfo{Online: true, Name: "Renamed via RCON"})
	if s, _ := im.getServer("cssserver"); s.Name != "Renamed via RCON" {
		t.Errorf("query name not applied: %q", s.Name)
	}
	im.applyQueryName(srv, QueryInfo{Online: false, Name: "stale"})
	if s, _ := im.getServer("cssserver"); s.Name != "Renamed via RCON" {
		t.Errorf("offline query changed the name: %q", s.Name)
	}

	// details without a name falls back to the game name.
	rt.lgsmOut = "Server name:      NOT SET\n"
	im.executeRefreshDetails("cssserver", srv)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := im.getServer("cssserver"); s.Name == mapScriptToGameName("cssserver") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s, _ := im.getServer("cssserver")
	t.Errorf("name after details without a name = %q", s.Name)
}

// A split player response in every header layout, also with the parts in the wrong order.
func TestA2SSplitLayouts(t *testing.T) {
	marker := []byte{0xFE, 0xFF, 0xFF, 0xFF, 7, 0, 0, 0}
	body := append([]byte{0xFF, 0xFF, 0xFF, 0xFF}, []byte("Dthe player list")...)
	first, second := body[:9], body[9:]
	part := func(header []byte, payload []byte) []byte {
		return append(append(append([]byte(nil), marker...), header...), payload...)
	}
	cases := map[string][][]byte{
		"source":           {part([]byte{2, 0, 0xE0, 0x04}, first), part([]byte{2, 1, 0xE0, 0x04}, second)},
		"source old":       {part([]byte{2, 0}, first), part([]byte{2, 1}, second)},
		"goldsrc":          {part([]byte{0x02}, first), part([]byte{0x12}, second)},
		"goldsrc reversed": {part([]byte{0x12}, second), part([]byte{0x02}, first)},
		"source reversed":  {part([]byte{2, 1, 0xE0, 0x04}, second), part([]byte{2, 0, 0xE0, 0x04}, first)},
	}
	for name, packets := range cases {
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			go func() {
				buf := make([]byte, 64)
				if _, err := server.Read(buf); err != nil {
					return
				}
				for _, p := range packets {
					if _, err := server.Write(p); err != nil {
						return
					}
				}
			}()
			_ = client.SetDeadline(time.Now().Add(5 * time.Second))
			got, err := a2sExchange(client, []byte("req"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "Dthe player list" {
				t.Fatalf("payload = %q", got)
			}
		})
	}
}

func TestA2SSplitRefusesInvalidParts(t *testing.T) {
	marker := []byte{0xFE, 0xFF, 0xFF, 0xFF, 7, 0, 0, 0}
	for name, header := range map[string][]byte{
		"part number beyond total": {2, 2, 0, 0},
		"no parts":                 {0, 0, 0, 0},
		"too many parts":           {33, 0, 0, 0},
	} {
		p := append(append(append([]byte(nil), marker...), header...), 0xFF, 0xFF, 0xFF, 0xFF, 'D')
		if resp, err := joinA2SSplit([][]byte{p}, a2sSplitSource); err == nil {
			t.Errorf("%s: accepted: %q", name, resp)
		}
	}
	compressed := []byte{0xFE, 0xFF, 0xFF, 0xFF, 7, 0, 0, 0x80, 1, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		buf := make([]byte, 64)
		_, _ = server.Read(buf)
		_, _ = server.Write(compressed)
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := a2sExchange(client, []byte("req")); err == nil {
		t.Error("compressed response accepted")
	}
}
