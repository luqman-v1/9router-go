package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// blackholeEdge answers nothing for the first `blackhole` requests and closes
// those sockets, then serves normally: an upstream edge that picked a request up
// on a keep-alive socket it had already given up on. A reaped socket looks
// exactly like this from the client — the request was written, the peer hung up,
// no status line ever arrived (upstream decolua/9router@963787f73).
func blackholeEdge(t *testing.T, blackhole int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if served.Add(1) <= blackhole {
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					// The request is fully read; the peer is about to hang up.
					time.Sleep(5 * time.Millisecond)
					_ = c.Close()
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &served
}

// TestDoRequest_RetriesDeadKeepAliveSocket is the contract: a request that died
// on a socket the upstream had already closed must be replayed on a fresh one,
// not handed back to the caller as a failed provider turn.
func TestDoRequest_RetriesDeadKeepAliveSocket(t *testing.T) {
	srv, served := blackholeEdge(t, 1)

	resp, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"gemini-3-pro"}`))
	if err != nil {
		t.Fatalf("a dead keep-alive socket must be retried, not surfaced: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from the fresh socket, got %d", resp.StatusCode)
	}
	// One request blackholed, one served: the retry reached the provider.
	if got := served.Load(); got != 2 {
		t.Errorf("expected 2 requests seen upstream (1 dropped + 1 served), got %d", got)
	}
}

// Only a failure before any response byte is retried. An answer in hand is the
// provider's: replaying it would run the request twice.
func TestIsDeadKeepAliveSocket(t *testing.T) {
	inHand := &http.Response{StatusCode: http.StatusOK}
	tests := []struct {
		name string
		err  error
		resp *http.Response
		want bool
	}{
		{name: "nil error is not a dead socket"},
		{name: "server closed idle connection", err: errors.New(`Post "https://x": http: server closed idle connection`), want: true},
		{name: "wrapped EOF", err: fmt.Errorf(`Post "https://x": %w`, io.EOF), want: true},
		// A real broken pipe surfaces as *net.OpError wrapping *os.SyscallError
		// wrapping syscall.EPIPE, or a *url.Error wrapping it; the chain must be
		// walked with errors.Is, not matched by substring text.
		{name: "broken pipe errno", err: fmt.Errorf("write tcp 1.2.3.4:5->6.7.8.9:443: %w", syscall.EPIPE), want: true},
		{name: "broken pipe wrapped in op error", err: &net.OpError{Op: "write", Net: "tcp", Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}}, want: true},
		{name: "connection reset by peer errno", err: fmt.Errorf("read tcp 1.2.3.4:5->6.7.8.9:443: %w", syscall.ECONNRESET), want: true},
		{name: "connection reset wrapped in op error", err: &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}}, want: true},
		{name: "response in hand is not retried", err: errors.New(`Post "https://x": EOF`), resp: inHand},
		{name: "dial failure is not a dead socket", err: errors.New("dial tcp: no route to host")},
		{name: "context deadline is not a dead socket", err: context.DeadlineExceeded},
		// An unrelated error whose message merely mentions the errno text must
		// not be retried: errors.Is is the classifier, not substring matching.
		{name: "unrelated broken pipe text is not a dead socket", err: errors.New("broken pipe in unrelated context")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeadKeepAliveSocket(tt.err, tt.resp); got != tt.want {
				t.Errorf("isDeadKeepAliveSocket(%v, %v) = %v, want %v", tt.err, tt.resp, got, tt.want)
			}
		})
	}
}

// Exactly one retry. A second dead socket is the provider's answer to the retry,
// and reporting it beats looping until the client gives up.
func TestSend_RetriesDeadKeepAliveSocketOnlyOnce(t *testing.T) {
	srv, served := blackholeEdge(t, 2)

	req, err := newJSONRequest(context.Background(), "POST", srv.URL, nil, []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	_, err = send(srv.Client(), req)
	if err == nil {
		t.Fatal("expected an error when the retry also lands on a dead socket")
	}
	if !strings.Contains(err.Error(), "EOF") {
		t.Errorf("the transport's own error must survive, got %v", err)
	}
	if got := served.Load(); got != 2 {
		t.Errorf("expected exactly 2 attempts (1 + 1 retry), got %d", got)
	}
}

// A request whose body cannot be rebuilt must not be replayed: the first attempt
// consumed the bytes, so a retry would send a truncated POST.
func TestSend_DoesNotRetryWithoutReplayableBody(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		served.Add(1)
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				time.Sleep(5 * time.Millisecond)
				_ = c.Close()
			}
		}
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), "POST", srv.URL, io.NopCloser(strings.NewReader(`{"model":"x"}`)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.GetBody = nil
	req.ContentLength = -1

	if _, err := send(srv.Client(), req); err == nil {
		t.Fatal("expected the socket failure to surface when the body cannot be replayed")
	}
	if got := served.Load(); got != 1 {
		t.Errorf("expected exactly one attempt without a replayable body, got %d", got)
	}
}
