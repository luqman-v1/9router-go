//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
)

// buildSemanticCacheOn arms both cache layers for one Env: the harness
// default pins every saver off, and these cases only mean anything with the
// semantic lookup and the exact-prompt hash running.
func buildSemanticCacheOn(repo *db.Repo) *handlers.TokenSaverConfig {
	ts := defaultTokenSaver(repo)
	ts.SetSemanticCache(true)
	return ts
}

// TestProviderPreferencesSteerSeparatesCacheEntries pins the cache half of
// the contract: the steer is part of both cache keys, so a request steered
// away from a provider can never be answered by that provider's stored
// response. Both layers run — the semantic lookup and the exact-prompt
// hash — and the probe is the reviewer's regression: two requests with
// byte-identical messages whose steer alone differs.
func TestProviderPreferencesSteerSeparatesCacheEntries(t *testing.T) {
	env := newEnvWithTokenSaver(t, buildSemanticCacheOn)

	deepseekUp := env.NewUpstream(t, JSONResponder(http.StatusOK,
		`{"id":"d","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"FROM_DEEPSEEK"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	groqUp := env.NewUpstream(t, JSONResponder(http.StatusOK,
		`{"id":"g","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"FROM_GROQ"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))

	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", deepseekUp, "sk-deepseek")
	env.AddConnection(t, "conn-groq", "groq", "Groq Account", groqUp, "sk-groq")
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	// R1 steered away from deepseek: served by groq, stored under R1's steer.
	res1 := env.Post(t, "/v1/chat/completions", providerPrefsBody("resilient", map[string]any{"ignore": []string{"deepseek"}}))
	if res1.Status != http.StatusOK {
		t.Fatalf("R1 POST /v1/chat/completions = %d, want 200 (body: %s)", res1.Status, truncate(res1.Body))
	}
	if !strings.Contains(string(res1.Body), "FROM_GROQ") {
		t.Errorf("R1 body = %s, want the groq answer", truncate(res1.Body))
	}
	if res1.Header.Get("X-Cache") == "HIT" {
		t.Error("R1 must not be a cache hit — it is the request that fills the cache")
	}

	// R2 steered the other way with byte-identical messages: it must miss
	// both cache layers and be answered by deepseek, not replay groq's
	// stored response to a client that excluded groq.
	res2 := env.Post(t, "/v1/chat/completions", providerPrefsBody("resilient", map[string]any{"ignore": []string{"groq"}}))
	if res2.Status != http.StatusOK {
		t.Fatalf("R2 POST /v1/chat/completions = %d, want 200 (body: %s)", res2.Status, truncate(res2.Body))
	}
	if strings.Contains(string(res2.Body), "FROM_GROQ") {
		t.Errorf("R2 body = %s, want the deepseek answer — the groq entry must not answer a groq-excluded steer", truncate(res2.Body))
	}
	if res2.Header.Get("X-Cache") == "HIT" {
		t.Error("R2 was a cache hit across a different steer: the steer is missing from the key")
	}
	if got := groqUp.Count(); got != 1 {
		t.Errorf("groq upstream received %d requests, want 1 (R1 only)", got)
	}
	if got := deepseekUp.Count(); got != 1 {
		t.Errorf("deepseek upstream received %d requests, want 1 (R2 only)", got)
	}

	// R3 repeats R1's steer byte for byte: the exact-prompt layer must hit,
	// proving the steer separation did not simply disable caching.
	res3 := env.Post(t, "/v1/chat/completions", providerPrefsBody("resilient", map[string]any{"ignore": []string{"deepseek"}}))
	if res3.Header.Get("X-Cache") != "HIT" {
		t.Errorf("R3 X-Cache = %q, want HIT — an identical steer should reuse its entry", res3.Header.Get("X-Cache"))
	}
	if !strings.Contains(string(res3.Body), "FROM_GROQ") {
		t.Errorf("R3 body = %s, want the cached groq answer", truncate(res3.Body))
	}
	if got := groqUp.Count(); got != 1 {
		t.Errorf("groq upstream received %d requests after the repeat, want 1 — the hit must not touch the provider", got)
	}
}

// TestProviderPreferencesExcludeByNodePrefix pins the single-model half of
// the contract for a custom provider node: the request resolves to the node
// id, but the client steered by the node's prefix, so the exclude must
// match that spelling or it fails open and routes to the excluded node.
func TestProviderPreferencesExcludeByNodePrefix(t *testing.T) {
	env := newEnv(t)

	upstream := env.NewUpstream(t, chatCompletionResponder())
	const nodeID = "openai-compatible-chat-bn"
	env.AddConnection(t, "conn-bn", nodeID, "BN Node", upstream, "sk-bn")
	if _, err := env.Repo.CreateProviderNode(nodeID, "openai-compatible", "BN Node",
		`{"prefix":"bn","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create provider node: %v", err)
	}

	// No steer: the prefix routes to the node and the request is served.
	res := env.Post(t, "/v1/chat/completions", providerPrefsBody("bn/some-model", map[string]any{}))
	if res.Status != http.StatusOK {
		t.Fatalf("unsteered POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Fatalf("upstream received %d requests, want 1 for the unsteered request", got)
	}

	// The steer names the prefix the client typed. Before the fix this
	// routed straight through to the excluded node.
	res = env.Post(t, "/v1/chat/completions", providerPrefsBody("bn/some-model", map[string]any{"ignore": []string{"bn"}}))
	if res.Status != http.StatusBadRequest {
		t.Fatalf("prefix-excluded POST = %d, want 400 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Errorf("excluded upstream received %d requests total, want 1 — the prefix steer must exclude the node", got)
	}

	// The node id spelling still excludes too.
	res = env.Post(t, "/v1/chat/completions", providerPrefsBody("bn/some-model", map[string]any{"ignore": []string{nodeID}}))
	if res.Status != http.StatusBadRequest {
		t.Fatalf("id-excluded POST = %d, want 400 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Errorf("excluded upstream received %d requests total after the id steer, want 1", got)
	}
}
