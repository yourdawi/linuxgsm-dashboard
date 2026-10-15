package backend

// Live server queries (map, players) without external dependencies.
//
// Supported protocols:
//   - Valve A2S (Source/GoldSrc and most Steam based games) incl. challenge handshake and
//     split responses
//   - Minecraft Java Edition "Server List Ping"
//   - Minecraft Bedrock Edition (RakNet unconnected ping)

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Query protocol identifiers.
const (
	QueryProtoA2S       = "a2s"
	QueryProtoMinecraft = "minecraft"
	QueryProtoBedrock   = "bedrock"
)

const (
	queryTimeout     = 2 * time.Second
	queryInterval    = 15 * time.Second
	queryConcurrency = 8
	maxQueryPlayers  = 256
)

var errQueryFormat = errors.New("unexpected query response")

// -------------------------------------------------------------
// Protocol selection
// -------------------------------------------------------------

// queryUnsupportedGames are games without a query protocol implemented here.
var queryUnsupportedGames = map[string]bool{"ts3": true, "mumble": true, "sbots": true}

var minecraftJavaGames = map[string]bool{"mc": true, "pmc": true, "vpmc": true, "wmc": true}

// queryTarget is one address/protocol combination to try.
type queryTarget struct {
	Host     string
	Port     int
	Protocol string
}

// queryTargets returns the candidates for a server, most likely first.
func queryTargets(srv GameServerInstance) []queryTarget {
	game := srv.Game
	if queryUnsupportedGames[game] {
		return nil
	}

	hosts := []string{"127.0.0.1"}
	if srv.BindIP != "" && srv.BindIP != "0.0.0.0" && srv.BindIP != "127.0.0.1" && net.ParseIP(srv.BindIP) != nil {
		hosts = append(hosts, srv.BindIP)
	}

	gamePort := srv.Port
	var queryPorts []int
	for _, p := range srv.ParsedPorts {
		d := strings.ToLower(p.Description)
		if strings.Contains(d, "game") && gamePort == 0 {
			gamePort = p.Port
		}
		if strings.Contains(d, "query") && !strings.Contains(d, "rcon") {
			queryPorts = append(queryPorts, p.Port)
		}
	}

	var ports []int
	add := func(p int) {
		if p <= 0 || p > 65535 {
			return
		}
		for _, existing := range ports {
			if existing == p {
				return
			}
		}
		ports = append(ports, p)
	}

	proto := QueryProtoA2S
	switch {
	case minecraftJavaGames[game]:
		proto = QueryProtoMinecraft
		add(gamePort)
		if gamePort == 0 {
			add(25565)
		}
	case game == "mcb":
		proto = QueryProtoBedrock
		add(gamePort)
		if gamePort == 0 {
			add(19132)
		}
	default:
		for _, p := range queryPorts {
			add(p)
		}
		add(gamePort)
		if gamePort > 0 {
			add(gamePort + 1)
		}
	}

	var targets []queryTarget
	for _, h := range hosts {
		for _, p := range ports {
			targets = append(targets, queryTarget{Host: h, Port: p, Protocol: proto})
		}
	}
	if len(targets) > 4 {
		targets = targets[:4]
	}
	return targets
}

// QueryTarget runs a single query.
func QueryTarget(t queryTarget, timeout time.Duration) (QueryInfo, error) {
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	switch t.Protocol {
	case QueryProtoMinecraft:
		return QueryMinecraft(addr, t.Host, t.Port, timeout)
	case QueryProtoBedrock:
		return QueryBedrock(addr, timeout)
	default:
		return QueryA2S(addr, timeout)
	}
}

// -------------------------------------------------------------
// Background poller
// -------------------------------------------------------------

type queryState struct {
	info   QueryInfo
	target *queryTarget // last working target
}

// startQueryLoop periodically queries every running server.
func (im *InstanceManager) startQueryLoop() {
	go func() {
		time.Sleep(3 * time.Second)
		for {
			im.pollQueries()
			time.Sleep(queryInterval)
		}
	}()
}

