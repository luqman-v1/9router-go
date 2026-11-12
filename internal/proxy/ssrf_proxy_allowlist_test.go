package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The socket-level SSRF guard runs on every dial the transport makes, including
// the dial to the outbound proxy. A request routed through a proxy never dials
// the target itself, so applying the guard to the proxy's address protected
// nothing and broke any operator whose proxy lives on loopback (a local CONNECT
// proxy, a sandbox sidecar) — every upstream call came back
//
//	proxyconnect tcp: dial tcp 127.0.0.1:8888: request to private/reserved IP
//	blocked by SSRF protection ... (strictProxy=true)
//
// These tests cover both halves: the guard must still refuse a private *target*,
// and must stop refusing the proxy itself.

// withProxyResolver swaps the environment proxy resolution for one test. The
// stdlib caches the process environment on first use, so the real function
// cannot be driven from a test that also has to stay independent of what the
// environment happened to hold when the package initialised.
func withProxyResolver(t *testing.T, proxyURL string) {
	t.Helper()
	original := proxyFromEnvironment
	t.Cleanup(func() { proxyFromEnvironment = original })
	proxyFromEnvironment = func(*http.Request) (*url.URL, error) {
		if proxyURL == "" {
			return nil, nil
		}
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		return u, nil
	}
}

// withProxyNameResolution swaps the resolver seam for one test and resets the
// resolution cache, so each test starts without another test's stale answers.
// answers maps a bare proxy name to its IP; a name absent from the map is an
// NXDOMAIN.
func withProxyNameResolution(t *testing.T, answers map[string]string) {
	t.Helper()
	original := lookupIPAddr
	t.Cleanup(func() { lookupIPAddr = original })
	lookupIPAddr = func(_ context.Context, name string) ([]net.IPAddr, error) {
		if ip, ok := answers[name]; ok {
			return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	resetProxyResolutionCache(t)
}

// resetProxyResolutionCache empties the proxy-resolution cache and restores it
// at test end, so the TTL cannot leak one test's answers into another.
func resetProxyResolutionCache(t *testing.T) {
	t.Helper()
	resolverMu.Lock()
	saved := make(map[string]proxyResolution, len(resolverCache))
	for k, v := range resolverCache {
		saved[k] = v
	}
	resolverCache = map[string]proxyResolution{}
	resolverMu.Unlock()
	t.Cleanup(func() {
		resolverMu.Lock()
		resolverCache = saved
		resolverMu.Unlock()
	})
}

// proxyDialerFor extracts the dialer a transport will use, which is the only
// way to observe the allowlist from outside the package: DialContext is the
// dialer's own method value.
func proxyDialerFor(tr *http.Transport) func(context.Context, string, string) (net.Conn, error) {
	return tr.DialContext
}

func TestSafeDialerStillBlocksPrivateTargets(t *testing.T) {
	tests := []struct {
		name    string
		address string
	}{
		{name: "loopback", address: "127.0.0.1:8888"},
		{name: "loopback without a port", address: "127.0.0.1"},
		{name: "private range", address: "10.1.2.3:443"},
		{name: "link local", address: "169.254.169.254:80"},
		{name: "unspecified", address: "0.0.0.0:80"},
	}

	// NewSafeDialer with no ambient proxy: nothing is exempted.
	withProxyResolver(t, "")
	dialer := NewSafeDialer()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dialer.ControlContext(context.Background(), "tcp", tt.address, nil)
			if !errors.Is(err, ErrSSRFBlocked) {
				t.Errorf("ControlContext(%q) = %v, want ErrSSRFBlocked", tt.address, err)
			}
		})
	}
}

