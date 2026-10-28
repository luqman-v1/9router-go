package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/netguard"
)

var (
	// ErrSSRFBlocked is returned when an outbound dial attempts to reach a private,
	// loopback, link-local, or cloud metadata IP address without authorization.
	ErrSSRFBlocked = errors.New("request to private/reserved IP blocked by SSRF protection")
)

type allowLoopbackKey struct{}

// WithAllowLoopback returns a child context that permits connections to loopback
// addresses (127.0.0.1, ::1, localhost). Used for local-by-design providers
// like ollama-local.
func WithAllowLoopback(ctx context.Context) context.Context {
	return context.WithValue(ctx, allowLoopbackKey{}, true)
}

// IsLoopbackAllowed reports whether loopback connections are permitted in the context.
func IsLoopbackAllowed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(allowLoopbackKey{}).(bool)
	return v
}

// IsPrivateIP reports whether an IP address is private, loopback, link-local,
// cloud metadata, carrier-grade-NAT, or reserved.
//
// The predicate lives in internal/netguard so that every layer which has to
// answer this question can answer it the same way without an import cycle —
// internal/proxy already imports internal/translator. The copy that lived here
// checked only net.IP's stdlib predicates and so missed 100.64.0.0/10, which
// puts Alibaba Cloud's metadata endpoint (100.100.100.200) inside a range the
// guard called public.
//
// A caller that legitimately targets a private address — an ollama on loopback,
// a provider behind a Tailscale overlay — is served by WithAllowLoopback and
// ALLOW_PRIVATE_NETWORKING, which short-circuit the guard rather than needing
// it to be lenient.
func IsPrivateIP(ip net.IP) bool {
	return netguard.IsPrivateOrReserved(ip)
}

// IsPrivateNetworkingAllowed reports whether connections to private/local IPs
// are globally permitted via ALLOW_PRIVATE_NETWORKING=true/1.
func IsPrivateNetworkingAllowed() bool {
	v := strings.TrimSpace(os.Getenv("ALLOW_PRIVATE_NETWORKING"))
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return false
}

// NewSafeDialer returns a *net.Dialer equipped with an active socket ControlContext
// hook that aborts connection attempts to private or reserved IP addresses after
// DNS resolution (mitigating DNS rebinding).
func NewSafeDialer() *net.Dialer {
	return newSafeDialerForProxy(ambientProxyAllowlist())
}

// proxyAllowlist holds the outbound-proxy endpoints a transport is allowed to
// dial even though they resolve to a private address.
//
// The socket-level guard exists to stop a *request* from reaching an internal
// address. A request routed through an outbound proxy never dials the target at
// all — the proxy does — so applying the guard to the proxy's own address
// protects nothing and breaks the operator's egress path instead. That is the
// reported failure: an operator running the proxy on loopback (a local CONNECT
// proxy for DNS, a sandbox sidecar, the common Termux setup) had every upstream
// call answered with
//
//	proxyconnect tcp: dial tcp 127.0.0.1:8888: request to private/reserved IP
//	blocked by SSRF protection ... (strictProxy=true)
//
// The set is bounded because a connection pool can rotate endpoints; past the
// bound the first entries stay (they are the ones a pool reuses).
type proxyAllowlist struct {
	mu    sync.RWMutex
	hosts map[string]struct{}
}

// proxyAllowlistMax bounds the remembered proxy endpoints.
const proxyAllowlistMax = 64

func newProxyAllowlist(hosts ...string) *proxyAllowlist {
	a := &proxyAllowlist{}
	for _, h := range hosts {
		a.allow(h)
	}
	return a
}

// allow records hostPort ("127.0.0.1:8888", "proxy.example") as a permitted
// dial target.
//
// Matching happens in allows, on the dial address Go hands ControlContext —
// which is a resolved "ip:port", never a name. A literal address therefore
// matches exactly, and a proxy URL written without a port exempts its host on
// any port (the operator named that host as their proxy; the port is implied by
// the scheme). A proxy named by *hostname* cannot be compared there without a
// DNS lookup in the dial path, and is not matched — the reported case, and every
// local proxy, is written as a literal address, while a hostname proxy that
// resolves to a public IP is never blocked to begin with.
func (a *proxyAllowlist) allow(hostPort string) {
	if a == nil || hostPort == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hosts == nil {
		a.hosts = make(map[string]struct{}, 4)
	}
	if _, ok := a.hosts[hostPort]; ok {
		return
	}
	if len(a.hosts) >= proxyAllowlistMax {
		return
	}
	a.hosts[hostPort] = struct{}{}
}

