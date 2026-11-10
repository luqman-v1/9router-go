//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
func TestFreeTierRefusal_DoesNotCoolTheConnectionDown(t *testing.T) {
	var calls int32
	up := refusingGate(t, &calls)
	env := newEnv(t)
	addOpenCodeConnection(t, env, "conn-cool", up)

	body := map[string]any{
		"model":    "oc/deepseek-v4-flash-free",
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}
	// Each request spends the retry budget: one initial attempt plus one retry.
	const perRequest = 2
	for i := 1; i <= 2; i++ {
		res := env.Post(t, "/v1/chat/completions", body)
		t.Logf("request %d: status=%d upstream_attempts=%d", i, res.Status, atomic.LoadInt32(&calls))
		if res.Status != http.StatusServiceUnavailable {
			t.Errorf("request %d status = %d, want 503", i, res.Status)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2*perRequest {
		t.Errorf("upstream saw %d attempts across two requests, want %d — a refusal must not lock the connection, so the second request has to dial again", got, 2*perRequest)
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
