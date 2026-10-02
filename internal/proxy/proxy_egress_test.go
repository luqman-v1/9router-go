package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// A request the operator routed through a proxy pool must never be completed
// from the host's own IP. doRequestOnce re-dials direct when a proxy refuses the
// tunnel, which was right for an ambient HTTP_PROXY the operator never chose and
// wrong for an assigned pool: the client answered anyway, the upstream saw the
// real egress IP, and the dashboard still showed the connection as proxied.

func deadProxyClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://127.0.0.1:1") },
	}}
}

func TestDoRequest_AssignedProxyFailureDoesNotFallBackToDirect(t *testing.T) {
	var directHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	_, err := DoRequest(context.Background(), deadProxyClient(t), "POST", upstream.URL, nil, []byte(`{"model":"x"}`))
	if err == nil {
		t.Fatal("an assigned proxy that cannot be reached must fail the request")
	}
	if !strings.Contains(err.Error(), "refusing to route direct") {
		t.Errorf("the error must name the egress decision, got %v", err)
	}
	if got := directHits.Load(); got != 0 {
		t.Errorf("request reached the upstream %d time(s) from the real IP despite an assigned proxy", got)
	}
}

// An ambient proxy is not an operator decision, so the direct escape hatch must
// keep working: a sandbox HTTP_PROXY that refuses the tunnel is noise to route
// around, and refusing to would break every install behind one.
func TestDoRequest_AmbientProxyStillFallsBackToDirect(t *testing.T) {
	var directHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	// DefaultTransport carries a nil Proxy func until an environment proxy is
	// applied, which is the shape an ambient-proxy client ends up with once the
	// proxy itself is the thing that failed.
	ambient := &http.Client{Transport: NewFallbackTransport(nil)}

	resp, err := DoRequest(context.Background(), ambient, "POST", upstream.URL, nil, []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatalf("an ambient proxy must not block the direct path, got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 through the direct fallback, got %d", resp.StatusCode)
	}
	if directHits.Load() == 0 {
		t.Error("the direct fallback did not reach the upstream")
	}
}

func TestProxiesViaAssignment(t *testing.T) {
	deadURL, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}

	tests := []struct {
		name    string
		client  *http.Client
		want    bool
		explain string
	}{
		{name: "nil client", client: nil, want: false, explain: "no client means no assigned proxy"},
		{name: "nil transport", client: &http.Client{}, want: false, explain: "the default client goes direct"},
		{
			name:   "assigned pool transport",
			client: &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(deadURL)}},
			want:   true,
		},
		{
			name:   "fallback transport with a proxy base",
			client: &http.Client{Transport: &FallbackTransport{Base: &http.Transport{Proxy: http.ProxyURL(deadURL)}}},
			want:   true,
		},
		{
			name:    "fallback transport without a proxy base",
			client:  &http.Client{Transport: NewFallbackTransport(nil)},
			want:    false,
			explain: "an ambient fallback wrapper carries no operator assignment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxiesViaAssignment(tt.client); got != tt.want {
				t.Errorf("proxiesViaAssignment() = %v, want %v (%s)", got, tt.want, tt.explain)
			}
		})
	}
}
