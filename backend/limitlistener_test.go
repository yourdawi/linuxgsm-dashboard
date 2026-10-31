package backend

import (
	"net"
	"testing"
	"time"
)

func TestLimitListener(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := LimitListener(raw, 2)
	defer l.Close()

	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", raw.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	wait := func() net.Conn {
		select {
		case c := <-accepted:
			return c
		case <-time.After(2 * time.Second):
			return nil
		}
	}
	dial()
	dial()
	first, second := wait(), wait()
	if first == nil || second == nil {
		t.Fatal("first two connections not accepted")
	}
	dial()
	if c := wait(); c != nil {
		t.Fatal("third connection accepted while two are open")
	}
	first.Close()
	first.Close() // closing twice frees only one slot
	if c := wait(); c == nil {
		t.Fatal("third connection not accepted after one was closed")
	}
}
