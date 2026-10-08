//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// limitKey mints a client key and returns its id and plaintext.
func limitKey(t *testing.T, env *Env, name string) (id, plaintext string) {
	t.Helper()
	res := env.Post(t, "/api/keys", map[string]any{"key": "sk-" + name, "name": name})
	if res.Status != http.StatusOK {
		t.Fatalf("create key: status %d: %s", res.Status, truncate(res.Body))
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	res.Decode(t, &out)
	return out.ID, out.Key
}

// TestRateLimiterRejectsThroughRouter is the only test that proves the limiter
// is actually reachable in production wiring. Every other 429 in this suite is
// an *upstream* refusing the gateway, which exercises the fallback path and
// nothing about the limiter: a limiter mounted in the wrong group, or behind
// the wrong middleware, would pass all of them.
func TestRateLimiterRejectsThroughRouter(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "limited")
	if put := env.Put(t, "/api/keys/"+id, map[string]any{"rateLimitRpm": 2}); put.Status != http.StatusOK {
		t.Fatalf("set rate limit: status %d: %s", put.Status, truncate(put.Body))
	}

	// Two requests fit inside the window; the third is refused.
	for i := range 2 {
		res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
		if res.Status != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 (body: %s)", i+1, res.Status, truncate(res.Body))
		}
	}
	rejected := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if rejected.Status != http.StatusTooManyRequests {
		t.Fatalf("request 3 = %d, want 429 (body: %s)", rejected.Status, truncate(rejected.Body))
	}

	// The refusal must not have reached the provider: the whole point of the
	// limiter is that the upstream never sees the request.
	if upstream.Count() != 2 {
		t.Errorf("upstream saw %d requests, want 2 — a rate-limited request must never be dispatched", upstream.Count())
	}
	if msg := rejected.ErrorMessage(t); msg == "" {
		t.Error("429 carried no error envelope")
	}
}

// TestRateLimitRejectionCarriesRetryHeaders pins the headers a client needs to
// back off without guessing. Retry-After is the contract; the X-RateLimit-*
// names say which axis refused, because a key can be bounded on three at once.
func TestRateLimitRejectionCarriesRetryHeaders(t *testing.T) {
	env, _ := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "headers")
	if put := env.Put(t, "/api/keys/"+id, map[string]any{"rateLimitRpm": 1}); put.Status != http.StatusOK {
		t.Fatalf("set rate limit: status %d", put.Status)
	}

	env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", res.Status)
	}

	for _, header := range []string{"Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if res.Header.Get(header) == "" {
			t.Errorf("429 is missing %s", header)
		}
	}
	if got := res.Header.Get("X-RateLimit-Limit"); got != "1" {
		t.Errorf("X-RateLimit-Limit = %q, want the configured 1", got)
	}
	// The message must name the wait, not send the client back to a guess.
	if msg := res.ErrorMessage(t); !strings.Contains(msg, "Retry after") {
		t.Errorf("429 message %q does not state the retry delay", msg)
	}
}

// TestGlobalRateLimitDefaultAppliesToUnsetKeys is the settings tier the port
// plan asks for: a key with no limit of its own inherits the operator's
// default, so an operator can bound every key without touching each row.
func TestGlobalRateLimitDefaultAppliesToUnsetKeys(t *testing.T) {
	env, _ := newProviderEnv(t)
	if err := env.Repo.SetRateLimitDefaults(db.RateLimitDefaults{
		Enabled: true, RPM: 1, WindowSeconds: 60,
	}); err != nil {
		t.Fatalf("set defaults: %v", err)
	}

	// The seeded key has no rate-limit columns, so only the default can refuse.
	first := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if first.Status != http.StatusOK {
		t.Fatalf("request 1 = %d, want 200", first.Status)
	}
	second := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if second.Status != http.StatusTooManyRequests {
		t.Errorf("request 2 = %d, want 429 — the global default did not apply", second.Status)
	}
}

