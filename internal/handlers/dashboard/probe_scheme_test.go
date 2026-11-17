package dashboard

import (
	"net/http"
	"net/url"
	"testing"
)

// The pool test endpoint and the request path must agree on which proxy URLs
// are dialable, because it is the same decision made twice. This endpoint used
// to build its own transport (`&http.Transport{Proxy: http.ProxyURL(...)}`),
// which accepted any scheme url.Parse accepted and dialled without the SSRF
// dialer's proxy exemption. It now calls proxy.NewSafeTransportForProxy, so the
// scheme decision lives in one place and cannot drift.
func TestPoolProbeSharesSchemeDecision(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		wantDial    bool
		wantMessage string
	}{
		{
			name:     "a socks5 pool is probeable",
			target:   "socks5://user:pass@127.0.0.1:1080",
			wantDial: true,
		},
		{
			name:     "a socks5h pool is probeable",
			target:   "socks5h://user:pass@127.0.0.1:1080",
			wantDial: true,
		},
		{
			name:     "an http pool is probeable",
			target:   "http://proxy.example:8080",
			wantDial: true,
		},
		{
			// Refused with a readable reason rather than a dial error, so an
			// operator sees the actual problem.
			name:        "an undialable scheme is refused",
			target:      "quic://127.0.0.1:7777",
			wantDial:    false,
			wantMessage: "unsupported proxy protocol in quic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := poolProbeTransport(tt.target)
			if !tt.wantDial {
				if got != nil {
					t.Fatalf("transport = %v, want nil", got)
				}
				if msg != tt.wantMessage {
					t.Errorf("message = %q, want %q", msg, tt.wantMessage)
				}
				return
			}
			if msg != "" {
				t.Fatalf("unexpected refusal: %q", msg)
			}
			if got == nil {
				t.Fatal("a dialable proxy must produce a probe transport")
			}
			if got.Proxy == nil {
				t.Fatal("probe transport must carry a proxy func")
			}
			u, err := got.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.invalid"}})
			if err != nil || u == nil {
				t.Fatalf("proxy func: %v", err)
			}
			if u.String() != tt.target {
				t.Errorf("dials %q, want %q", u, tt.target)
			}
			// The SSRF dialer must survive: a probe without it would reach a
			// private target the request path blocks.
			if got.DialContext == nil {
				t.Error("probe transport must keep the SSRF dialer")
			}
		})
	}
}
