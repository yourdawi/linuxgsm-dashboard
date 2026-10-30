package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"testing"
	"time"
)

// User requests that occupy every regular stream must not starve the liveness ping.
func TestAgentTunnelPingHasReservedStreams(t *testing.T) {
	pki := newTunnelPKI(t)
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-release
		}
	})
	p := openTunnel(t, directCarrier(), pki, h, time.Minute)
	defer close(release)

	regular := agentTunnelMaxStreams - agentTunnelControlStreams
	for i := 0; i < regular; i++ {
		req, _ := http.NewRequest(http.MethodGet, "/slow", nil)
		resp, err := p.client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		defer resp.Body.Close()
	}
	// Every regular stream is taken: a further user request waits.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/slow", nil)
	if _, err := p.client.Do(req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the user request to wait, got %v", err)
	}
	cancel()
	// The ping still goes through.
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.client.Ping(ctx); err != nil {
		t.Fatalf("ping starved: %v", err)
	}
}

// The controller refuses 1xx answers through a client trace (see main.go tunnelTransport); this
// checks that the hook reaches the tunnel's HTTP/2 client and aborts the request.
func TestAgentTunnel1xxCanBeRefused(t *testing.T) {
	pki := newTunnelPKI(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "<https://evil.example/>; rel=preconnect")
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	p := openTunnel(t, directCarrier(), pki, h, time.Minute)
	refused := errors.New("refused")
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		Got1xxResponse: func(int, textproto.MIMEHeader) error { return refused },
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/x", nil)
	resp, err := p.client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("1xx answer was not refused")
	}
	// The tunnel stays usable.
	if code, _ := tunnelGet(t, p.client, AgentTunnelPingPath); code != http.StatusNoContent {
		t.Fatalf("tunnel broken after refusing a 1xx: %d", code)
	}
}
