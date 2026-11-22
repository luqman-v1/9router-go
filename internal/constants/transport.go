package constants

import (
	"net/http"
	"os"
	"strconv"
	"time"
)

// HTTPTransportConfig encapsulates connection pooling and timeout configurations
// for upstream HTTP transports in reverse proxying and streaming workloads.
type HTTPTransportConfig struct {
	// MaxIdleConns controls the maximum number of idle (keep-alive) connections across all hosts.
	MaxIdleConns int

	// MaxIdleConnsPerHost controls the maximum idle (keep-alive) connections to keep per-host.
	// Go standard library defaults this to 2, which causes connection thrashing under high concurrency.
	MaxIdleConnsPerHost int

	// IdleConnTimeout is the maximum amount of time an idle (keep-alive) connection will remain idle before closing itself.
	IdleConnTimeout time.Duration

	// TLSHandshakeTimeout specifies the maximum amount of time waiting to wait for a TLS handshake.
	TLSHandshakeTimeout time.Duration

	// ExpectContinueTimeout specifies the amount of time to wait for a server's first response headers
	// after fully writing the request headers if the request has an "Expect: 100-continue" header.
	ExpectContinueTimeout time.Duration

	// ResponseHeaderTimeout specifies the amount of time to wait for a server's response headers after fully writing the request.
	ResponseHeaderTimeout time.Duration
}

// defaultKeepAliveTimeout caps how long an idle keep-alive socket may be held in
// the pool. Upstream/edge idle windows (Cloudflare, AWS Global Accelerator /
// NVIDIA NIM) drop idle sockets without a TCP RST; a pool that outlives that
// window hands the next request a poisoned socket. Matches upstream
// decolua/9router@963787f73 (DEFAULT_KEEPALIVE_TIMEOUT_MS).
const defaultKeepAliveTimeout = 4 * time.Second

// resolveKeepAliveTimeout reads FETCH_KEEPALIVE_TIMEOUT_MS (milliseconds) and
// caps the idle-connection TTL at that value; an unset, empty or invalid value
// falls back to defaultKeepAliveTimeout. Matches upstream's envMs override.
func resolveKeepAliveTimeout() time.Duration {
	if raw := os.Getenv("FETCH_KEEPALIVE_TIMEOUT_MS"); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultKeepAliveTimeout
}

// DefaultHTTPTransportConfig defines the high-throughput production defaults for
// upstream LLM reverse-proxy connections.
var DefaultHTTPTransportConfig = HTTPTransportConfig{
	MaxIdleConns:          256,
	MaxIdleConnsPerHost:   128,
	IdleConnTimeout:       resolveKeepAliveTimeout(),
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 2 * time.Minute,
}

// Configure applies the connection pool and timeout settings to an existing *http.Transport.
func (c HTTPTransportConfig) Configure(t *http.Transport) {
	if t == nil {
		return
	}
	t.ForceAttemptHTTP2 = true
	t.MaxIdleConns = c.MaxIdleConns
	t.MaxIdleConnsPerHost = c.MaxIdleConnsPerHost
	t.IdleConnTimeout = c.IdleConnTimeout
	t.TLSHandshakeTimeout = c.TLSHandshakeTimeout
	t.ExpectContinueTimeout = c.ExpectContinueTimeout
	t.ResponseHeaderTimeout = c.ResponseHeaderTimeout
}

// NewTransport instantiates a fresh *http.Transport with these settings applied and Proxy: nil.
func (c HTTPTransportConfig) NewTransport() *http.Transport {
	t := &http.Transport{
		Proxy: nil,
	}
	c.Configure(t)
	return t
}