func (im *InstanceManager) pollQueries() {
	im.mu.Lock()
	servers := make([]GameServerInstance, 0, len(im.instances))
	for _, s := range im.instances {
		servers = append(servers, *s)
	}
	im.mu.Unlock()

	sem := make(chan struct{}, queryConcurrency)
	var wg sync.WaitGroup
	for _, srv := range servers {
		if srv.Status != "running" {
			im.setQuery(srv.ID, queryState{})
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(srv GameServerInstance) {
			defer wg.Done()
			defer func() { <-sem }()
			im.setQuery(srv.ID, im.queryServer(srv))
		}(srv)
	}
	wg.Wait()
}

func (im *InstanceManager) queryServer(srv GameServerInstance) queryState {
	im.queryMu.Lock()
	prev := im.queryCache[srv.ID]
	im.queryMu.Unlock()

	targets := queryTargets(srv)
	if prev.target != nil {
		targets = append([]queryTarget{*prev.target}, targets...)
	}
	tried := map[queryTarget]bool{}
	for _, t := range targets {
		if tried[t] {
			continue
		}
		tried[t] = true
		info, err := QueryTarget(t, queryTimeout)
		if err == nil {
			target := t
			return queryState{info: info, target: &target}
		}
	}
	return queryState{target: prev.target}
}

func (im *InstanceManager) setQuery(id string, st queryState) {
	im.queryMu.Lock()
	defer im.queryMu.Unlock()
	if im.queryCache == nil {
		im.queryCache = make(map[string]queryState)
	}
	im.queryCache[id] = st
}

func (im *InstanceManager) cachedQuery(id string) QueryInfo {
	im.queryMu.Lock()
	defer im.queryMu.Unlock()
	return im.queryCache[id].info
}

func finishQueryInfo(info *QueryInfo, protocol string, start time.Time) {
	info.Online = true
	info.Protocol = protocol
	info.Name = sanitizeDisplayText(info.Name, 128)
	info.Map = sanitizeDisplayText(info.Map, 128)
	info.Ping = int(time.Since(start).Milliseconds())
	info.Updated = time.Now().UTC()
	if len(info.Players) > maxQueryPlayers {
		info.Players = info.Players[:maxQueryPlayers]
	}
	for i := range info.Players {
		info.Players[i].Name = sanitizeDisplayText(info.Players[i].Name, 64)
	}
	sort.SliceStable(info.Players, func(i, j int) bool { return info.Players[i].Score > info.Players[j].Score })
}

// -------------------------------------------------------------
// Valve A2S
// -------------------------------------------------------------

var a2sInfoRequest = append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'T'}, []byte("Source Engine Query\x00")...)

type byteReader struct {
	b   []byte
	pos int
	err error
}

func (r *byteReader) u8() byte {
	if r.err != nil || r.pos+1 > len(r.b) {
		r.err = errQueryFormat
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

func (r *byteReader) u16() uint16 {
	if r.err != nil || r.pos+2 > len(r.b) {
		r.err = errQueryFormat
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.pos:])
	r.pos += 2
	return v
}

func (r *byteReader) u32() uint32 {
	if r.err != nil || r.pos+4 > len(r.b) {
		r.err = errQueryFormat
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v
}

func (r *byteReader) str() string {
	if r.err != nil {
		return ""
	}
	end := bytes.IndexByte(r.b[r.pos:], 0)
	if end < 0 {
		r.err = errQueryFormat
		return ""
	}
	s := string(r.b[r.pos : r.pos+end])
	r.pos += end + 1
	return s
}

// a2sExchange sends req and returns the reassembled response payload (after the 0xFFFFFFFF header).
func a2sExchange(conn net.Conn, req []byte) ([]byte, error) {
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	var parts map[int][]byte
	total := 0
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n < 5 {
			return nil, errQueryFormat
		}
		header := int32(binary.LittleEndian.Uint32(buf[:4]))
		if header == -1 {
			return append([]byte(nil), buf[4:n]...), nil
		}
		if header != -2 {
			return nil, errQueryFormat
		}
		// Split packet (Source format): id(4) total(1) number(1) size(2)
		if n < 12 {
			return nil, errQueryFormat
		}
		id := binary.LittleEndian.Uint32(buf[4:8])
		if id&0x80000000 != 0 {
			return nil, errors.New("compressed responses are not supported")
		}
		total = int(buf[8])
		number := int(buf[9])
		if total == 0 || total > 32 || number >= total {
			return nil, errQueryFormat
		}
		if parts == nil {
			parts = make(map[int][]byte)
		}
		parts[number] = append([]byte(nil), buf[12:n]...)
		if len(parts) == total {
			var joined []byte
			for i := 0; i < total; i++ {
				joined = append(joined, parts[i]...)
			}
			if len(joined) < 4 || int32(binary.LittleEndian.Uint32(joined[:4])) != -1 {
				return nil, errQueryFormat
			}
			return joined[4:], nil
		}
	}
}

// a2sRequest performs a request that may be answered with a challenge (0x41).
func a2sRequest(conn net.Conn, base []byte, challengeSuffix bool) ([]byte, error) {
	req := base
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := a2sExchange(conn, req)
		if err != nil {
			return nil, err
		}
		if len(resp) >= 5 && resp[0] == 0x41 {
			challenge := resp[1:5]
			if challengeSuffix {
				req = append(append([]byte(nil), base...), challenge...)
			} else {
				// A2S_PLAYER: the challenge replaces the placeholder.
				req = append(append([]byte(nil), base[:5]...), challenge...)
			}
			continue
		}
		return resp, nil
	}
	return nil, errors.New("challenge loop")
}

