package proxy

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

func TestResolveDefaultStallTimeout(t *testing.T) {
	// 1. Unset env -> DefaultStallTimeout (6m)
	os.Unsetenv("STREAM_STALL_TIMEOUT_MS")
	os.Unsetenv("NINEROUTER_STREAM_STALL_TIMEOUT_MS")
	if d := ResolveDefaultStallTimeout(); d != DefaultStallTimeout {
		t.Errorf("ResolveDefaultStallTimeout() with unset env = %v, want %v", d, DefaultStallTimeout)
	}

	// 2. STREAM_STALL_TIMEOUT_MS = 15000 -> 15s
	t.Setenv("STREAM_STALL_TIMEOUT_MS", "15000")
	if d := ResolveDefaultStallTimeout(); d != 15*time.Second {
		t.Errorf("ResolveDefaultStallTimeout() = %v, want 15s", d)
	}

	// 3. NINEROUTER_STREAM_STALL_TIMEOUT_MS = 20000 -> 20s
	os.Unsetenv("STREAM_STALL_TIMEOUT_MS")
	t.Setenv("NINEROUTER_STREAM_STALL_TIMEOUT_MS", "20000")
	if d := ResolveDefaultStallTimeout(); d != 20*time.Second {
		t.Errorf("ResolveDefaultStallTimeout() = %v, want 20s", d)
	}

	// 4. Invalid string fallback -> DefaultStallTimeout
	t.Setenv("STREAM_STALL_TIMEOUT_MS", "invalid")
	t.Setenv("NINEROUTER_STREAM_STALL_TIMEOUT_MS", "0")
	if d := ResolveDefaultStallTimeout(); d != DefaultStallTimeout {
		t.Errorf("ResolveDefaultStallTimeout() with invalid env = %v, want %v", d, DefaultStallTimeout)
	}
}

func TestProviderConfigResolveStallTimeout(t *testing.T) {
	// Unset / nil / zero
	var nilCfg *providers.ProviderConfig
	if d := nilCfg.ResolveStallTimeout(5 * time.Second); d != 5*time.Second {
		t.Errorf("nil config ResolveStallTimeout = %v, want 5s", d)
	}

	cfgZero := &providers.ProviderConfig{StallTimeoutMs: 0}
	if d := cfgZero.ResolveStallTimeout(5 * time.Second); d != 5*time.Second {
		t.Errorf("zero StallTimeoutMs ResolveStallTimeout = %v, want 5s", d)
	}

	cfgSet := &providers.ProviderConfig{StallTimeoutMs: 1234}
	if d := cfgSet.ResolveStallTimeout(5 * time.Second); d != 1234*time.Millisecond {
		t.Errorf("set StallTimeoutMs ResolveStallTimeout = %v, want 1234ms", d)
	}
}

func TestStallTimeoutEnvEndToEnd(t *testing.T) {
	t.Setenv("STREAM_STALL_TIMEOUT_MS", "20") // 20ms timeout

	pr, pw := io.Pipe()
	defer pw.Close()

	r := NewStallReaderWithContext(nil, pr, 0, "env-stall-e2e")
	defer r.Close()

	go func() {
		_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	}()

	rec := &mockResponseWriter{}
	err := SSECopy(rec, r, rec, nil)
	if err == nil {
		t.Fatal("expected stalled stream to error")
	}
	if !errors.Is(err, ErrStreamStall) {
		t.Errorf("error = %v, want it to wrap ErrStreamStall", err)
	}

	frame, ok := errorFrame(t, rec.String())
	if !ok {
		t.Fatalf("client got no error frame: %q", rec.String())
	}
	if frame.Error.Code != "gateway_timeout" {
		t.Errorf("code = %q, want gateway_timeout", frame.Error.Code)
	}
}
