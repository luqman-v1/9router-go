//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// providerPrefsBody is a chat request carrying the request-level provider
// routing object.
func providerPrefsBody(model string, prefs map[string]any) map[string]any {
	return map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": "Summarize the Go memory model."}},
		"stream":   false,
		"provider": prefs,
	}
}

// TestProviderPreferencesIgnoreSteersTheCombo pins the observable half of
// the provider object: a combo's first member is normally tried first, and
// "ignore" must remove it from the candidate set entirely — the excluded
// upstream receives nothing, not a failed attempt followed by a fallback.
func TestProviderPreferencesIgnoreSteersTheCombo(t *testing.T) {
	env := newEnv(t)

	excluded := env.NewUpstream(t, chatCompletionResponder())
	kept := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", excluded, "sk-deepseek")
	env.AddConnection(t, "conn-groq", "groq", "Groq Account", kept, "sk-groq")
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/chat/completions", providerPrefsBody("resilient", map[string]any{"ignore": []string{"deepseek"}}))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := excluded.Count(); got != 0 {
		t.Errorf("excluded upstream received %d requests, want 0 — the preference must remove the member, not demote it", got)
	}
	if got := kept.Count(); got != 1 {
		t.Errorf("kept upstream received %d requests, want 1", got)
	}
	if got := kept.Last(t).Model(t); got != "llama-3.3-70b-versatile" {
		t.Errorf("kept upstream model = %q, want the combo's groq member", got)
	}
}

// TestProviderPreferencesOnlyRejectsAnExcludedSingleModel pins the other
// side: a single-model request whose only provider the preferences exclude
// must fail with the steer named, never silently route to the excluded
// provider it asked to avoid.
func TestProviderPreferencesOnlyRejectsAnExcludedSingleModel(t *testing.T) {
	env := newEnv(t)

	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", upstream, "sk-deepseek")

	res := env.Post(t, "/v1/chat/completions", providerPrefsBody("deepseek/deepseek-chat",
		map[string]any{"only": []string{"groq"}}))
	if res.Status != http.StatusBadRequest {
		t.Fatalf("POST /v1/chat/completions = %d, want 400 (body: %s)", res.Status, truncate(res.Body))
	}
	if msg := res.ErrorMessage(t); msg == "" {
		t.Errorf("error message is empty, want the excluded-provider reason")
	}
	if got := upstream.Count(); got != 0 {
		t.Errorf("excluded upstream received %d requests, want 0", got)
	}
}

// TestProviderPreferencesOrderWithFallbacksDisabled pins "order" as a hard
// allowlist when allow_fallbacks is false: the first member of the combo is
// outside the order list, so it must not be attempted at all.
func TestProviderPreferencesOrderWithFallbacksDisabled(t *testing.T) {
	env := newEnv(t)

	first := env.NewUpstream(t, chatCompletionResponder())
	second := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", first, "sk-deepseek")
	env.AddConnection(t, "conn-groq", "groq", "Groq Account", second, "sk-groq")
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/chat/completions", providerPrefsBody("resilient",
		map[string]any{"order": []string{"groq"}, "allow_fallbacks": false}))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := first.Count(); got != 0 {
		t.Errorf("out-of-order upstream received %d requests, want 0 with fallbacks disabled", got)
	}
	if got := second.Count(); got != 1 {
		t.Errorf("ordered upstream received %d requests, want 1", got)
	}
}

// TestProviderPreferencesOnTheMessagesLane pins that the object also
// steers the Claude Messages lane, whose body is translated on the way
// through — the client-only field must survive neither the steer's
// filtering nor leak into the translated upstream body.
func TestProviderPreferencesOnTheMessagesLane(t *testing.T) {
	env := newEnv(t)

	excluded := env.NewUpstream(t, chatCompletionResponder())
	kept := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", excluded, "sk-deepseek")
	env.AddConnection(t, "conn-groq", "groq", "Groq Account", kept, "sk-groq")
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	body := claudeMessagesBody("resilient", false)
	body["provider"] = map[string]any{"ignore": []string{"deepseek"}}

	res := env.Post(t, "/v1/messages", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/messages = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := excluded.Count(); got != 0 {
		t.Errorf("excluded upstream received %d requests, want 0 on the messages lane", got)
	}
	if got := kept.Count(); got != 1 {
		t.Errorf("kept upstream received %d requests, want 1 on the messages lane", got)
	}
}