// QueryA2S queries a Steam/Source compatible server (A2S_INFO + A2S_PLAYER).
func QueryA2S(addr string, timeout time.Duration) (QueryInfo, error) {
	start := time.Now()
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return QueryInfo{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	resp, err := a2sRequest(conn, a2sInfoRequest, true)
	if err != nil {
		return QueryInfo{}, err
	}
	info, err := parseA2SInfo(resp)
	if err != nil {
		return QueryInfo{}, err
	}
	ping := time.Since(start)

	if info.NumPlayers > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
		playerReq := []byte{0xFF, 0xFF, 0xFF, 0xFF, 'U', 0xFF, 0xFF, 0xFF, 0xFF}
		if presp, err := a2sRequest(conn, playerReq, false); err == nil {
			if players, err := parseA2SPlayers(presp); err == nil {
				info.Players = players
			}
		}
	}

	finishQueryInfo(&info, QueryProtoA2S, start)
	info.Ping = int(ping.Milliseconds())
	return info, nil
}

func parseA2SInfo(resp []byte) (QueryInfo, error) {
	r := &byteReader{b: resp}
	var info QueryInfo
	switch r.u8() {
	case 'I': // Source
		r.u8() // protocol
		info.Name = r.str()
		info.Map = r.str()
		r.str() // folder
		r.str() // game
		r.u16() // app id
		info.NumPlayers = int(r.u8())
		info.MaxPlayers = int(r.u8())
		info.Bots = int(r.u8())
	case 'm': // obsolete GoldSrc
		r.str() // address
		info.Name = r.str()
		info.Map = r.str()
		r.str() // folder
		r.str() // game
		info.NumPlayers = int(r.u8())
		info.MaxPlayers = int(r.u8())
	default:
		return QueryInfo{}, errQueryFormat
	}
	if r.err != nil {
		return QueryInfo{}, r.err
	}
	return info, nil
}

func parseA2SPlayers(resp []byte) ([]PlayerInfo, error) {
	r := &byteReader{b: resp}
	if r.u8() != 'D' {
		return nil, errQueryFormat
	}
	count := int(r.u8())
	players := []PlayerInfo{}
	for i := 0; i < count && r.err == nil; i++ {
		r.u8() // index
		name := r.str()
		score := int32(r.u32())
		duration := math.Float32frombits(r.u32())
		if r.err != nil {
			break
		}
		if strings.TrimSpace(name) == "" {
			continue // connecting players have no name yet
		}
		players = append(players, PlayerInfo{Name: name, Score: int(score), Time: formatDuration(float64(duration))})
	}
	return players, nil
}

func formatDuration(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return ""
	}
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm %02ds", m, int(d.Seconds())%60)
}

// -------------------------------------------------------------
// Minecraft Java Edition: Server List Ping
// -------------------------------------------------------------

func appendVarInt(b []byte, v int32) []byte {
	u := uint32(v)
	for {
		if u&^0x7F == 0 {
			return append(b, byte(u))
		}
		b = append(b, byte(u&0x7F|0x80))
		u >>= 7
	}
}

func readVarInt(r io.ByteReader) (int32, error) {
	var result uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(result), nil
		}
	}
	return 0, errQueryFormat
}

type mcStatus struct {
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
		Sample []struct {
			Name string `json:"name"`
		} `json:"sample"`
	} `json:"players"`
	Description json.RawMessage `json:"description"`
}

// mcText flattens a Minecraft chat component (string or {"text":..,"extra":[..]}).
func mcText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var comp struct {
		Text  string            `json:"text"`
		Extra []json.RawMessage `json:"extra"`
	}
	if json.Unmarshal(raw, &comp) != nil {
		return ""
	}
	out := comp.Text
	for _, e := range comp.Extra {
		out += mcText(e)
	}
	return out
}

var reMinecraftFormatting = regexp.MustCompile(`\x{00A7}.`)

