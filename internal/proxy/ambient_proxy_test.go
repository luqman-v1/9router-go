package proxy

import (
	"net/http"
	"testing"
)

// TestNewSafeTransportHonoursAmbientEnv pins the fix for the silent ambient
// bypass: a *http.Transport with Proxy nil never consults the environment, so
// NewSafeTransport must install http.ProxyFromEnvironment explicitly. Without
// it, HTTP_PROXY / ALL_PROXY (including the values syncEnvFromViper lifts from
// .env) are set on the process yet every poolless connection egresses direct.
func TestNewSafeTransportHonoursAmbientEnv(t *testing.T) {
	// Swapped through the package's proxyFromEnvironment indirection: calling
	// the stdlib function directly would depend on whether an earlier test in
	// the binary already froze its once-cache with a different environment.
	withProxyResolver(t, "socks5h://192.168.22.206:1080")
	tr := NewSafeTransport()
	req, err := http.NewRequest("GET", "https://api.opencode.ai", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	u, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy() error: %v", err)
	}
	if u == nil || u.String() != "socks5h://192.168.22.206:1080" {
		t.Fatalf("NewSafeTransport ambient proxy = %v, want socks5h://192.168.22.206:1080", u)
	}
}

// TestNewDirectSafeTransportNeverProxies pins the escape legs: the direct
// transport behind FallbackTransport's fallback and replayDirect must dial
// from the host's own address even when the environment names a proxy.
func TestNewDirectSafeTransportNeverProxies(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "socks5h://192.168.22.206:1080")
	t.Setenv("HTTP_PROXY", "socks5h://192.168.22.206:1080")
	tr := NewDirectSafeTransport()
	if tr.Proxy != nil {
		t.Fatal("NewDirectSafeTransport installed a Proxy func; the escape leg must stay proxy-free")
	}
}

// TestDefaultDirectIsLazy pins the init-order constraint: resolving the direct
// leg at package-init time would fire http.ProxyFromEnvironment's once-cache
// before main() ran viper's .env sync, freezing an empty ambient view for the
// whole process.
func TestDefaultDirectIsLazy(t *testing.T) {
	if defaultDirectTransport != nil {
		t.Fatal("defaultDirectTransport resolved at package init; it must stay nil until defaultDirect() runs post-boot")
	}
	rt := defaultDirect()
	if rt == nil {
		t.Fatal("defaultDirect() returned nil")
	}
}
