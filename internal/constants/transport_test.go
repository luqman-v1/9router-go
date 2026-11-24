package constants

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPTransportConfig_NewTransport(t *testing.T) {
	cfg := HTTPTransportConfig{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       45 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	tr := cfg.NewTransport()
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}

	if tr.MaxIdleConns != 100 {
		t.Errorf("expected MaxIdleConns=100, got %d", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 50 {
		t.Errorf("expected MaxIdleConnsPerHost=50, got %d", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != 45*time.Second {
		t.Errorf("expected IdleConnTimeout=45s, got %v", tr.IdleConnTimeout)
	}
	if tr.TLSHandshakeTimeout != 5*time.Second {
		t.Errorf("expected TLSHandshakeTimeout=5s, got %v", tr.TLSHandshakeTimeout)
	}
	if tr.ExpectContinueTimeout != 2*time.Second {
		t.Errorf("expected ExpectContinueTimeout=2s, got %v", tr.ExpectContinueTimeout)
	}
	if tr.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("expected ResponseHeaderTimeout=30s, got %v", tr.ResponseHeaderTimeout)
	}
}

func TestHTTPTransportConfig_Configure(t *testing.T) {
	tr := &http.Transport{}
	DefaultHTTPTransportConfig.Configure(tr)
	if tr.MaxIdleConns != DefaultHTTPTransportConfig.MaxIdleConns {
		t.Errorf("expected MaxIdleConns=%d, got %d", DefaultHTTPTransportConfig.MaxIdleConns, tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != DefaultHTTPTransportConfig.MaxIdleConnsPerHost {
		t.Errorf("expected MaxIdleConnsPerHost=%d, got %d", DefaultHTTPTransportConfig.MaxIdleConnsPerHost, tr.MaxIdleConnsPerHost)
	}
}

func TestHTTPTransportConfig_Configure_Nil(t *testing.T) {
	// Should not panic on nil transport
	DefaultHTTPTransportConfig.Configure(nil)
}

// TestDefaultIdleConnTimeoutIsCapped pins the keep-alive TTL cap: the idle
// connection pool must not hold dead sockets for 90s (upstream/edge idle
// windows drop idle sockets without a TCP RST; decolua/9router@963787f73).
func TestDefaultIdleConnTimeoutIsCapped(t *testing.T) {
	if got := DefaultHTTPTransportConfig.IdleConnTimeout; got != defaultKeepAliveTimeout {
		t.Errorf("expected IdleConnTimeout=%v (capped keep-alive TTL), got %v", defaultKeepAliveTimeout, got)
	}
}

// TestResolveKeepAliveTimeoutEnvOverride pins the FETCH_KEEPALIVE_TIMEOUT_MS
// override and its fallback on unset/invalid input.
func TestResolveKeepAliveTimeoutEnvOverride(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		t.Setenv("FETCH_KEEPALIVE_TIMEOUT_MS", "2500")
		if got := resolveKeepAliveTimeout(); got != 2500*time.Millisecond {
			t.Errorf("expected 2500ms, got %v", got)
		}
	})
	t.Run("unset falls back to default", func(t *testing.T) {
		t.Setenv("FETCH_KEEPALIVE_TIMEOUT_MS", "")
		if got := resolveKeepAliveTimeout(); got != defaultKeepAliveTimeout {
			t.Errorf("expected default %v, got %v", defaultKeepAliveTimeout, got)
		}
	})
	t.Run("invalid falls back to default", func(t *testing.T) {
		t.Setenv("FETCH_KEEPALIVE_TIMEOUT_MS", "not-a-number")
		if got := resolveKeepAliveTimeout(); got != defaultKeepAliveTimeout {
			t.Errorf("expected default %v, got %v", defaultKeepAliveTimeout, got)
		}
	})
	t.Run("non-positive falls back to default", func(t *testing.T) {
		t.Setenv("FETCH_KEEPALIVE_TIMEOUT_MS", "0")
		if got := resolveKeepAliveTimeout(); got != defaultKeepAliveTimeout {
			t.Errorf("expected default %v, got %v", defaultKeepAliveTimeout, got)
		}
	})
}

// TestResolveHTTP2PingIdleTimeoutEnvOverride pins the probe's opt-in. Zero
// disables the probe entirely, which is the historical behaviour, so an unset
// variable and an explicit "0" must both resolve to zero rather than a default.
func TestResolveHTTP2PingIdleTimeoutEnvOverride(t *testing.T) {
	t.Run("unset means disabled", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_IDLE_TIMEOUT", "")
		if got := resolveHTTP2PingIdleTimeout(); got != 0 {
			t.Errorf("expected 0 (disabled), got %v", got)
		}
	})
	t.Run("explicit zero is also disabled", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_IDLE_TIMEOUT", "0")
		if got := resolveHTTP2PingIdleTimeout(); got != 0 {
			t.Errorf("expected 0 (disabled), got %v", got)
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_IDLE_TIMEOUT", "30")
		if got := resolveHTTP2PingIdleTimeout(); got != 30*time.Second {
			t.Errorf("expected 30s, got %v", got)
		}
	})
	t.Run("invalid falls back to disabled", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_IDLE_TIMEOUT", "not-a-number")
		if got := resolveHTTP2PingIdleTimeout(); got != 0 {
			t.Errorf("expected 0 (disabled), got %v", got)
		}
	})
	t.Run("negative falls back to disabled", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_IDLE_TIMEOUT", "-5")
		if got := resolveHTTP2PingIdleTimeout(); got != 0 {
			t.Errorf("expected 0 (disabled), got %v", got)
		}
	})
}

