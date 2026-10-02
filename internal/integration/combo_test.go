//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// TestComboFallsBackToTheNextModel pins the combo contract: a combo name is
// resolved to its member list, the first member's provider is tried, and a
// failure moves on to the next member. This is how an operator gets uptime
// across two accounts, so a regression here is a silent single-account outage.
func TestComboFallsBackToTheNextModel(t *testing.T) {
	env := newEnv(t)

	broken := env.NewUpstream(t, JSONResponder(http.StatusInternalServerError,
		`{"error":{"message":"model overloaded","type":"server_error"}}`))
	working := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", broken, "sk-deepseek")
	env.AddConnection(t, "conn-groq", "groq", "Groq Account", working, "sk-groq")
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("resilient", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions with a combo = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := broken.Count(); got < 1 {
		t.Errorf("first member upstream received %d requests, want at least 1", got)
	}
	if got := working.Count(); got != 1 {
		t.Errorf("second member upstream received %d requests, want 1", got)
	}
	// The fallback must carry the second member's own model, not the first's.
	if got := working.Last(t).Model(t); got != "llama-3.3-70b-versatile" {
		t.Errorf("fallback upstream model = %q, want the combo's second member model", got)
	}
}

// TestComboSurfacesTheLastUpstreamError pins the other side: when every member
// fails, the client must see a real provider error, not a synthesized
// "combo failed" message that hides the cause.
//
// The number of requests a failing member receives is deliberately not asserted:
// a 502/503 is retried within one request before the combo falls through
// (upstream open-sse/executors/base.js:155 retries before the fallback at :157),
// so the count is an implementation detail — the client-visible contract here is
// the status and the reason it carries.
func TestComboSurfacesTheLastUpstreamError(t *testing.T) {
	env := newEnv(t)

	first := env.NewUpstream(t, JSONResponder(http.StatusBadGateway, `{"error":{"message":"gateway down"}}`))
	second := env.NewUpstream(t, JSONResponder(http.StatusServiceUnavailable, `{"error":{"message":"all accounts busy"}}`))

	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", first, "sk-deepseek")
	env.AddConnection(t, "conn-groq", "groq", "Groq Account", second, "sk-groq")
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("resilient", false))
	if res.Status != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/chat/completions = %d, want 503 from the last failing member (body: %s)",
			res.Status, truncate(res.Body))
	}
	if msg := res.ErrorMessage(t); !strings.Contains(msg, "busy") {
		t.Errorf("error message = %q, want the last upstream reason", msg)
	}
}

// TestComboWithASingleMemberBehavesLikeThatModel pins that a one-model combo
// is transparent: the request reaches that provider and the response comes
// back unchanged. It is the shape most operator-created combos actually have.
func TestComboWithASingleMemberBehavesLikeThatModel(t *testing.T) {
	env, upstream := newProviderEnv(t)
	env.AddCombo(t, "combo-1", "solo", []string{"deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("solo", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions with a single-member combo = %d, want 200 (body: %s)",
			res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Errorf("upstream received %d requests, want 1", got)
	}
	if got := upstream.Last(t).Model(t); got != "deepseek-chat" {
		t.Errorf("upstream model = %q, want \"deepseek-chat\"", got)
	}
}
