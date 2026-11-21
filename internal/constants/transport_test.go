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
