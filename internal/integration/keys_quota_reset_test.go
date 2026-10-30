//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"
)

// TestQuotaResetReopensAnExhaustedKey drives issue #365's reset through the
// real router: a key over its token budget is refused, POST reset-quota clears
// it, and the same key is served again on the very next request.
//
// The refusal has to happen before dispatch, so this also pins that an
// exhausted key never reaches the provider — the whole point of a budget.
func TestQuotaResetReopensAnExhaustedKey(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "quota-reset")

	// A budget of one token: any real completion blows straight through it.
	if put := env.Put(t, "/api/keys/"+id, map[string]any{"tokenQuota": 1}); put.Status != http.StatusOK {
		t.Fatalf("set token quota: status %d: %s", put.Status, truncate(put.Body))
	}

	first := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if first.Status != http.StatusOK {
		t.Fatalf("first request = %d, want 200 (body: %s)", first.Status, truncate(first.Body))
	}
	dispatched := upstream.Count()

	// Usage is recorded after the response is written, so wait for the spend to
	// become visible instead of racing the usage writer and failing for the
	// wrong reason.
	used := waitForKeyUsage(t, env, id)
	if used <= 1 {
		t.Fatalf("recorded spend = %d, want more than the 1-token budget", used)
	}

	rejected := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if rejected.Status != http.StatusTooManyRequests {
		t.Fatalf("exhausted key = %d, want 429 (body: %s)", rejected.Status, truncate(rejected.Body))
	}
	if got := rejected.Header.Get("X-Token-Quota-Limit"); got != "1" {
		t.Errorf("X-Token-Quota-Limit = %q, want the configured 1", got)
	}
	if got := rejected.Header.Get("X-Token-Quota-Used"); got == "" {
		t.Error("X-Token-Quota-Used is missing: a refusal must say how much was spent")
	}
	if upstream.Count() != dispatched {
		t.Errorf("the refused request reached the provider (%d -> %d dispatches)", dispatched, upstream.Count())
	}

	reset := env.Post(t, "/api/keys/"+id+"/reset-quota", nil)
	if reset.Status != http.StatusOK {
		t.Fatalf("reset-quota = %d, want 200 (body: %s)", reset.Status, truncate(reset.Body))
	}
	var out struct {
		Status       string `json:"status"`
		UsedTokens   int    `json:"usedTokens"`
		TokenQuota   int    `json:"tokenQuota"`
		QuotaResetID int    `json:"quotaResetId"`
	}
	reset.Decode(t, &out)
	if out.Status != "ok" {
		t.Errorf("reset status = %q, want ok", out.Status)
	}
	if out.UsedTokens != 0 {
		t.Errorf("usedTokens after reset = %d, want 0", out.UsedTokens)
	}
	if out.QuotaResetID <= 0 {
		t.Errorf("quotaResetId = %d, want the usageHistory mark the reset recorded", out.QuotaResetID)
	}
	if out.TokenQuota != 1 {
		t.Errorf("tokenQuota after reset = %d, want the configured 1 kept — a reset must not clear the budget", out.TokenQuota)
	}

	// The dashboard's own listing must agree with the refusal: one reader, not
	// two that can drift.
	if listed := keyUsageFromList(t, env, id); listed != 0 {
		t.Errorf("GET /api/keys usedTokens = %d after reset, want 0", listed)
	}

	again := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if again.Status != http.StatusOK {
		t.Fatalf("after reset = %d, want 200 (body: %s)", again.Status, truncate(again.Body))
	}
}

// TestQuotaResetRejectsAnUnknownKey pins the endpoint's answer for a key that is
// not there: a 200 would hide a stale dashboard row behind a no-op.
func TestQuotaResetRejectsAnUnknownKey(t *testing.T) {
	env, _ := newProviderEnv(t)

	res := env.Post(t, "/api/keys/does-not-exist/reset-quota", nil)
	if res.Status != http.StatusNotFound {
		t.Fatalf("reset-quota for an unknown key = %d, want 404 (body: %s)", res.Status, truncate(res.Body))
	}
}

// waitForKeyUsage polls the key listing until the key reports a spend, because
// usageHistory is written after the response was sent. It is bounded so a wiring
// failure fails fast instead of hanging, and it returns the last value it saw.
func waitForKeyUsage(t *testing.T, env *Env, id string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last int
	for time.Now().Before(deadline) {
		last = keyUsageFromList(t, env, id)
		if last > 0 {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// keyUsageFromList reads one key's reported spend from GET /api/keys.
func keyUsageFromList(t *testing.T, env *Env, id string) int {
	t.Helper()
	res := env.Get(t, "/api/keys")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/keys = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var rows []struct {
		ID         string `json:"id"`
		UsedTokens int    `json:"usedTokens"`
		TokenQuota int    `json:"tokenQuota"`
	}
	res.Decode(t, &rows)
	for _, row := range rows {
		if row.ID == id {
			return row.UsedTokens
		}
	}
	t.Fatalf("key %s is missing from GET /api/keys", id)
	return 0
}
