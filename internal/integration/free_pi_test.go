//go:build integration

package integration

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	json "encoding/json/v2"
)

// Free-pi through the real router and a real database.
//
// The unit suites cover the executor in isolation; these prove the wiring a user
// actually routes through: model resolution, the per-account session header, and
// the 409 recovery path against the store that backs leases in production.

// freePiToken builds a JWT-shaped credential with a live `exp`. The executor
// refuses a credential it can see has expired, so a fixture without one would
// never reach upstream — which would make these tests pass for the wrong reason.
func freePiToken(sub string) string {
	claims, _ := json.Marshal(map[string]any{"sub": sub, "exp": 4102444800}) // 2100-01-01
	return "hdr." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

// uniqueFreePiSub returns an account subject unique to this invocation.
//
// The reset cooldown is process-global and keyed by account, which is correct
// behaviour but means two runs that share a subject would interfere: the second
// would legitimately be refused a reset. Deriving a distinct account per
// invocation keeps repeated runs (-count=N) independent without reaching into
// the executor's test seams.
func uniqueFreePiSub(t *testing.T, label string) string {
	t.Helper()
	n := atomic.AddInt32(&freePiSubSeq, 1)
	return fmt.Sprintf("%s-%s-%d", t.Name(), label, n)
}

var freePiSubSeq int32

// addFreePiConnection stores a connection whose baseUrl points at up and whose
// providerSpecificData carries the fields the executor reads. Written directly
// rather than through AddConnection, which only knows apiKey and baseUrl.
func addFreePiConnection(t *testing.T, env *Env, id, token string, up *Upstream, extra map[string]any) {
	t.Helper()

	psd := map[string]any{"authMethod": "device_flow"}
	for k, v := range extra {
		psd[k] = v
	}
	data, err := json.Marshal(map[string]any{
		"apiKey":               token,
		"accessToken":          token,
		"baseUrl":              up.URL + "/v1/chat/completions",
		"providerSpecificData": psd,
	})
	if err != nil {
		t.Fatalf("encode connection data: %v", err)
	}

	priority := 1
	if err := env.Repo.CreateProviderConnectionFull(id, "free-pi", "apikey", "Free Pi "+id, &priority, string(data)); err != nil {
		t.Fatalf("create free-pi connection %s: %v", id, err)
	}
}

// sessionHeaderOf pulls one header off the most recent upstream request.
func sessionHeaderOf(t *testing.T, up *Upstream, name string) string {
	t.Helper()
	return up.Last(t).Header.Get(name)
}

// TestFreePi_ForwardsSessionHeadersToUpstream pins the three headers upstream
// requires. Without x-session-id every completion is rejected, so this is the
// contract that makes the provider work at all.
func TestFreePi_ForwardsSessionHeadersToUpstream(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	// Captured once: the credential is compared byte for byte below, and
	// re-deriving it would compare against a second, independently encoded value.
	token := freePiToken(uniqueFreePiSub(t, "headers"))
	addFreePiConnection(t, env, "conn-freepi", token, upstream, nil)

	res := env.Post(t, "/v1/chat/completions", ChatBody("free-pi/deepseek/deepseek-v4-flash", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, truncate(res.Body))
	}

	got := upstream.Last(t)
	if auth, want := got.Header.Get("Authorization"), "Bearer "+token; auth != want {
		t.Errorf("Authorization = %q, want the connection credential", auth)
	}
	if sid := got.Header.Get("x-session-id"); sid == "" {
		t.Error("x-session-id missing: upstream ties every completion to it")
	}
	if v := got.Header.Get("x-client-version"); v == "" {
		t.Error("x-client-version missing: upstream refuses clients below its published minimum")
	}
	if model := got.Model(t); model != "deepseek/deepseek-v4-flash" {
		t.Errorf("upstream model = %q, want the provider's own id", model)
	}
}

// TestFreePi_SessionIDIsStablePerAccount is the single-session design under real
// wiring: two connections provisioned for the SAME account must present the same
// upstream session. Minting one per connection would consume two of the account's
// four slots for a single user.
func TestFreePi_SessionIDIsStablePerAccount(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	token := freePiToken(uniqueFreePiSub(t, "shared"))

	addFreePiConnection(t, env, "conn-freepi-a", token, upstream, nil)
	addFreePiConnection(t, env, "conn-freepi-b", token, upstream, nil)

	if res := env.Post(t, "/v1/chat/completions", ChatBody("free-pi/deepseek/deepseek-v4-flash", false)); res.Status != http.StatusOK {
		t.Fatalf("first request status = %d: %s", res.Status, truncate(res.Body))
	}
	first := sessionHeaderOf(t, upstream, "x-session-id")

	if res := env.Post(t, "/v1/chat/completions", ChatBody("free-pi/deepseek/deepseek-v4-flash", false)); res.Status != http.StatusOK {
		t.Fatalf("second request status = %d: %s", res.Status, truncate(res.Body))
	}
	second := sessionHeaderOf(t, upstream, "x-session-id")

	if first != second {
		t.Errorf("two connections on one account used different sessions (%q vs %q); "+
			"the account should occupy exactly one upstream session", first, second)
	}
}

// newFreePiConflictUpstream answers 409 `concurrent_session` to the first chat
// call and 200 to every later one, and counts /session/reset calls. The counter
// is returned by pointer so the assertions can read it after the request.
func newFreePiConflictUpstream(t *testing.T, env *Env) (up *Upstream, resets *int32) {
	t.Helper()

	var chatCalls, resetCalls int32
	up = env.NewUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/reset" {
			atomic.AddInt32(&resetCalls, 1)
			JSONResponder(http.StatusOK, `{"released":true}`)(w, r)
			return
		}
		if atomic.AddInt32(&chatCalls, 1) == 1 {
			JSONResponder(http.StatusConflict,
				`{"code":"concurrent_session","message":"You've already got 4 free-pi CLI sessions running."}`)(w, r)
			return
		}
		chatCompletionResponder()(w, r)
	}))
	return up, &resetCalls
}

