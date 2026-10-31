package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// frame builds a raw WebSocket frame. A nil mask writes an unmasked frame.
func frame(fin bool, opcode byte, payload []byte, mask []byte) []byte {
	var b []byte
	first := opcode
	if fin {
		first |= 0x80
	}
	b = append(b, first)
	maskBit := byte(0)
	if mask != nil {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n <= 125:
		b = append(b, maskBit|byte(n))
	case n <= 0xFFFF:
		b = append(b, maskBit|126, byte(n>>8), byte(n))
	default:
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		b = append(b, maskBit|127)
		b = append(b, ext[:]...)
	}
	if mask != nil {
		b = append(b, mask...)
		for i, c := range payload {
			b = append(b, c^mask[i&3])
		}
		return b
	}
	return append(b, payload...)
}

// fakeConn reads from a fixed input and records what is written.
type fakeConn struct {
	in  io.Reader
	out bytes.Buffer
}

func (f *fakeConn) Read(p []byte) (int, error)         { return f.in.Read(p) }
func (f *fakeConn) Write(p []byte) (int, error)        { return f.out.Write(p) }
func (f *fakeConn) Close() error                       { return nil }
func (f *fakeConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (f *fakeConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (f *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// serverSide returns a server-side wsConn reading input.
func serverSide(input []byte) (*wsConn, *fakeConn) {
	fc := &fakeConn{in: bytes.NewReader(input)}
	return newWSConn(fc, bufio.NewReader(fc), false), fc
}

var testMask = []byte{0x12, 0x34, 0x56, 0x78}

func TestWebSocketReadsMaskedFragmentedStream(t *testing.T) {
	var in []byte
	in = append(in, frame(false, wsOpBinary, []byte("hello "), testMask)...)
	in = append(in, frame(true, wsOpPing, []byte("p"), testMask)...)
	in = append(in, frame(false, wsOpContinuation, nil, testMask)...)
	in = append(in, frame(true, wsOpContinuation, []byte("world"), testMask)...)
	in = append(in, frame(true, wsOpBinary, bytes.Repeat([]byte{'x'}, 70000), testMask)...)
	in = append(in, frame(true, wsOpClose, []byte{0x03, 0xE8}, testMask)...)

	c, fc := serverSide(in)
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hello world" + strings.Repeat("x", 70000); string(got) != want {
		t.Fatalf("got %d bytes, want %d", len(got), len(want))
	}
	// The server answered the ping with an unmasked pong and the close with a close frame.
	out := fc.out.Bytes()
	if !bytes.HasPrefix(out, frame(true, wsOpPong, []byte("p"), nil)) {
		t.Fatalf("no pong: %x", out)
	}
	if !bytes.HasSuffix(out, frame(true, wsOpClose, []byte{0x03, 0xE8}, nil)) {
		t.Fatalf("no close reply: %x", out)
	}
}

func TestWebSocketRejectsInvalidFrames(t *testing.T) {
	big := make([]byte, wsMaxFramePayload+1)
	cases := map[string][]byte{
		"unmasked client frame": frame(true, wsOpBinary, []byte("x"), nil),
		"text frame":            frame(true, wsOpText, []byte("x"), testMask),
		"reserved bits":         append([]byte{0xC2, 0x81}, append(testMask, 'x')...),
		"oversized frame":       frame(true, wsOpBinary, big, testMask),
		"oversized 64-bit len":  {0x82, 0xFF, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 1, 2, 3, 4},
		"stray continuation":    frame(true, wsOpContinuation, []byte("x"), testMask),
		"fragmented ping":       frame(false, wsOpPing, nil, testMask),
		"long control frame":    frame(true, wsOpPing, make([]byte, 126), testMask),
		"unknown opcode":        frame(true, 0x3, []byte("x"), testMask),
		"binary inside fragment": append(frame(false, wsOpBinary, []byte("a"), testMask),
			frame(true, wsOpBinary, []byte("b"), testMask)...),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := serverSide(in)
			_, err := io.ReadAll(c)
			if !errors.Is(err, ErrWebSocketProtocol) {
				t.Fatalf("expected protocol error, got %v", err)
			}
		})
	}

	t.Run("masked server frame on client", func(t *testing.T) {
		fc := &fakeConn{in: bytes.NewReader(frame(true, wsOpBinary, []byte("x"), testMask))}
		c := newWSConn(fc, bufio.NewReader(fc), true)
		if _, err := io.ReadAll(c); !errors.Is(err, ErrWebSocketProtocol) {
			t.Fatalf("expected protocol error, got %v", err)
		}
	})
	t.Run("truncated frame", func(t *testing.T) {
		in := frame(true, wsOpBinary, []byte("hello"), testMask)
		c, _ := serverSide(in[:len(in)-2])
		if _, err := io.ReadAll(c); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected unexpected EOF, got %v", err)
		}
	})
}

func TestWebSocketClientMasksAndServerDoesNot(t *testing.T) {
	client := &fakeConn{in: bytes.NewReader(nil)}
	cw := newWSConn(client, bufio.NewReader(client), true)
	payload := bytes.Repeat([]byte("abc"), wsWriteChunk) // spans several frames
	if n, err := cw.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("client write: %d %v", n, err)
	}
	// What the client wrote must read back as the same stream on a server.
	s, _ := serverSide(client.out.Bytes())
	got, err := io.ReadAll(io.LimitReader(s, int64(len(payload))))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("round trip: %v, %d bytes", err, len(got))
	}

	server := &fakeConn{in: bytes.NewReader(nil)}
	sw := newWSConn(server, bufio.NewReader(server), false)
	if _, err := sw.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(server.out.Bytes(), frame(true, wsOpBinary, []byte("hi"), nil)) {
		t.Fatalf("server frame: %x", server.out.Bytes())
	}
}

func TestWebSocketHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := AcceptWebSocket(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c) // echo
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/connect"

	t.Run("echo", func(t *testing.T) {
		c, err := DialWebSocket(context.Background(), wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("echo: %q %v", buf, err)
		}
	})

	t.Run("plain GET is refused", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/agent/connect")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUpgradeRequired {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("refusal is reported with the URL", func(t *testing.T) {
		plain := httptest.NewServer(http.NotFoundHandler())
		defer plain.Close()
		u := "ws" + strings.TrimPrefix(plain.URL, "http") + "/agent/connect"
		_, err := DialWebSocket(context.Background(), u, nil)
		if err == nil || !strings.Contains(err.Error(), "refused by "+u) || !strings.Contains(err.Error(), "404") {
			t.Fatalf("unexpected error %v", err)
		}
	})

	t.Run("unsupported scheme", func(t *testing.T) {
		if _, err := DialWebSocket(context.Background(), "http://example.invalid/", nil); err == nil {
			t.Fatal("http:// accepted")
		}
	})
}

func FuzzWebSocketFrames(f *testing.F) {
	f.Add(frame(true, wsOpBinary, []byte("hello"), testMask))
	f.Add(frame(false, wsOpBinary, []byte("a"), testMask))
	f.Add(frame(true, wsOpPing, []byte("p"), testMask))
	f.Add(frame(true, wsOpClose, []byte{0x03, 0xE8}, testMask))
	f.Add([]byte{0x82, 0xFF, 0, 0, 0, 0, 0, 0x10, 0, 0})
	f.Fuzz(func(t *testing.T, in []byte) {
		c, _ := serverSide(in)
		n, _ := io.Copy(io.Discard, c)
		if n > int64(len(in)) {
			t.Fatalf("read %d bytes from %d bytes of input", n, len(in))
		}
	})
}
