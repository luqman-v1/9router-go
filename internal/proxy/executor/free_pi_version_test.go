package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/providers"
)

// The reported failure: a connection whose stored x-client-version was pinned
// at login, long before the server raised its `min` floor. Every request then
// answered 426 from a container that was already current, because the stored
// pin outranks the compiled-in fallback and nothing ever refreshed it.

// versionStub serves a scripted sequence of /client-version payloads, repeating
// the last one.
func versionStub(t *testing.T, payloads ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n := calls
		calls++
		mu.Unlock()
		if n >= len(payloads) {
			n = len(payloads) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payloads[n]))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(providers.SetFreePiClientVersionEndpointForTesting(srv.URL))
	t.Cleanup(providers.ResetFreePiClientVersionForTesting)
	providers.ResetFreePiClientVersionForTesting()
	return srv
}

// upgradeGateStub answers the first request with 426 and the next with a
// completion, recording the x-client-version each attempt carried.
func upgradeGateStub(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("x-client-version"))
		attempt := len(seen)
		mu.Unlock()

		if attempt == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"error":{"code":"client_outdated","message":"your free-pi-cli is below the required minimum (0.6.16); update with: npx free-pi-cli@latest"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(freePiJSONCompletion))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// A 426 `client_outdated` must recover without a re-login: refresh the published
// floor, lift the header, retry exactly once. The stub refuses the first attempt
// outright rather than modelling a specific cause, so this pins the old gate's
// recovery path.
func TestFreePiForward_426RefreshesTheVersionAndRetriesOnce(t *testing.T) {
	versionStub(t,
		`{"min":"0.6.2","latest":"0.6.10"}`, // the stale snapshot the connection is pinned to
		`{"min":"0.6.16","latest":"0.8"}`,   // what the server publishes now
	)
	upstream, seen := upgradeGateStub(t)

	token := freePiTestToken("user_version_retry")
	req := freePiTestRequest(t, upstream, token, map[string]any{"freePiClientVersion": "0.6.10"}, false)
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi: %v (a 426 must self-heal once)", err)
	}

	versions := seen()
	if len(versions) != 2 {
		t.Fatalf("upstream saw %d attempts (%v), want exactly 2: the refusal plus one retry", len(versions), versions)
	}
	// The stale 0.6.10 pin cannot reach the wire: the first snapshot already
	// publishes 0.6.10 as `latest`, and the 426 refresh then lifts to 0.8.
	if versions[0] != "0.6.10" {
		t.Errorf("first attempt sent %q, want the first published snapshot 0.6.10", versions[0])
	}
	if versions[1] != "0.8" {
		t.Errorf("retry sent %q, want the refreshed 0.8", versions[1])
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after the healed retry", rec.Code)
	}
}

// The reported failure, verbatim: the server publishes min=latest=0.6.28 and
// refuses the gateway's unpublished 0.7 with 426 upgrade_required. The resolver
// must send the published release on BOTH attempts, and the 426 must self-heal
// once rather than being relayed to the client.
func TestFreePiForward_UpgradeRequired426SendsPublishedReleaseAndRetries(t *testing.T) {
	versionStub(t, `{"min":"0.6.28","latest":"0.6.28"}`)

	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("x-client-version"))
		attempt := len(seen)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"code":"upgrade_required","message":"This free-pi-cli version is not an official release. Install the official CLI with: npx free-pi-cli@latest","error":{"message":"This free-pi-cli version is not an official release. Install the official CLI with: npx free-pi-cli@latest","code":"upgrade_required","type":"free_pi_error"}}`))
			return
		}
		_, _ = w.Write([]byte(freePiJSONCompletion))
	}))
	t.Cleanup(upstream.Close)

	token := freePiTestToken("user_upgrade_required")
	req := freePiTestRequest(t, upstream, token, map[string]any{"freePiClientVersion": "0.7"}, false)
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi: %v (a 426 upgrade_required must self-heal once)", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("upstream saw %d attempts (%v), want exactly 2: the refusal plus one retry", len(seen), seen)
	}
	for i, got := range seen {
		if got != "0.6.28" {
			t.Errorf("attempt %d sent %q, want the published release 0.6.28", i+1, got)
		}
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after the healed retry", rec.Code)
	}
}

// When the refresh fails there is no published release to fall back to, so a
// retry would only replay a header the server already refused. The original
// 426 is reported and no second attempt is spent.
func TestFreePiForward_UpgradeRequired426WithoutFeedReportsOriginal(t *testing.T) {
	providers.ResetFreePiClientVersionForTesting()
	t.Cleanup(providers.ResetFreePiClientVersionForTesting)
	t.Cleanup(providers.SetFreePiClientVersionEndpointForTesting("http://127.0.0.1:1/client-version"))

	var mu sync.Mutex
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"code":"upgrade_required","message":"not an official release"}`))
	}))
	defer upstream.Close()

	token := freePiTestToken("user_upgrade_no_feed")
	req := freePiTestRequest(t, upstream, token, nil, false)
	err := ForwardFreePi(httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("a 426 that cannot advance must still be reported")
	}
	if !strings.Contains(err.Error(), "426") {
		t.Errorf("error = %v, want the upstream 426 surfaced", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("upstream attempts = %d, want 1: a failed refresh must not spend a retry", attempts)
	}
}