// QueryMinecraft queries a Minecraft Java server via the status protocol.
func QueryMinecraft(addr, host string, port int, timeout time.Duration) (QueryInfo, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return QueryInfo{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// Handshake: id 0x00, protocol -1, host, port, next state 1 (status)
	var hs []byte
	hs = appendVarInt(hs, 0x00)
	hs = appendVarInt(hs, -1)
	hs = appendVarInt(hs, int32(len(host)))
	hs = append(hs, host...)
	hs = append(hs, byte(port>>8), byte(port))
	hs = appendVarInt(hs, 1)
	packet := appendVarInt(nil, int32(len(hs)))
	packet = append(packet, hs...)
	packet = append(packet, 0x01, 0x00) // status request
	if _, err := conn.Write(packet); err != nil {
		return QueryInfo{}, err
	}

	br := &limitedByteReader{r: conn, left: 1 << 20}
	length, err := readVarInt(br)
	if err != nil || length <= 0 || length > 1<<20 {
		return QueryInfo{}, errQueryFormat
	}
	if id, err := readVarInt(br); err != nil || id != 0x00 {
		return QueryInfo{}, errQueryFormat
	}
	strLen, err := readVarInt(br)
	if err != nil || strLen <= 0 || strLen > 1<<20 {
		return QueryInfo{}, errQueryFormat
	}
	data := make([]byte, strLen)
	if _, err := io.ReadFull(br, data); err != nil {
		return QueryInfo{}, err
	}

	var st mcStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return QueryInfo{}, errQueryFormat
	}
	info := QueryInfo{
		Name:       reMinecraftFormatting.ReplaceAllString(mcText(st.Description), ""),
		NumPlayers: st.Players.Online,
		MaxPlayers: st.Players.Max,
	}
	for _, p := range st.Players.Sample {
		info.Players = append(info.Players, PlayerInfo{Name: p.Name})
	}
	finishQueryInfo(&info, QueryProtoMinecraft, start)
	return info, nil
}

// limitedByteReader reads byte-wise from a connection with an upper bound.
type limitedByteReader struct {
	r    io.Reader
	left int
	one  [1]byte
}

func (l *limitedByteReader) ReadByte() (byte, error) {
	if l.left <= 0 {
		return 0, errQueryFormat
	}
	if _, err := io.ReadFull(l.r, l.one[:]); err != nil {
		return 0, err
	}
	l.left--
	return l.one[0], nil
}

func (l *limitedByteReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, errQueryFormat
	}
	if len(p) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= n
	return n, err
}

// -------------------------------------------------------------
// Minecraft Bedrock Edition: RakNet unconnected ping
// -------------------------------------------------------------

var raknetMagic = []byte{0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe, 0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78}

// QueryBedrock queries a Minecraft Bedrock server.
func QueryBedrock(addr string, timeout time.Duration) (QueryInfo, error) {
	start := time.Now()
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return QueryInfo{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	req := make([]byte, 33)
	req[0] = 0x01
	binary.BigEndian.PutUint64(req[1:9], uint64(time.Now().UnixMilli()))
	copy(req[9:25], raknetMagic)
	binary.BigEndian.PutUint64(req[25:33], 0x4C47534D44415348) // client GUID
	if _, err := conn.Write(req); err != nil {
		return QueryInfo{}, err
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return QueryInfo{}, err
	}
	info, err := parseBedrockPong(buf[:n])
	if err != nil {
		return QueryInfo{}, err
	}
	finishQueryInfo(&info, QueryProtoBedrock, start)
	return info, nil
}

func parseBedrockPong(b []byte) (QueryInfo, error) {
	// 0x1C, time(8), server guid(8), magic(16), strlen(2), string
	if len(b) < 35 || b[0] != 0x1C || !bytes.Equal(b[17:33], raknetMagic) {
		return QueryInfo{}, errQueryFormat
	}
	strLen := int(binary.BigEndian.Uint16(b[33:35]))
	if 35+strLen > len(b) {
		return QueryInfo{}, errQueryFormat
	}
	fields := strings.Split(string(b[35:35+strLen]), ";")
	// MCPE;motd;protocol;version;online;max;guid;level name;gamemode;...
	if len(fields) < 6 {
		return QueryInfo{}, errQueryFormat
	}
	online, _ := strconv.Atoi(fields[4])
	max, _ := strconv.Atoi(fields[5])
	info := QueryInfo{Name: fields[1], NumPlayers: online, MaxPlayers: max}
	if len(fields) > 7 {
		info.Map = fields[7]
	}
	return info, nil
}
