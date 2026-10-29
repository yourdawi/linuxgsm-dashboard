package backend

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Minimal WebSocket transport (RFC 6455) for the agent tunnel. It only carries an opaque byte
// stream (the inner TLS session of the tunnel) in binary frames, so only what that needs is
// implemented: no text frames, no extensions, no subprotocols. The standard library has no
// WebSocket support; a few hundred lines here are less attack surface than an external module.
//
// Bugs in this layer can break the connection but cannot expose data: everything sent through
// it is already encrypted and authenticated by the inner TLS session.

const (
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// wsMaxFramePayload limits data frames read from the peer. The server side parses these
	// frames before the inner TLS handshake has authenticated anyone.
	wsMaxFramePayload = 1 << 20
	// wsWriteChunk is the payload size of the frames this side writes.
	wsWriteChunk = 32 << 10
	// wsHandshakeTimeout applies to the HTTP upgrade when the caller sets no deadline.
	wsHandshakeTimeout = 10 * time.Second
)

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA
)

// ErrWebSocketProtocol reports a frame that violates the protocol subset used by the tunnel.
var ErrWebSocketProtocol = errors.New("websocket protocol error")

// wsConn is a net.Conn on top of a WebSocket connection. Read returns the payload of binary
// frames as one continuous stream; frame boundaries carry no meaning.
type wsConn struct {
	conn   net.Conn
	br     *bufio.Reader
	client bool // clients mask their frames, servers must not

	readMu     sync.Mutex
	remaining  int64 // unread payload bytes of the current data frame
	mask       [4]byte
	masked     bool
	maskPos    int
	fragmented bool // inside a fragmented binary message
	readErr    error

	writeMu   sync.Mutex
	writeBuf  []byte
	closeOnce sync.Once
}

func newWSConn(conn net.Conn, br *bufio.Reader, client bool) *wsConn {
	return &wsConn{conn: conn, br: br, client: client}
}

func (c *wsConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for c.remaining == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		if err := c.nextDataFrame(); err != nil {
			c.readErr = err
			return 0, err
		}
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.br.Read(p)
	if c.masked {
		for i := 0; i < n; i++ {
			p[i] ^= c.mask[c.maskPos&3]
			c.maskPos++
		}
	}
	c.remaining -= int64(n)
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		c.readErr = err
	}
	return n, err
}

// nextDataFrame reads frame headers until a data frame starts. Control frames in between are
// handled here: pings are answered, pongs ignored, a close frame ends the stream with io.EOF.
func (c *wsConn) nextDataFrame() error {
	for {
		var head [2]byte
		if _, err := io.ReadFull(c.br, head[:]); err != nil {
			return err
		}
		fin := head[0]&0x80 != 0
		if head[0]&0x70 != 0 {
			return fmt.Errorf("%w: reserved bits set", ErrWebSocketProtocol)
		}
		opcode := head[0] & 0x0F
		masked := head[1]&0x80 != 0
		if masked == c.client {
			return fmt.Errorf("%w: wrong masking", ErrWebSocketProtocol)
		}
		length := int64(head[1] & 0x7F)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.br, ext[:]); err != nil {
				return err
			}
			length = int64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.br, ext[:]); err != nil {
				return err
			}
			v := binary.BigEndian.Uint64(ext[:])
			if v > wsMaxFramePayload {
				return fmt.Errorf("%w: frame too large", ErrWebSocketProtocol)
			}
			length = int64(v)
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.br, mask[:]); err != nil {
				return err
			}
		}

		if opcode >= wsOpClose {
			if !fin || length > 125 {
				return fmt.Errorf("%w: invalid control frame", ErrWebSocketProtocol)
			}
			payload := make([]byte, length)
			if _, err := io.ReadFull(c.br, payload); err != nil {
				return err
			}
			if masked {
				for i := range payload {
					payload[i] ^= mask[i&3]
				}
			}
			switch opcode {
			case wsOpPing:
				if err := c.writeFrame(wsOpPong, payload); err != nil {
					return err
				}
			case wsOpPong:
			case wsOpClose:
				code := payload
				if len(code) > 2 {
					code = code[:2]
				}
				_ = c.writeFrame(wsOpClose, code)
				return io.EOF
			default:
				return fmt.Errorf("%w: unknown opcode %d", ErrWebSocketProtocol, opcode)
			}
			continue
		}

		switch opcode {
		case wsOpBinary:
			if c.fragmented {
				return fmt.Errorf("%w: new message inside fragmented message", ErrWebSocketProtocol)
			}
		case wsOpContinuation:
			if !c.fragmented {
				return fmt.Errorf("%w: unexpected continuation frame", ErrWebSocketProtocol)
			}
		case wsOpText:
			return fmt.Errorf("%w: text frames are not supported", ErrWebSocketProtocol)
		default:
			return fmt.Errorf("%w: unknown opcode %d", ErrWebSocketProtocol, opcode)
		}
		if length > wsMaxFramePayload {
			return fmt.Errorf("%w: frame too large", ErrWebSocketProtocol)
		}
		c.fragmented = !fin
		c.remaining = length
		c.mask = mask
		c.masked = masked
		c.maskPos = 0
		if length > 0 {
			return nil
		}
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > wsWriteChunk {
			chunk = chunk[:wsWriteChunk]
		}
		if err := c.writeFrame(wsOpBinary, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// writeFrame writes one unfragmented frame. Header and payload go out in a single write so
// frames from concurrent writers (data and pong replies) never interleave.
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	buf := c.writeBuf[:0]
	buf = append(buf, 0x80|opcode)
	maskBit := byte(0)
	if c.client {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n <= 125:
		buf = append(buf, maskBit|byte(n))
	case n <= 0xFFFF:
		buf = append(buf, maskBit|126, byte(n>>8), byte(n))
	default:
		buf = append(buf, maskBit|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(n))
	}
	if c.client {
		var mask [4]byte
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		buf = append(buf, mask[:]...)
		start := len(buf)
		buf = append(buf, payload...)
		for i := range buf[start:] {
			buf[start+i] ^= mask[i&3]
		}
	} else {
		buf = append(buf, payload...)
	}
	c.writeBuf = buf
	_, err := c.conn.Write(buf)
	return err
}