// A stale pin must not even reach the wire when the published floor is already
// known: resolving at the start of the request is what prevents the 426 rather
// than recovering from it.
func TestFreePiForward_SendsThePublishedVersionWithoutSeeingA426(t *testing.T) {
	versionStub(t, `{"min":"0.6.16","latest":"0.7.2"}`)

	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("x-client-version"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(freePiJSONCompletion))
	}))
	defer upstream.Close()

	token := freePiTestToken("user_version_prevent")
	req := freePiTestRequest(t, upstream, token, map[string]any{"freePiClientVersion": "0.6.10"}, false)

	if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("upstream saw %d attempts, want 1", len(seen))
	}
	if seen[0] != "0.7.2" {
		t.Errorf("sent %q, want the published 0.7.2, not the stale 0.6.10 pin", seen[0])
	}
}

// When the refresh cannot advance past what was already sent, a retry would
// draw the same answer. The original 426 is reported instead of being spent.
func TestFreePiForward_DoesNotRetryWhenTheFloorDoesNotAdvance(t *testing.T) {
	// Both resolves see the same published latest, so the 426 refresh learns
	// nothing new.
	versionStub(t, `{"min":"0.6.16","latest":"0.7"}`)

	var mu sync.Mutex
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"client_outdated"}}`))
	}))
	defer upstream.Close()

	token := freePiTestToken("user_version_same")
	req := freePiTestRequest(t, upstream, token, map[string]any{"freePiClientVersion": "0.7"}, false)

	err := ForwardFreePi(httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("a 426 that cannot advance must still be reported")
	}
	if !strings.Contains(err.Error(), "426") {
		t.Errorf("error = %v, want the upstream 426 surfaced", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("upstream attempts = %d, want 1: an unchanged header must not be replayed", attempts)
	}
}

// A manually configured x-client-version is the operator's explicit answer
// and must win over the live feed — including when the feed publishes a newer
// version. The name is matched case-insensitively because the dashboard's
// override endpoint preserves the operator's spelling.
func TestFreePiForward_ManualClientVersionOverrideWins(t *testing.T) {
	for _, name := range []string{"x-client-version", "X-Client-Version"} {
		t.Run(name, func(t *testing.T) {
			versionStub(t, `{"min":"0.6.28","latest":"0.6.29"}`)

			var mu sync.Mutex
			var seen []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Header.Get("x-client-version"))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(freePiJSONCompletion))
			}))
			t.Cleanup(upstream.Close)

			token := freePiTestToken("user_manual_" + strings.ReplaceAll(name, "-", "_"))
			req := freePiTestRequest(t, upstream, token, map[string]any{"freePiClientVersion": "0.6.10"}, false)
			req.Config.StaticHeaders = map[string]string{name: "0.5.99"}

			if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
				t.Fatalf("ForwardFreePi: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 1 || seen[0] != "0.5.99" {
				t.Fatalf("upstream saw %v, want exactly [0.5.99]: the manual override must win", seen)
			}
		})
	}
}

// With no override configured the automatic resolver stays in charge, so the
// existing self-heal behaviour is unchanged.
func TestFreePiForward_NoManualOverrideKeepsAutomaticResolution(t *testing.T) {
	versionStub(t, `{"min":"0.6.28","latest":"0.6.29"}`)

	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("x-client-version"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(freePiJSONCompletion))
	}))
	t.Cleanup(upstream.Close)

	token := freePiTestToken("user_no_override")
	req := freePiTestRequest(t, upstream, token, map[string]any{"freePiClientVersion": "0.6.10"}, false)

	if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "0.6.29" {
		t.Fatalf("upstream saw %v, want exactly [0.6.29]: the live feed must resolve when no override is set", seen)
	}
}

// A 426 with a manual override must not be papered over by a retry that would
// replay the same header: the operator has to see the upstream verdict.
func TestFreePiForward_ManualOverride426IsNotRetried(t *testing.T) {
	versionStub(t, `{"min":"0.6.28","latest":"0.6.29"}`)

	var mu sync.Mutex
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"code":"upgrade_required","message":"not an official release"}`))
	}))
	t.Cleanup(upstream.Close)

	token := freePiTestToken("user_manual_426")
	req := freePiTestRequest(t, upstream, token, nil, false)
	req.Config.StaticHeaders = map[string]string{"x-client-version": "0.5.99"}

	err := ForwardFreePi(httptest.NewRecorder(), req)
	if err == nil || !strings.Contains(err.Error(), "426") {
		t.Fatalf("error = %v, want the upstream 426 surfaced", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("upstream attempts = %d, want 1: a manual override must not be overridden by a retry", attempts)
	}
}
