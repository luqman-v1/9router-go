package executor

import (
	"net/http"
	"testing"

	"9router/proxy/internal/proxy"
)

// TestOpencodeProxyFromFallbackTransport pins the stealth-lane fix: the
// poolless client wraps its safe transport in a *FallbackTransport, so
// opencodeProxyFrom must unwrap Base — a bare type assert on *http.Transport
// returned "" and the stealth client dialed opencode.ai direct, silently
// bypassing the operator's ambient proxy (verified via SOCKS5 spy 2026-11-13).
func TestOpencodeProxyFromFallbackTransport(t *testing.T) {
	// NewSafeTransportForProxy with an explicit URL mirrors what the ambient
	// transport resolves, without depending on the stdlib once-cache that an
	// earlier test in this binary may already have frozen with other env.
	client := &http.Client{Transport: proxy.NewSafeTransportForProxy("socks5h://192.168.22.206:1080")}
	got := opencodeProxyFrom(client)
	if got != "socks5h://192.168.22.206:1080" {
		t.Fatalf("opencodeProxyFrom(FallbackTransport client) = %q, want the ambient proxy", got)
	}
}

// TestOpencodeProxyFromPlainTransport pins the pool case: a pool client keeps
// a bare *http.Transport and must keep resolving.
func TestOpencodeProxyFromPlainTransport(t *testing.T) {
	client := &http.Client{Transport: proxy.NewSafeTransportForProxy("socks5://10.0.0.1:1080")}
	if got := opencodeProxyFrom(client); got != "socks5://10.0.0.1:1080" {
		t.Fatalf("opencodeProxyFrom(pool client) = %q, want socks5://10.0.0.1:1080", got)
	}
}

func TestOpencodeProxyFromNil(t *testing.T) {
	if got := opencodeProxyFrom(nil); got != "" {
		t.Fatalf("opencodeProxyFrom(nil) = %q, want empty", got)
	}
}
