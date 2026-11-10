//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/proxy/executor"
)

// End-to-end coverage for the closed-free-tier path: the 503 mapping, the
// no-cooldown branch and the status-preserving error, driven through the real
// router against a fake gate that refuses.
//
// These exist because the changelog fragment claims them. An earlier draft of
// the PR described these cases while they lived only in a scratch file, so the
// record said "verified" about tests that were not in the tree; this file is
// what makes that sentence true.

// refusingGate answers every request with the gate's FreeTierError.
func refusingGate(t *testing.T, calls *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func addOpenCodeConnection(t *testing.T, env *Env, id string, up *httptest.Server) {
	t.Helper()
	priority := 1
	data := fmt.Sprintf(`{"apiKey":"public","baseUrl":%q}`, up.URL+"/inference/v1/chat/completions")
	if err := env.Repo.CreateProviderConnectionFull(id, "opencode", "apikey", "OpenCode", &priority, data); err != nil {
		t.Fatalf("create connection %s: %v", id, err)
	}
}

// A refusal survives every attempt and reaches the client as 503 with a retry
// hint — never as the upstream 403 or its wording.
func TestFreeTierRefusal_ReportsUnavailableNotForbidden(t *testing.T) {
	var calls int32
	up := refusingGate(t, &calls)
	env := newEnv(t)
	addOpenCodeConnection(t, env, "conn-closed", up)

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":    "oc/deepseek-v4-flash-free",
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})

	t.Logf("status=%d retry-after=%q attempts=%d", res.Status, res.Header.Get("Retry-After"), atomic.LoadInt32(&calls))

	if res.Status == http.StatusForbidden {
		t.Errorf("client received the upstream 403 instead of a handled status")
	}
	if res.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.Status)
	}
	if got := res.Header.Get("Retry-After"); got == "" {
		t.Errorf("missing Retry-After, so a client cannot tell when to come back")
	}
	if body := string(res.Body); strings.Contains(body, "FreeTierError") {
		t.Errorf("body still carries the gate's error type: %s", truncate(res.Body))
	} else if strings.Contains(body, "can only be used from within OpenCode") {
		t.Errorf("body still quotes the gate's message: %s", truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), "free_tier_unavailable") {
		t.Errorf("body does not name the condition for clients that branch on it: %s", truncate(res.Body))
	}
}

// The refusal is one request's bad luck, not an account fault: a second request
// must still reach the upstream rather than being short-circuited by a cooldown.
//
// The replay is a different matter. Once a replay has itself been refused, the
// gate has said this egress is out, so the following request skips the replay
// (it would only deepen an IP-level penalty) while still dialling its own first
// attempt. That is the distinction this pins: the connection is never locked,
// but the useless replay is not repeated.
func TestFreeTierRefusal_DoesNotCoolTheConnectionDown(t *testing.T) {
	// The cooldown is process-global; another test may have left a live window.
	executor.ResetOpenCodeRetryCooldownForTesting()
	t.Cleanup(executor.ResetOpenCodeRetryCooldownForTesting)
	var calls int32
	up := refusingGate(t, &calls)
	env := newEnv(t)
	addOpenCodeConnection(t, env, "conn-cool", up)

	body := map[string]any{
		"model":    "oc/deepseek-v4-flash-free",
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}
	// The first request spends the full retry budget (an initial attempt plus
	// one replay). The second still dials, but its replay is suppressed by the
	// cooldown the first one armed.
	const firstRequest, secondRequest = 2, 1
	for i := 1; i <= 2; i++ {
		res := env.Post(t, "/v1/chat/completions", body)
		t.Logf("request %d: status=%d upstream_attempts=%d", i, res.Status, atomic.LoadInt32(&calls))
		if res.Status != http.StatusServiceUnavailable {
			t.Errorf("request %d status = %d, want 503", i, res.Status)
		}
	}
	if got := atomic.LoadInt32(&calls); got != firstRequest+secondRequest {
		t.Errorf("upstream saw %d attempts across two requests, want %d — the connection must not be locked (the second request dials), only its replay suppressed",
			got, firstRequest+secondRequest)
	}
}

// A 403 whose body could not be read in full still carries its status, so the
// classifier sees a refusal rather than an opaque 502.
func TestFreeTierRefusal_SurvivesATruncatedErrorBody(t *testing.T) {
	var calls int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		// Declare far more than is written, so the client's read fails.
		w.Header().Set("Content-Length", "9999")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"FreeTierError","message":"closed"}}`))
	}))
	t.Cleanup(up.Close)

	env := newEnv(t)
	addOpenCodeConnection(t, env, "conn-trunc", up)

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":    "oc/deepseek-v4-flash-free",
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	t.Logf("status=%d attempts=%d", res.Status, atomic.LoadInt32(&calls))

	if res.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 — a truncated body must not turn the refusal into a 502", res.Status)
	}
	if strings.Contains(string(res.Body), "FreeTierError") {
		t.Errorf("body leaks the gate's error: %s", truncate(res.Body))
	}
}
