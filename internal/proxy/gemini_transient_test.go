package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

func TestForwardGemini_TransportTimeout_IsRetryable(t *testing.T) {
	tests := []struct {
		name    string
		isError func(err error) bool
		// wantRetryable: true means ForwardGemini must return UpstreamError 502
		wantRetryable bool
	}{
		{
			name: "ResponseHeaderTimeout classifies as retryable 502",
			isError: func(err error) bool {
				var ue *UpstreamError
				return errors.As(err, &ue) && ue.StatusCode == http.StatusBadGateway
			},
			wantRetryable: true,
		},
	}

	// Subtests exercise the classifier directly — the httptest server below
	// covers the ForwardGemini wiring, because a live ResponseHeaderTimeout
	// server is timing-sensitive and flaky.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cases := []struct {
				err  error
				want bool
			}{
				{err: fmt.Errorf("Post %q: http2: timeout awaiting response headers", "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent"), want: true},
				{err: fmt.Errorf("timeout awaiting response headers"), want: true},
				{err: context.DeadlineExceeded, want: true},
				{err: &url.Error{Op: "Post", URL: "https://x", Err: fmt.Errorf("timeout")}, want: false}, // bare "timeout" without net timeout opt-in
				{err: fmt.Errorf("EOF"), want: true},
				{err: fmt.Errorf("connection reset by peer"), want: true},
				{err: fmt.Errorf("dial tcp: connection refused"), want: true},
				{err: fmt.Errorf("unknown error"), want: false},
				{err: nil, want: false},
			}
			for i, c := range cases {
				if got := isTransientGeminiTransportError(c.err); got != c.want {
					t.Errorf("case %d isTransientGeminiTransportError(%v) = %v, want %v", i, c.err, got, c.want)
				}
			}
		})
	}
}

func TestForwardGemini_HeaderTimeout_ReturnsRetryable502(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Never write headers within ResponseHeaderTimeout — let the client timer fire.
		// Unblock on client close OR a bounded sleep so srv.Close() can return
		// (HTTP/1 server does not cancel r.Context() on client timeout alone).
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	transport := &http.Transport{
		ResponseHeaderTimeout: 50_000_000, // 50ms — keep the test fast
		ForceAttemptHTTP2:     true,
	}
	client := &http.Client{Transport: transport}

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3.8-flash-medium","messages":[{"role":"user","content":"hi"}]}`
	// projectID non-empty routes through WrapForAntigravity, which needs valid JSON.
	// If translation fails the call returns before dialing, so pin the body to the
	// OpenAI shape TranslateOpenAIToGeminiWithSession accepts.

	_, err := ForwardGemini(context.Background(), client, cfg, "ya29.fake", body, false, "proj-1", "gemini-3.8-flash-medium")
	if err == nil {
		t.Fatal("ForwardGemini() error = nil, want retryable 502")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("ForwardGemini() error type = %T (%v), want *UpstreamError", err, err)
	}
	if ue.StatusCode != http.StatusBadGateway {
		t.Errorf("ForwardGemini() status = %d, want 502", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "ForwardGemini antigravity transport") {
		t.Errorf("ForwardGemini() body = %s, want transport marker", string(ue.Body))
	}
	// hits may be 0-1 depending on whether the transport dialed before the timer;
	// the assertion that matters is the error classification, not the count.
}

func TestForwardGemini_ClientCancelled_StaysTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: true}}
	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := `{"model":"gemini-3.8-flash-medium","messages":[{"role":"user","content":"hi"}]}`

	_, err := ForwardGemini(ctx, client, cfg, "ya29.fake", body, false, "proj-1", "gemini-3.8-flash-medium")
	if err == nil {
		t.Fatal("ForwardGemini() error = nil, want context error")
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		t.Errorf("ForwardGemini() cancelled error must stay unwrapped (not UpstreamError), got %T: %v", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ForwardGemini() error = %v, want context.Canceled", err)
	}
}
