package proxy

import "testing"

// NewSafeTransportForProxy is the one place a caller-supplied proxy URL becomes
// a transport. It installed http.ProxyURL for any URL url.Parse accepted, so an
// undialable scheme produced a transport that looked configured but failed at
// dial time — after the request had already been committed to that proxy.
//
// The dialable set is what Go's transport actually speaks: HTTP CONNECT
// (http/https) and, natively, SOCKS5 (socks5/socks5h).

func TestDialableProxyScheme(t *testing.T) {
	tests := []struct {
		name   string
		scheme string
		want   bool
	}{
		{name: "http", scheme: "http", want: true},
		{name: "https", scheme: "https", want: true},
		{name: "socks5", scheme: "socks5", want: true},
		{name: "socks5h", scheme: "socks5h", want: true},
		{name: "case insensitive", scheme: "SOCKS5H", want: true},
		{name: "whitespace tolerated", scheme: "  socks5 ", want: true},
		{name: "socks4 is not dialable by the stdlib", scheme: "socks4", want: false},
		{name: "quic", scheme: "quic", want: false},
		{name: "empty", scheme: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DialableProxyScheme(tt.scheme); got != tt.want {
				t.Errorf("DialableProxyScheme(%q) = %v, want %v", tt.scheme, got, tt.want)
			}
		})
	}
}

func TestProxyURLDialable(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "http with port", url: "http://proxy.example:8080", want: true},
		{name: "socks5 with credentials", url: "socks5://user:pass@127.0.0.1:1080", want: true},
		{name: "socks5h", url: "socks5h://user:pass@127.0.0.1:1080", want: true},
		{name: "surrounding whitespace", url: "  socks5://127.0.0.1:1080  ", want: true},
		{name: "undialable scheme", url: "quic://127.0.0.1:7777", want: false},
		{name: "no scheme", url: "127.0.0.1:1080", want: false},
		{name: "missing host", url: "socks5://", want: false},
		{name: "empty", url: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProxyURLDialable(tt.url); got != tt.want {
				t.Errorf("ProxyURLDialable(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// The transport either dials the proxy or falls back to the ambient path; it
// must never carry a proxy func the transport cannot speak.
func TestNewSafeTransportForProxySchemeHandling(t *testing.T) {
	tests := []struct {
		name        string
		proxyURL    string
		wantProxied bool
	}{
		{name: "socks5 pool dials the proxy", proxyURL: "socks5://user:pass@127.0.0.1:1080", wantProxied: true},
		{name: "socks5h pool dials the proxy", proxyURL: "socks5h://user:pass@127.0.0.1:1080", wantProxied: true},
		{name: "http pool dials the proxy", proxyURL: "http://proxy.example:8080", wantProxied: true},
		{name: "undialable scheme installs no proxy", proxyURL: "quic://127.0.0.1:7777", wantProxied: false},
		{name: "empty installs no proxy", proxyURL: "", wantProxied: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewSafeTransportForProxy(tt.proxyURL)
			if got := tr.Proxy != nil; got != tt.wantProxied {
				t.Fatalf("Proxy set = %v, want %v", got, tt.wantProxied)
			}
			// Whichever branch ran, the SSRF-filtering dialer must be in place:
			// a transport without it would reach a private target the request
			// path is supposed to block.
			if tr.DialContext == nil {
				t.Error("DialContext must always be the SSRF-filtering dialer")
			}
		})
	}
}
