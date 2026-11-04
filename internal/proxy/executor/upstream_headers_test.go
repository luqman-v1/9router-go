package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestForwardUpstreamResponseHeaders covers the retry and rate-limit headers
// the client needs to back off sensibly. Without them a throttle arrives as an
// opaque failure and a gateway in front of us cannot decide on our behalf.
func TestForwardUpstreamResponseHeaders(t *testing.T) {
	upstream := http.Header{}
	upstream.Set("Retry-After", "30")
	upstream.Set("X-Should-Retry", "true")
	upstream.Set("Anthropic-Ratelimit-Requests-Remaining", "17")
	upstream.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	upstream.Set("Anthropic-Ratelimit-Unified-Reset", "2026-01-01T00:00:00Z")
	// Not ours to forward: hop-by-hop, auth and content headers stay internal.
	upstream.Set("Set-Cookie", "secret=1")
	upstream.Set("X-Request-Id", "upstream-id")
	upstream.Set("Content-Length", "42")

	rec := httptest.NewRecorder()
	forwardUpstreamResponseHeaders(rec, upstream)

	want := map[string]string{
		"retry-after":                            "30",
		"x-should-retry":                         "true",
		"anthropic-ratelimit-requests-remaining": "17",
		"anthropic-ratelimit-unified-status":     "allowed",
		"anthropic-ratelimit-unified-reset":      "2026-01-01T00:00:00Z",
	}
	for key, value := range want {
		if got := rec.Header().Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	for _, key := range []string{"Set-Cookie", "X-Request-Id", "Content-Length"} {
		if got := rec.Header().Get(key); got != "" {
			t.Errorf("%s should not have been forwarded, got %q", key, got)
		}
	}
}

func TestForwardUpstreamResponseHeaders_NilSafe(t *testing.T) {
	rec := httptest.NewRecorder()
	forwardUpstreamResponseHeaders(nil, http.Header{"Retry-After": {"1"}})
	forwardUpstreamResponseHeaders(rec, nil)
	if rec.Header().Get("Retry-After") != "" {
		t.Errorf("a nil upstream should forward nothing, got %q", rec.Header().Get("Retry-After"))
	}
}
func TestForwardUpstreamResponseHeaders_SlowMode(t *testing.T) {
	rec := httptest.NewRecorder()
	upstream := http.Header{
		"Anthropic-Usage-Limit":                   []string{"slow"},
		"Anthropic-Ratelimit-Unified-Slow-Status": []string{"active"},
		"Anthropic-Ratelimit-Unified-Reset":       []string{"1700000000"},
		"Retry-After":                             []string{"30"},
		"X-Unrelated":                             []string{"ignore"},
	}

	forwardUpstreamResponseHeaders(rec, upstream)

	if got := rec.Header().Get("anthropic-usage-limit"); got != "slow" {
		t.Errorf("anthropic-usage-limit = %q, want slow", got)
	}
	if got := rec.Header().Get("anthropic-ratelimit-unified-slow-status"); got != "active" {
		t.Errorf("slow status = %q, want active", got)
	}
	if got := rec.Header().Get("retry-after"); got != "30" {
		t.Errorf("retry-after = %q, want 30", got)
	}
	if got := rec.Header().Get("x-unrelated"); got != "" {
		t.Errorf("unexpected header forwarded: %q", got)
	}
}