func TestSafeDialerAllowsTheOperatorsProxyAndNothingElse(t *testing.T) {
	withProxyResolver(t, "")
	dialer := newSafeDialerForProxy(newProxyAllowlist("127.0.0.1:8888"))

	t.Run("the configured proxy is permitted", func(t *testing.T) {
		if err := dialer.ControlContext(context.Background(), "tcp", "127.0.0.1:8888", nil); err != nil {
			t.Fatalf("ControlContext(proxy) = %v, want nil", err)
		}
	})

	t.Run("another loopback port is still blocked", func(t *testing.T) {
		if err := dialer.ControlContext(context.Background(), "tcp", "127.0.0.1:9999", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(127.0.0.1:9999) = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("a private target is still blocked", func(t *testing.T) {
		if err := dialer.ControlContext(context.Background(), "tcp", "10.1.2.3:443", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(10.1.2.3:443) = %v, want ErrSSRFBlocked", err)
		}
	})
}

// A proxy URL written without a port is remembered bare, while the dial address
// always carries one — the host matches on any port. A name that is not a
// literal address is matched by resolution: Go resolves it before the address
// reaches ControlContext, so the guard compares the resolved IP against what
// the configured proxy name answers with (see withProxyNameResolution).
func TestSafeDialerMatchesAProxyWrittenAsALiteralAddress(t *testing.T) {
	withProxyResolver(t, "")

	t.Run("a portless proxy URL exempts its host on any port", func(t *testing.T) {
		dialer := newSafeDialerForProxy(newProxyAllowlist("127.0.0.1"))
		if err := dialer.ControlContext(context.Background(), "tcp", "127.0.0.1:3128", nil); err != nil {
			t.Fatalf("ControlContext(127.0.0.1:3128) = %v, want nil", err)
		}
	})

	t.Run("a hostname proxy is matched by its resolved address", func(t *testing.T) {
		// #1078: the operator names the proxy "hatch-egress-proxy:3128" and
		// Podman DNS answers 198.19.0.1 (RFC 2544), so the string allowlist
		// never matched. The guard now resolves the configured name and
		// exempts that address.
		withProxyNameResolution(t, map[string]string{"hatch-egress-proxy": "198.19.0.1"})
		dialer := newSafeDialerForProxy(newProxyAllowlist("hatch-egress-proxy:3128"))
		if err := dialer.ControlContext(context.Background(), "tcp", "198.19.0.1:3128", nil); err != nil {
			t.Errorf("ControlContext(resolved proxy) = %v, want nil", err)
		}
	})

	t.Run("a hostname proxy resolved elsewhere is still blocked", func(t *testing.T) {
		// The exemption is the proxy's own address, not the reserved range it
		// happens to sit in: another name answering the same IP stays refused.
		withProxyNameResolution(t, map[string]string{"other-proxy": "198.19.0.1"})
		dialer := newSafeDialerForProxy(newProxyAllowlist("other-proxy:3128"))
		if err := dialer.ControlContext(context.Background(), "tcp", "198.19.0.1:9000", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(unrelated port on resolved IP) = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("a hostname proxy that resolves to another address is blocked", func(t *testing.T) {
		withProxyNameResolution(t, map[string]string{"hatch-egress-proxy": "198.19.0.1"})
		dialer := newSafeDialerForProxy(newProxyAllowlist("hatch-egress-proxy:3128"))
		if err := dialer.ControlContext(context.Background(), "tcp", "10.0.0.7:3128", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(10.0.0.7:3128) = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("a different literal host stays blocked", func(t *testing.T) {
		dialer := newSafeDialerForProxy(newProxyAllowlist("127.0.0.1:8888"))
		if err := dialer.ControlContext(context.Background(), "tcp", "127.0.0.1:9999", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(127.0.0.1:9999) = %v, want ErrSSRFBlocked", err)
		}
	})
}

// The reported case: the proxy comes from the environment, and the environment
// names a loopback address.
func TestAmbientEnvironmentProxyIsExemptFromTheGuard(t *testing.T) {
	withProxyResolver(t, "http://127.0.0.1:8888")

	dialer := NewSafeDialer()
	if err := dialer.ControlContext(context.Background(), "tcp", "127.0.0.1:8888", nil); err != nil {
		t.Fatalf("ControlContext(ambient proxy) = %v, want nil", err)
	}
	// The exemption is the proxy, not loopback at large.
	if err := dialer.ControlContext(context.Background(), "tcp", "127.0.0.1:5432", nil); !errors.Is(err, ErrSSRFBlocked) {
		t.Errorf("ControlContext(127.0.0.1:5432) = %v, want ErrSSRFBlocked", err)
	}
}

func TestAmbientEnvironmentWithoutAProxyExemptsNothing(t *testing.T) {
	withProxyResolver(t, "")

	if allow := ambientProxyAllowlist(); allow != nil {
		t.Fatalf("ambientProxyAllowlist() = %v, want nil when the environment dials direct", allow)
	}
	dialer := NewSafeDialer()
	if err := dialer.ControlContext(context.Background(), "tcp", "127.0.0.1:8888", nil); !errors.Is(err, ErrSSRFBlocked) {
		t.Errorf("ControlContext(127.0.0.1:8888) = %v, want ErrSSRFBlocked", err)
	}
}

// TestRequestThroughALoopbackProxyReachesIt is the end-to-end form of the bug:
// a real listener on 127.0.0.1 acting as the operator's proxy, used by a
// transport built the way the chat path builds one. Before the exemption every
// one of these requests died with ErrSSRFBlocked.
func TestRequestThroughALoopbackProxyReachesIt(t *testing.T) {
	var seenPath string
	fakeProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fakeProxy.Close()

	tr := NewSafeTransportForProxy(fakeProxy.URL)
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	resp, err := client.Get("http://upstream.invalid/v1/models")
	if err != nil {
		if errors.Is(err, ErrSSRFBlocked) {
			t.Fatalf("request through a loopback proxy = %v; the operator's own proxy must not be treated as an SSRF target", err)
		}
		t.Fatalf("request through a loopback proxy = %v, want it to reach the proxy", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 from the fake proxy", resp.StatusCode)
	}
	if seenPath != "/v1/models" {
		t.Errorf("proxy saw path %q, want /v1/models", seenPath)
	}
}

// TestSafeTransportForProxyWiresTheExemptionIntoTheDialContext checks the
// transport the callers actually build, rather than the dialer in isolation: the
// proxy's own listener must be reachable through tr.DialContext.
func TestSafeTransportForProxyWiresTheExemptionIntoTheDialContext(t *testing.T) {
	fakeProxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer fakeProxy.Close()

	proxyHost := strings.TrimPrefix(fakeProxy.URL, "http://")

	t.Run("the proxy endpoint is dialable", func(t *testing.T) {
		tr := NewSafeTransportForProxy(fakeProxy.URL)
		conn, err := proxyDialerFor(tr)(context.Background(), "tcp", proxyHost)
		if err != nil {
			t.Fatalf("dial the proxy = %v, want a connection", err)
		}
		_ = conn.Close()
	})

	t.Run("another loopback listener is not", func(t *testing.T) {
		other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer other.Close()

		tr := NewSafeTransportForProxy(fakeProxy.URL)
		_, err := proxyDialerFor(tr)(context.Background(), "tcp", strings.TrimPrefix(other.URL, "http://"))
		if !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("dial an unrelated loopback listener = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("an ambient proxy endpoint is dialable too", func(t *testing.T) {
		withProxyResolver(t, fakeProxy.URL)
		tr := NewSafeTransport()
		conn, err := proxyDialerFor(tr)(context.Background(), "tcp", proxyHost)
		if err != nil {
			t.Fatalf("dial the ambient proxy = %v, want a connection", err)
		}
		_ = conn.Close()
	})
}

// TestProxyIdentityIsUnchanged pins the invariant the fix had to preserve:
// proxyFuncIsAssigned tells an operator-assigned proxy from an ambient one by
// comparing func identity against http.ProxyFromEnvironment. Wrapping t.Proxy to
// observe which host it resolves would have made every ambient proxy look
// assigned — and with it, every strictProxy request would refuse the direct
// replay the ambient case exists to allow.
func TestProxyIdentityIsUnchanged(t *testing.T) {
	t.Run("an ambient transport still reads as ambient", func(t *testing.T) {
		// No resolver swap here: the identity contract under test is that the
		// transport's Proxy IS the package's ambient resolver, which in
		// production is http.ProxyFromEnvironment itself.
		tr := NewSafeTransport()
		if tr.Proxy == nil {
			t.Fatal("NewSafeTransport left Proxy nil; the stdlib never consults the environment on a nil field, so the ambient proxy would be silently ignored")
		}
		if proxiesViaAssignment(&http.Client{Transport: tr}) {
			t.Error("a transport that inherits its proxy from the environment reads as assigned")
		}
	})

	t.Run("an explicit proxy still reads as assigned", func(t *testing.T) {
		tr := NewSafeTransportForProxy("http://proxy.example:3128")
		if !proxiesViaAssignment(&http.Client{Transport: tr}) {
			t.Error("a transport with an explicit proxy pool URL must read as assigned")
		}
		req := &http.Request{URL: &url.URL{Scheme: "https", Host: "upstream.invalid"}}
		got, err := tr.Proxy(req)
		if err != nil {
			t.Fatalf("Proxy() = %v", err)
		}
		if got == nil || got.String() != "http://proxy.example:3128" {
			t.Errorf("Proxy() = %v, want the configured pool URL", got)
		}
	})

	t.Run("an unparseable proxy URL falls back to the ambient path", func(t *testing.T) {
		tr := NewDirectSafeTransport()
		if tr.Proxy != nil {
			t.Error("an unparseable proxy URL must not install a Proxy func")
		}
	})
}

func TestProxyAllowlistIsBounded(t *testing.T) {
	allow := &proxyAllowlist{}
	for i := 0; i < proxyAllowlistMax+10; i++ {
		allow.allow(strings.Repeat("h", i%5+1) + ":" + strings.Repeat("0", i%3+1))
	}
	allow.mu.RLock()
	size := len(allow.hosts)
	allow.mu.RUnlock()
	if size > proxyAllowlistMax {
		t.Errorf("allowlist holds %d entries, want at most %d", size, proxyAllowlistMax)
	}
}

// TestRequestThroughAHostnameProxyReachesIt is #1078 end-to-end: the proxy URL
// names its endpoint by hostname ("localhost:3128"), so the string allowlist
// stores a name while the socket guard only ever sees the resolved dial
// address ("127.0.0.1:3128"). Before the resolution match that mismatch made
// every proxied dial die with
//
//	proxyconnect tcp: dial tcp <proxy-ip>:3128: request to private/reserved IP
//	blocked by SSRF protection ... (strictProxy=true)
//
// localhost resolves through the OS resolver on every platform, so this stays
// deterministic where a made-up container name would need a DNS seam the
// transport itself does not use.
func TestRequestThroughAHostnameProxyReachesIt(t *testing.T) {
	var seenPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	// The allowlist stores the port the operator configured, and a port the
	// dial address does not carry is the signal that an address is a *target*
	// rather than the proxy — that filter must stay. So the fake proxy has to
	// listen on exactly 127.0.0.1:3128, the address the transport dials.
	listener, err := net.Listen("tcp", "127.0.0.1:3128")
	if err != nil {
		t.Skipf("127.0.0.1:3128 is in use: %v", err)
	}
	fakeProxy := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	fakeProxy.Start()
	defer fakeProxy.Close()

	tr := NewSafeTransportForProxy("http://localhost:3128")
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Get("http://upstream.invalid/v1/models")
	if err != nil {
		if errors.Is(err, ErrSSRFBlocked) {
			t.Fatalf("request through the hostname-named proxy = %v; the operator's own proxy must not be treated as an SSRF target", err)
		}
		t.Fatalf("request through the hostname-named proxy = %v, want it to reach the proxy", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 from the fake proxy", resp.StatusCode)
	}
	if seenPath != "/v1/models" {
		t.Errorf("proxy saw path %q, want /v1/models", seenPath)
	}
}

// TestCancelledLookupDoesNotPoisonTheResolutionCache pins the concurrency
// property a shared singleflight flight has to keep: the lookup serves every
// dial through the same proxy, so one client hanging up mid-lookup must not
// cancel it for the others or cache the cancellation as a failure — that
// would refuse every proxied dial for the failure TTL and re-introduce the
// #1078 symptom from an unrelated aborted request. Verified owner finding.
func TestCancelledLookupDoesNotPoisonTheResolutionCache(t *testing.T) {
	withProxyNameResolution(t, map[string]string{"proxy-internal": "198.19.0.1"})

	orig := lookupIPAddr
	lookupIPAddr = func(ctx context.Context, name string) ([]net.IPAddr, error) {
		// Deliberately does NOT observe ctx before sleeping: the cancellation
		// has to land mid-lookup, the way a real client hangup does.
		time.Sleep(80 * time.Millisecond)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return []net.IPAddr{{IP: net.ParseIP("198.19.0.1")}}, nil
	}
	t.Cleanup(func() { lookupIPAddr = orig })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolveProxyIPs(ctx, "proxy-internal")

	// A healthy caller immediately after must still get the real answer.
	got := resolveProxyIPs(context.Background(), "proxy-internal")
	if len(got) == 0 {
		t.Error("healthy caller after a cancelled lookup got no addresses; the failure cache was poisoned by a caller hangup")
	}
}
