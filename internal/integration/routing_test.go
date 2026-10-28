//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// TestRetryableUpstreamErrorRotatesAccount pins the account-rotation contract
// on the single-model path: a 429 is a quota problem, not a request problem,
// so the gateway must lock that connection and try the next one instead of
// surfacing the throttle to a client that has a healthy account available.
func TestRetryableUpstreamErrorRotatesAccount(t *testing.T) {
	env := newEnv(t)

	throttled := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests,
		`{"error":{"message":"rate limit reached for requests","type":"rate_limit_error"}}`))
	healthy := env.NewUpstream(t, chatCompletionResponder())

	// Distinct priorities order the picker, so the throttled account is picked
	// first. Leaving both at the harness default (priority 1) would tie them,
	// and the tie-break is the most recently updated row, decided by a
	// second-resolution timestamp: the first request then goes to whichever
	// account was inserted on the later second.
	env.AddConnectionWithPriority(t, "conn-1", "deepseek", "Throttled Account", throttled, "sk-throttled", 1)
	env.AddConnectionWithPriority(t, "conn-2", "deepseek", "Healthy Account", healthy, "sk-healthy", 2)

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 after rotating accounts (body: %s)",
			res.Status, truncate(res.Body))
	}
	if got := throttled.Count(); got != 1 {
		t.Errorf("throttled upstream received %d requests, want 1", got)
	}
	if got := healthy.Count(); got != 1 {
		t.Errorf("healthy upstream received %d requests, want 1", got)
	}

	// The throttled account is now in cooldown, so the next request must go
	// straight to the healthy one.
	second := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if second.Status != http.StatusOK {
		t.Fatalf("second POST /v1/chat/completions = %d, want 200 (body: %s)", second.Status, truncate(second.Body))
	}
	if got := throttled.Count(); got != 1 {
		t.Errorf("throttled upstream received %d requests in total, want 1: the locked account must be skipped", got)
	}
	if got := healthy.Count(); got != 2 {
		t.Errorf("healthy upstream received %d requests in total, want 2", got)
	}
}

// TestAllAccountsThrottledSurfacesThrottle pins the other half of the rotation
// contract: when every account is throttled, the client must see the real 429
// (so it can back off), not a synthesized 502 that looks like a gateway bug.
func TestAllAccountsThrottledSurfacesThrottle(t *testing.T) {
	env := newEnv(t)

	first := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests,
		`{"error":{"message":"quota exhausted","type":"rate_limit_error"}}`))
	second := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests,
		`{"error":{"message":"quota exhausted too","type":"rate_limit_error"}}`))

	env.AddConnection(t, "conn-1", "deepseek", "Account One", first, "sk-one")
	env.AddConnection(t, "conn-2", "deepseek", "Account Two", second, "sk-two")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("POST /v1/chat/completions = %d, want 429 (body: %s)", res.Status, truncate(res.Body))
	}
	if msg := res.ErrorMessage(t); !strings.Contains(msg, "quota exhausted") {
		t.Errorf("error message = %q, want the upstream throttle reason from the last account tried", msg)
	}
	if got := first.Count(); got != 1 {
		t.Errorf("first upstream received %d requests, want 1", got)
	}
	if got := second.Count(); got != 1 {
		t.Errorf("second upstream received %d requests, want 1", got)
	}
}

// TestDisabledConnectionIsNotUsed pins that the dashboard's enable/disable
// toggle actually removes an account from rotation. A regression here sends
// traffic to an account the operator retired — often a deactivated card.
func TestDisabledConnectionIsNotUsed(t *testing.T) {
	env := newEnv(t)

	disabled := env.NewUpstream(t, chatCompletionResponder())
	enabled := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-disabled", "deepseek", "Disabled Account", disabled, "sk-disabled")
	env.AddConnection(t, "conn-enabled", "deepseek", "Enabled Account", enabled, "sk-enabled")

	if err := env.Repo.SetConnectionStatus("conn-disabled", false); err != nil {
		t.Fatalf("disable connection: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := disabled.Count(); got != 0 {
		t.Errorf("disabled upstream received %d requests, want 0", got)
	}
	if got := enabled.Count(); got != 1 {
		t.Errorf("enabled upstream received %d requests, want 1", got)
	}
}

// TestProvidersStayIsolated pins the strict provider-isolation rule: a model
// addressed to one provider must reach that provider's account and no other.
// Cross-hijacking here leaks one provider's credential to another's upstream,
// which is the failure mode AGENTS.md §3.A calls out explicitly.
func TestProvidersStayIsolated(t *testing.T) {
	env := newEnv(t)

	deepseek := env.NewUpstream(t, chatCompletionResponder())
	groq := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", deepseek, "sk-deepseek-only")
	env.AddConnection(t, "conn-groq", "groq", "Groq Account", groq, "sk-groq-only")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	if got := deepseek.Count(); got != 1 {
		t.Errorf("deepseek upstream received %d requests, want 1", got)
	}
	if got := groq.Count(); got != 0 {
		t.Errorf("groq upstream received %d requests, want 0: a deepseek/ model must not fall through to another provider", got)
	}
	if got := deepseek.Last(t).Header.Get("Authorization"); got != "Bearer sk-deepseek-only" {
		t.Errorf("deepseek upstream Authorization = %q, want the deepseek credential", got)
	}
}

// TestProviderAliasResolvesToTheSameAccount pins the short-alias table
// (ds/ → deepseek). Agents and CLI tools address models by alias, so a broken
// alias table is a silent routing change for every user of that alias.
func TestProviderAliasResolvesToTheSameAccount(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", upstream, "sk-upstream")

	res := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions with the ds/ alias = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Errorf("upstream received %d requests, want 1", got)
	}
	if got := upstream.Last(t).Model(t); got != "deepseek-chat" {
		t.Errorf("upstream model = %q, want \"deepseek-chat\"", got)
	}
}

// TestModelAliasRedirectsToTheProvider pins the kv-backed model alias: a
// dashboard-created alias must redirect the request to its target and rewrite
// the model on the way out. Agents and shared configs depend on a stable alias
// surviving a model rename on the provider side.
func TestModelAliasRedirectsToTheProvider(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", upstream, "sk-deepseek")

	// Repo.SetKV is the store HandleSetModelAlias writes to.
	if err := env.Repo.SetKV("modelAliases", "team-fast", `"deepseek/deepseek-chat"`); err != nil {
		t.Fatalf("set model alias: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("team-fast", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions with a model alias = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Errorf("upstream received %d requests, want 1", got)
	}
	if got := upstream.Last(t).Model(t); got != "deepseek-chat" {
		t.Errorf("upstream model = %q, want \"deepseek-chat\" behind the alias", got)
	}
}