// allows reports whether address matches a remembered proxy endpoint. address is
// the dial target as the ControlContext receives it ("host:port"); a proxy URL
// without an explicit port is remembered bare, so the bare host is tried too.
func (a *proxyAllowlist) allows(address, host string) bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.hosts) == 0 {
		return false
	}
	if _, ok := a.hosts[address]; ok {
		return true
	}
	_, ok := a.hosts[host]
	return ok
}

// proxyFromEnvironment is http.ProxyFromEnvironment, indirect so a test can
// supply a proxy without mutating the process environment: the stdlib caches
// the environment on first use for the life of the process, which would make an
// env-driven test depend on whether an earlier call had already frozen it.
// The transports themselves keep using the stdlib function directly — see
// proxyFuncIsAssigned, which tells an assigned proxy from an ambient one by that
// function's identity.
var proxyFromEnvironment = http.ProxyFromEnvironment

// ambientProxyURL reports the proxy the environment installs for outbound
// requests, or nil when the environment asks for a direct dial. no_proxy is
// resolved for a public host: an environment that only excludes internal
// targets still proxies this one.
func ambientProxyURL() *url.URL {
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "proxy-target.invalid"}}
	u, err := proxyFromEnvironment(req)
	if err != nil || u == nil || u.Host == "" {
		return nil
	}
	return u
}

// ambientProxyAllowlist permits the environment's own proxy endpoint.
func ambientProxyAllowlist() *proxyAllowlist {
	if u := ambientProxyURL(); u != nil {
		return newProxyAllowlist(u.Host)
	}
	return nil
}

// newSafeDialerForProxy returns NewSafeDialer's dialer with the given proxy
// endpoints exempted from the private-IP guard. A nil allowlist behaves exactly
// like NewSafeDialer.
func newSafeDialerForProxy(allow *proxyAllowlist) *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		ControlContext: func(ctx context.Context, network, address string, c syscall.RawConn) error {
			if IsPrivateNetworkingAllowed() {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			// The operator's own egress proxy is not the SSRF surface: the
			// request never reaches an internal target through it that the
			// request-scoped checks above have not already judged.
			if allow.allows(address, host) {
				return nil
			}
			// netguard.ParseIP strips an IPv6 zone. net.ParseIP answers nil
			// for "::1%lo", and the nil branch below is "not an address, let
			// it through" — so a zoned literal dialled a loopback or metadata
			// target this hook exists to refuse.
			ip := netguard.ParseIP(host)
			if ip != nil {
				if ip.IsLoopback() && IsLoopbackAllowed(ctx) {
					return nil
				}
				if IsPrivateIP(ip) {
					return ErrSSRFBlocked
				}
			}
			return nil
		},
	}
}

// NewSafeTransport returns an *http.Transport configured with production pool
// defaults and an active SSRF-filtering DialContext. t.Proxy is left nil so the
// stdlib applies http.ProxyFromEnvironment, and the dialer permits whatever
// endpoint that environment names.
func NewSafeTransport() *http.Transport {
	return NewSafeTransportForProxy("")
}

// NewSafeTransportForProxy returns NewSafeTransport's transport for a caller
// that installs its own proxy: the proxy is set on the transport and its
// endpoint is exempted from the private-IP guard. An empty or unparseable
// proxyURL falls back to the ambient path, so a malformed pool URL cannot
// silently turn into a direct dial.
func NewSafeTransportForProxy(proxyURL string) *http.Transport {
	t := constants.DefaultHTTPTransportConfig.NewTransport()
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil && u.Host != "" {
			t.Proxy = http.ProxyURL(u)
			t.DialContext = newSafeDialerForProxy(newProxyAllowlist(u.Host)).DialContext
			return t
		}
	}
	t.DialContext = newSafeDialerForProxy(ambientProxyAllowlist()).DialContext
	return t
}