// TestFreePi_ConflictResetsOnceAndRetries proves the recovery path end to end:
// a 409 from upstream is repaired by POST /session/reset and exactly one retry,
// so the client sees a normal completion instead of an error.
func TestFreePi_ConflictResetsOnceAndRetries(t *testing.T) {
	env := newEnv(t)
	upstream, resets := newFreePiConflictUpstream(t, env)
	addFreePiConnection(t, env, "conn-freepi", freePiToken(uniqueFreePiSub(t, "conflict")), upstream, nil)

	res := env.Post(t, "/v1/chat/completions", ChatBody("free-pi/deepseek/deepseek-v4-flash", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 after recovery: %s", res.Status, truncate(res.Body))
	}
	if got := atomic.LoadInt32(resets); got != 1 {
		t.Errorf("session reset called %d times, want exactly 1", got)
	}
}

// TestFreePi_ConflictSurfacesWhenAutoResetOff: the documented opt-out for anyone
// running free-pi-cli interactively on the same account. The conflict must reach
// the client rather than being silently repaired by ending their CLI session.
func TestFreePi_ConflictSurfacesWhenAutoResetOff(t *testing.T) {
	env := newEnv(t)
	upstream, resets := newFreePiConflictUpstream(t, env)
	addFreePiConnection(t, env, "conn-freepi", freePiToken(uniqueFreePiSub(t, "optout")), upstream,
		map[string]any{"freePiAutoReset": false})

	res := env.Post(t, "/v1/chat/completions", ChatBody("free-pi/deepseek/deepseek-v4-flash", false))

	if res.Status == http.StatusOK {
		t.Fatalf("a 409 was reported as success (status 200): %s", truncate(res.Body))
	}
	if got := atomic.LoadInt32(resets); got != 0 {
		t.Errorf("session reset called %d times, want 0 when auto-reset is off", got)
	}
}

// TestFreePi_AdGateIsReportedVerbatim: when upstream refuses because no ad has
// been shown, the client must see that 403 rather than a generic failure — and
// the gateway must not fabricate an impression to clear it.
func TestFreePi_AdGateIsReportedVerbatim(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, JSONResponder(http.StatusForbidden,
		`{"code":"ads_not_showing","message":"Ads aren't showing for this account."}`))
	addFreePiConnection(t, env, "conn-freepi", freePiToken(uniqueFreePiSub(t, "noads")), upstream, nil)

	res := env.Post(t, "/v1/chat/completions", ChatBody("free-pi/deepseek/deepseek-v4-flash", false))

	if res.Status == http.StatusOK {
		t.Fatalf("the ad gate was reported as success: %s", truncate(res.Body))
	}
	if res.Status != http.StatusForbidden {
		t.Errorf("status = %d, want the upstream 403 to reach the client", res.Status)
	}
	// Nothing may have been sent to an impression endpoint: an impression for an
	// ad nobody saw is a false report to the advertiser whose spend funds the
	// inference.
	for _, r := range upstream.Requests() {
		if r.Path == "/ads/impression" {
			t.Fatalf("an ad impression was posted without an ad being displayed (%s)", r.Path)
		}
	}
}

// TestFreePi_NoImpressionWithoutARender is the honesty invariant as a standing
// assertion rather than a one-off: whatever the upstream says, the gateway never
// posts an ad impression on its own initiative. Rendering is a user action.
func TestFreePi_NoImpressionWithoutARender(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	addFreePiConnection(t, env, "conn-freepi", freePiToken(uniqueFreePiSub(t, "render")), upstream, nil)

	if res := env.Post(t, "/v1/chat/completions", ChatBody("free-pi/deepseek/deepseek-v4-flash", false)); res.Status != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Status, truncate(res.Body))
	}
	for _, r := range upstream.Requests() {
		if r.Path == "/ads/impression" {
			t.Fatalf("an ad impression was posted without an ad being displayed (%s)", r.Path)
		}
	}
}