// TestResolveHTTP2PingTimeoutEnvOverride pins the acknowledgement wait. Its
// default is 15s (upstream's RELAY_HTTP2_PING_TIMEOUT default) and it only
// matters when a probe is configured at all.
func TestResolveHTTP2PingTimeoutEnvOverride(t *testing.T) {
	t.Run("unset falls back to default", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_TIMEOUT", "")
		if got := resolveHTTP2PingTimeout(); got != defaultHTTP2PingTimeout {
			t.Errorf("expected %v, got %v", defaultHTTP2PingTimeout, got)
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_TIMEOUT", "5")
		if got := resolveHTTP2PingTimeout(); got != 5*time.Second {
			t.Errorf("expected 5s, got %v", got)
		}
	})
	t.Run("invalid falls back to default", func(t *testing.T) {
		t.Setenv("RELAY_HTTP2_PING_TIMEOUT", "not-a-number")
		if got := resolveHTTP2PingTimeout(); got != defaultHTTP2PingTimeout {
			t.Errorf("expected %v, got %v", defaultHTTP2PingTimeout, got)
		}
	})
}

// TestConfigureHTTP2Ping proves the setting reaches the transport and that the
// zero case leaves it untouched. The ping is the difference between a dead
// idle connection being retired by the pool and a request hanging on it, so the
// zero case must leave HTTP2 nil rather than install a config with a zero
// interval that reads as "no probe" but confuses an operator reading the field.
func TestConfigureHTTP2Ping(t *testing.T) {
	t.Run("idle timeout set, ping timeout from config", func(t *testing.T) {
		cfg := HTTPTransportConfig{HTTP2PingIdleTimeout: 30 * time.Second, HTTP2PingTimeout: 5 * time.Second}
		tr := cfg.NewTransport()
		if tr.HTTP2 == nil {
			t.Fatal("HTTP2 config nil, want the probe installed")
		}
		if tr.HTTP2.SendPingTimeout != 30*time.Second {
			t.Errorf("SendPingTimeout = %v, want 30s", tr.HTTP2.SendPingTimeout)
		}
		if tr.HTTP2.PingTimeout != 5*time.Second {
			t.Errorf("PingTimeout = %v, want 5s", tr.HTTP2.PingTimeout)
		}
	})
	t.Run("zero idle leaves HTTP2 nil (probe off)", func(t *testing.T) {
		cfg := HTTPTransportConfig{HTTP2PingIdleTimeout: 0, HTTP2PingTimeout: 5 * time.Second}
		tr := cfg.NewTransport()
		if tr.HTTP2 != nil {
			t.Errorf("HTTP2 config = %+v, want nil when the probe is disabled", tr.HTTP2)
		}
	})
	t.Run("zero ping timeout keeps Go's own default", func(t *testing.T) {
		cfg := HTTPTransportConfig{HTTP2PingIdleTimeout: 30 * time.Second, HTTP2PingTimeout: 0}
		tr := cfg.NewTransport()
		if tr.HTTP2 == nil {
			t.Fatal("HTTP2 config nil, want the probe installed")
		}
		if tr.HTTP2.PingTimeout != 0 {
			t.Errorf("PingTimeout = %v, want 0 (Go's 15s default)", tr.HTTP2.PingTimeout)
		}
	})
}

// TestDefaultConfigReachesTheTransport pins the end-to-end path an operator's
// env var takes: DefaultHTTPTransportConfig reads RELAY_HTTP2_PING_IDLE_TIMEOUT
// at package init, and NewSafeTransport — the transport every chat request goes
// through — consumes it. Breaking that chain is silent: the env var would be
// read and discarded, and the operator would see no probe and no error.
func TestDefaultConfigReachesTheTransport(t *testing.T) {
	// The default config was built once at init, from whatever the env said
	// then. A test cannot re-run init, so this asserts the wiring rather than
	// the value: the transport's HTTP2 field must be nil exactly when the
	// config's idle timeout is zero, and carry that timeout when it is not.
	tr := DefaultHTTPTransportConfig.NewTransport()
	if DefaultHTTPTransportConfig.HTTP2PingIdleTimeout == 0 {
		if tr.HTTP2 != nil {
			t.Errorf("config disables the probe but the transport has HTTP2 = %+v", tr.HTTP2)
		}
		return
	}
	if tr.HTTP2 == nil {
		t.Fatal("config sets a probe but the transport has no HTTP2 config")
	}
	if tr.HTTP2.SendPingTimeout != DefaultHTTPTransportConfig.HTTP2PingIdleTimeout {
		t.Errorf("transport SendPingTimeout = %v, want the config's %v",
			tr.HTTP2.SendPingTimeout, DefaultHTTPTransportConfig.HTTP2PingIdleTimeout)
	}
}
