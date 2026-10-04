package chat

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// Live tests reach a real provider over the network using the operator's own
// credentials from ~/.9router/db/data.sqlite. Two things follow, and both are
// reasons such a test must never run in CI:
//
//   - The models are free-tier and shared. oc/space-bunny-free and the
//     muse-spark-* contributor tiers are rate limited per IP and are regularly
//     exhausted by other users, so a run that passes on Monday fails on Tuesday
//     for reasons that have nothing to do with this gateway.
//   - The credential is a real one. CI has none, and a test that silently
//     resolved to "no connection configured" would pass while proving nothing.
//
// So the suite is split by an explicit opt-in rather than by a per-status skip.
// Every live test calls requireLiveUpstream, and CI simply does not set the
// variable. A new live test that forgets the guard still skips: the helper sits
// inside getRealUserDB, which every credential-backed one already calls.
//
// Run them locally with:
//
//	make test-live
//
// which is `9ROUTER_LIVE_TESTS=1 go test ./internal/handlers/chat/ -v -run 'Live|MuseSpark'`.

// liveUpstreamEnv is the opt-in variable. The 9ROUTER_ prefix keeps it from
// colliding with a real runtime setting and makes it greppable.
const liveUpstreamEnv = "9ROUTER_LIVE_TESTS"

// liveUpstreamEnabled reports whether live upstream tests were opted into.
func liveUpstreamEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(liveUpstreamEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// requireLiveUpstream skips the calling test unless live tests were enabled.
// It is the gate for every test that opens a connection to a provider.
func requireLiveUpstream(t *testing.T) {
	t.Helper()
	if !liveUpstreamEnabled() {
		t.Skipf("live upstream test: set %s=1 to run it against a real provider", liveUpstreamEnv)
	}
}

// upstreamUnavailable reports whether a status from a real provider means the
// provider is unavailable rather than the gateway being wrong: a free-tier rate
// limit (429), a free-tier refusal (403), an exhausted trial (402), or a
// backend or transport failure (502/503/504).
//
// A test whose outcome depends on a third party's availability has to skip on
// every status that means "not right now", or it fails at the mercy of the
// provider's load. That is not a hypothetical: muse-spark-1.3 returned "Error
// from provider (Console): The backend is temporarily overloaded" and failed CI
// on #82, and oc/space-bunny-free answers 429 whenever the shared free tier is
// busy — which is most of the time.
func upstreamUnavailable(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // free-tier rate limit, shared per IP
		http.StatusForbidden,        // free-tier refusal
		http.StatusPaymentRequired,  // trial exhausted
		http.StatusServiceUnavailable,
		http.StatusBadGateway,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// skipIfUpstreamUnavailable turns a provider-side refusal into a skip carrying
// the response body, so a local live run reports what upstream said instead of
// calling it a gateway defect.
func skipIfUpstreamUnavailable(t *testing.T, code int, body string) {
	t.Helper()
	if upstreamUnavailable(code) {
		t.Skipf("upstream unavailable (%d), not a gateway defect: %s", code, body)
	}
}

// requireLiveOK asserts a live response was a real success, skipping on the
// provider-side statuses a free tier produces and failing on anything else.
// Every skip carries the full body, so a local live run shows what upstream
// actually answered.
func requireLiveOK(t *testing.T, code int, body string) {
	t.Helper()
	skipIfUpstreamUnavailable(t, code, body)
	if code == http.StatusUnauthorized {
		// An expired token is a local profile problem rather than a provider
		// outage, but it is still not a gateway defect, and re-running needs a
		// refresh first.
		t.Skipf("credential rejected (%d), refresh the token in this profile first: %s", code, body)
	}
	if code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", code, body)
	}
}

// requireLiveSSE asserts a streamed live response carried real SSE frames.
func requireLiveSSE(t *testing.T, code int, body string) {
	t.Helper()
	requireLiveOK(t, code, body)
	if !strings.Contains(body, "data:") || !strings.Contains(body, "[DONE]") {
		t.Errorf("expected SSE chunks and [DONE], got: %s", body)
	}
}