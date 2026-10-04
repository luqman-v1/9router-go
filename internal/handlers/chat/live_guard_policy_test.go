package chat

import (
	"net/http"
	"os"
	"testing"
)

// Pins the skip set itself, so the next "not right now" status a free tier
// starts returning is a one-line change in upstreamUnavailable rather than
// another red CI run discovered after the fact.
//
// A real defect status must stay outside it: 400 and 500 mean the gateway got
// something wrong and the test should still fail loudly. 401 is the interesting
// boundary — it is a local profile problem rather than a provider outage, so
// upstreamUnavailable reports "no" and requireLiveOK skips on it separately.
// Widening this set to swallow 401 would hide a credential that silently needs a
// refresh.
func TestUpstreamUnavailable_SkipsOnlyProviderAvailabilityStatuses(t *testing.T) {
	tests := []struct {
		name string
		code int
		want bool
	}{
		{name: "rate limited", code: http.StatusTooManyRequests, want: true},
		{name: "free tier refused", code: http.StatusForbidden, want: true},
		{name: "trial exhausted", code: http.StatusPaymentRequired, want: true},
		{name: "backend overloaded", code: http.StatusServiceUnavailable, want: true},
		{name: "upstream proxy path", code: http.StatusBadGateway, want: true},
		{name: "upstream timed out", code: http.StatusGatewayTimeout, want: true},
		{name: "success", code: http.StatusOK, want: false},
		{name: "bad request is our bug", code: http.StatusBadRequest, want: false},
		{name: "server error is our bug", code: http.StatusInternalServerError, want: false},
		{name: "auth failure is a credential problem, not an outage", code: http.StatusUnauthorized, want: false},
		{name: "not found is our routing bug", code: http.StatusNotFound, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upstreamUnavailable(tt.code); got != tt.want {
				t.Errorf("upstreamUnavailable(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// The opt-in is what keeps CI deterministic, so the accepted spellings are
// pinned: a developer who writes 9ROUTER_LIVE_TESTS=true must not silently get
// a skipped run, and a CI job that forgets the variable must get skips rather
// than a red build at the mercy of someone else's free tier.
func TestLiveUpstreamEnabled_AcceptsOnlyExplicitOptIn(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "TRUE", want: true},
		{value: "yes", want: true},
		{value: "on", want: true},
		{value: " 1 ", want: true},
		{value: "", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "maybe", want: false},
	}

	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			t.Setenv(liveUpstreamEnv, tt.value)
			if got := liveUpstreamEnabled(); got != tt.want {
				t.Errorf("liveUpstreamEnabled() with %s=%q = %v, want %v", liveUpstreamEnv, tt.value, got, tt.want)
			}
		})
	}
}

// TestLiveUpstreamUnsetIsDisabled is the property CI depends on: with the
// variable absent, live tests stay off. CI never sets it, so this is the state
// every run starts from.
func TestLiveUpstreamUnsetIsDisabled(t *testing.T) {
	orig, had := os.LookupEnv(liveUpstreamEnv)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(liveUpstreamEnv, orig)
			return
		}
		_ = os.Unsetenv(liveUpstreamEnv)
	})
	if err := os.Unsetenv(liveUpstreamEnv); err != nil {
		t.Fatalf("unset %s: %v", liveUpstreamEnv, err)
	}
	if liveUpstreamEnabled() {
		t.Errorf("live upstream tests must be off when %s is unset; CI runs with no value", liveUpstreamEnv)
	}
}

// The assertion helpers must stay usable on the success path, so a live run that
// genuinely works is not failed by the guard that protects it.
func TestLiveAssertionHelpers_AcceptRealSuccess(t *testing.T) {
	t.Run("non-stream", func(t *testing.T) {
		requireLiveOK(t, http.StatusOK, `{"choices":[{"message":{"content":"PONG"}}]}`)
	})
	t.Run("stream", func(t *testing.T) {
		requireLiveSSE(t, http.StatusOK, "data: {\"id\":\"1\"}\n\ndata: [DONE]\n\n")
	})
}