// TestKeyLimitOverridesGlobalDefault is the precedence rule that makes a global
// default safe to set: it fills only the columns a key left at 0, so a key
// deliberately given a higher budget is never silently capped.
func TestKeyLimitOverridesGlobalDefault(t *testing.T) {
	env, _ := newProviderEnv(t)
	if err := env.Repo.SetRateLimitDefaults(db.RateLimitDefaults{
		Enabled: true, RPM: 1, WindowSeconds: 60,
	}); err != nil {
		t.Fatalf("set defaults: %v", err)
	}
	id, plaintext := limitKey(t, env, "override")
	if put := env.Put(t, "/api/keys/"+id, map[string]any{"rateLimitRpm": 5}); put.Status != http.StatusOK {
		t.Fatalf("set key limit: status %d", put.Status)
	}

	for i := range 3 {
		res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
		if res.Status != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 — the key's own limit must win over the default", i+1, res.Status)
		}
	}
}

// TestRateLimitDisabledByDefault is the backward-compatibility guarantee: an
// install that configured nothing enforces nothing.
func TestRateLimitDisabledByDefault(t *testing.T) {
	env, _ := newProviderEnv(t)

	// Ten requests with no limits configured anywhere. A limiter that refused
	// any of them would break every existing install on upgrade.
	for i := range 10 {
		res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
		if res.Status != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 with nothing configured", i+1, res.Status)
		}
	}
}

// TestMetricsEndpointRequiresAuth guards the leak the port plan calls out:
// labels name providers and models, so an unauthenticated scrape hands a
// prober the whole topology.
func TestMetricsEndpointRequiresAuth(t *testing.T) {
	env := newEnv(t)

	res := env.Get(t, "/api/metrics", WithoutAPIKey())
	if res.Status != http.StatusUnauthorized {
		t.Errorf("anonymous /api/metrics = %d, want 401", res.Status)
	}
}

// TestMetricsCountsServedRequests proves the endpoint is wired to the live
// request path rather than serving an empty registry behind a correct auth
// check.
func TestMetricsCountsServedRequests(t *testing.T) {
	env, _ := newProviderEnv(t)

	if res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false)); res.Status != http.StatusOK {
		t.Fatalf("chat: status %d", res.Status)
	}

	res := env.Get(t, "/api/metrics")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/metrics = %d, want 200", res.Status)
	}
	body := string(res.Body)
	if !strings.Contains(body, "router_requests_total") {
		t.Fatalf("metrics output does not expose router_requests_total: %s", truncate(res.Body))
	}
	if !strings.Contains(body, `provider="deepseek"`) {
		t.Errorf("a served request did not reach the counter: %s", truncate(res.Body))
	}
}

// TestMetricsCountsRateLimitRejections is the collector that shipped unwired:
// the field, the registration, and the helper all existed with no call site, so
// the series was permanently zero.
func TestMetricsCountsRateLimitRejections(t *testing.T) {
	env, _ := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "metric-limited")
	if put := env.Put(t, "/api/keys/"+id, map[string]any{"rateLimitRpm": 1}); put.Status != http.StatusOK {
		t.Fatalf("set rate limit: status %d", put.Status)
	}

	env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))

	res := env.Get(t, "/api/metrics")
	body := string(res.Body)
	if !strings.Contains(body, "router_rate_limit_rejects_total") {
		t.Fatalf("metrics do not expose router_rate_limit_rejects_total: %s", truncate(res.Body))
	}
	if !strings.Contains(body, `scope="rpm"`) {
		t.Errorf("the rejection was not counted: %s", truncate(res.Body))
	}
}

// TestMetricsCountsGuardrailDecisions covers the two collectors added with this
// change. They are labelled by detector, action, and direction, and a policy
// that fires has to show up here or the dashboard is the only place an operator
// can see it happen.
func TestMetricsCountsGuardrailDecisions(t *testing.T) {
	env := newEnv(t)
	// The upstream has to be seeded before the connection, or the request the
	// test makes is served by a different fixture and never carries the address.
	up := env.NewUpstream(t, JSONResponder(http.StatusOK,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"mail billing@acme-corp.com"}}]}`))
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", up, "sk-upstream")
	env.addPIIPolicy(t, "mask")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if strings.Contains(string(res.Body), "billing@acme-corp.com") {
		t.Fatalf("address survived the policy: %s", truncate(res.Body))
	}

	metrics := env.Get(t, "/api/metrics")
	body := string(metrics.Body)
	if !strings.Contains(body, "router_guardrail_decisions_total") {
		t.Fatalf("metrics do not expose router_guardrail_decisions_total: %s", truncate(metrics.Body))
	}
	if !strings.Contains(body, `direction="outbound"`) {
		t.Errorf("the guardrail decision was not counted: %s", truncate(metrics.Body))
	}
}
