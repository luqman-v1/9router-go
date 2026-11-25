package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/proxy"
)

// The media client must judge the address it connects to, not only the URL it
// was handed. A hostname that resolves public for the pre-flight check and
// private for the dial defeats every name-based guard; only the dial-time check
// sees the address that is actually used.
func TestMediaClient_DialGuardRefusesPrivateWhenNetworkingDisallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")
	h := NewMediaHandler(nil, nil, nil)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := h.Client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a loopback dial must be refused while private networking is off")
	}
	if !contains(err.Error(), proxy.ErrSSRFBlocked.Error()) && !contains(err.Error(), "SSRF") {
		t.Fatalf("refusal must name the SSRF guard, got: %v", err)
	}
}

// The same client must still reach a loopback provider with the default
// setting: a gateway with a local ollama or a LAN SearXNG is a normal
// deployment, and the guard exists to be turned on, not to break it.
func TestMediaClient_DialGuardAllowsPrivateWhenNetworkingAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	t.Setenv("ALLOW_PRIVATE_NETWORKING", "true")
	h := NewMediaHandler(nil, nil, nil)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		t.Fatalf("default private networking must reach loopback: %v", err)
	}
	_ = resp.Body.Close()
}

func contains(s, sub string) bool {
	if len(sub) == 0 || len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