// Close sends a close frame (best effort) and closes the connection.
func (c *wsConn) Close() error {
	err := net.ErrClosed
	c.closeOnce.Do(func() {
		_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
		_ = c.writeFrame(wsOpClose, []byte{0x03, 0xE8}) // 1000 normal closure
		err = c.conn.Close()
	})
	return err
}

func (c *wsConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *wsConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

func wsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerHasToken reports whether a comma separated header contains token (case-insensitive).
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// AcceptWebSocket upgrades an HTTP/1.1 request to a WebSocket connection. On failure it has
// already answered the request with an error status.
func AcceptWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return nil, errors.New("websocket upgrade: method not allowed")
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") || !headerHasToken(r.Header, "Upgrade", "websocket") {
		http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
		return nil, errors.New("websocket upgrade: missing upgrade headers")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "Unsupported WebSocket version", http.StatusUpgradeRequired)
		return nil, errors.New("websocket upgrade: unsupported version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		http.Error(w, "Invalid WebSocket key", http.StatusBadRequest)
		return nil, errors.New("websocket upgrade: invalid key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "WebSocket not supported", http.StatusInternalServerError)
		return nil, errors.New("websocket upgrade: connection cannot be hijacked")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("websocket upgrade: %w", err)
	}
	// The client must wait for the 101 response; data before it is a protocol violation.
	if brw.Reader.Buffered() > 0 {
		conn.Close()
		return nil, errors.New("websocket upgrade: data sent before handshake completed")
	}
	// Drop the deadlines the HTTP server set for the request.
	_ = conn.SetDeadline(time.Now().Add(wsHandshakeTimeout))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAcceptKey(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket upgrade: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return newWSConn(conn, brw.Reader, false), nil
}

// DialWebSocket opens a WebSocket connection to a ws:// or wss:// URL. tlsConfig configures the
// outer TLS of wss:// URLs (nil uses the system roots); it is unrelated to the tunnel's own TLS.
func DialWebSocket(ctx context.Context, rawURL string, tlsConfig *tls.Config) (net.Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	var secure bool
	switch u.Scheme {
	case "ws":
	case "wss":
		secure = true
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q (ws or wss)", u.Scheme)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
		if secure {
			port = "443"
		}
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wsHandshakeTimeout)
		defer cancel()
	}
	dialer := &net.Dialer{KeepAlive: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	}
	if secure {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
	}

	var keyBytes [16]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes[:])
	path := u.RequestURI()
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"User-Agent: lgsm-dashboard-agent\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, fmt.Errorf("WebSocket upgrade failed at %s: %w", rawURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("WebSocket upgrade refused by %s: %s", rawURL, resp.Status)
	}
	if !headerHasToken(resp.Header, "Upgrade", "websocket") || !headerHasToken(resp.Header, "Connection", "upgrade") ||
		resp.Header.Get("Sec-WebSocket-Accept") != wsAcceptKey(key) {
		return nil, fmt.Errorf("WebSocket upgrade failed at %s: invalid handshake response", rawURL)
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return newWSConn(conn, br, true), nil
}
