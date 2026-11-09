//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// TestQuotaExhaustionAnswers429 drives the per-key token budget through the
// real router: a key under its budget is served, and the same key is refused
// with 429 once the spend crosses the ceiling.
//
// The status is the contract a client branches on, and the error type is what
// tells "you are over your allowance" apart from "you are sending too fast" —
// a rate limit clears by itself, a spent budget does not.
func TestQuotaExhaustionAnswers429(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "quota-429")

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"tokenQuota": 1}); put.Status != http.StatusOK {
		t.Fatalf("set token quota: status %d: %s", put.Status, truncate(put.Body))
	}

	first := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false), WithAPIKey(plaintext))
	if first.Status != http.StatusOK {
		t.Fatalf("first request = %d, want 200 (body: %s)", first.Status, truncate(first.Body))
	}
	dispatched := upstream.Count()

	// Usage is recorded after the response was written, so wait for the spend
	// to become visible rather than racing the usage writer.
	if used := waitForKeyUsage(t, env, id); used <= 1 {
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
	if got := refusalType(t, rejected); got != "insufficient_quota" {
		t.Errorf("error type = %q, want insufficient_quota", got)
	}
	if upstream.Count() != dispatched {
		t.Errorf("the refused request reached the provider (%d -> %d dispatches)", dispatched, upstream.Count())
	}
}

// TestKeyContextLimitTrimsTheRequest drives the per-key context ceiling
// through the real router: a key with a small ceiling sends a shorter request
// upstream than the same key without one, and the request is served rather
// than refused.
//
// The ceiling bounds one request, so the gateway trims the history to fit
// instead of rejecting the turn — a key that cannot hold a long conversation
// would be useless exactly when it is used hard.
func TestKeyContextLimitTrimsTheRequest(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "context-limit")

	// A conversation far over a 200-token ceiling.
	var messages []map[string]any
	for i := range 40 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": strings.Repeat("x", 400),
		})
	}

	body := map[string]any{
		"model":      "deepseek/deepseek-chat",
		"messages":   messages,
		"max_tokens": 4000,
	}

	// Without a ceiling the whole conversation reaches the provider.
	before := env.Post(t, "/v1/chat/completions", body, WithAPIKey(plaintext))
	if before.Status != http.StatusOK {
		t.Fatalf("unbounded request = %d, want 200 (body: %s)", before.Status, truncate(before.Body))
	}
	unbounded := len(upstream.Last(t).Body)

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"contextLimit": 200}); put.Status != http.StatusOK {
		t.Fatalf("set context limit: status %d: %s", put.Status, truncate(put.Body))
	}

	after := env.Post(t, "/v1/chat/completions", body, WithAPIKey(plaintext))
	if after.Status != http.StatusOK {
		t.Fatalf("bounded request = %d, want 200 — a ceiling trims, it does not refuse (body: %s)",
			after.Status, truncate(after.Body))
	}
	bounded := len(upstream.Last(t).Body)
	if bounded >= unbounded {
		t.Errorf("bounded request was not trimmed: %d bytes upstream, want fewer than %d", bounded, unbounded)
	}
}

// TestKeyContextLimitLeavesOtherKeysAlone proves the ceiling is per key: a
// second key without one still sends its whole conversation.
func TestKeyContextLimitLeavesOtherKeysAlone(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "context-limited")
	_, otherPlaintext := limitKey(t, env, "context-free")

	var messages []map[string]any
	for i := range 40 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": strings.Repeat("x", 400),
		})
	}
	body := map[string]any{
		"model":      "deepseek/deepseek-chat",
		"messages":   messages,
		"max_tokens": 4000,
	}

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"contextLimit": 200}); put.Status != http.StatusOK {
		t.Fatalf("set context limit: status %d: %s", put.Status, truncate(put.Body))
	}

	env.Post(t, "/v1/chat/completions", body, WithAPIKey(plaintext))
	bounded := len(upstream.Last(t).Body)

	env.Post(t, "/v1/chat/completions", body, WithAPIKey(otherPlaintext))
	unbounded := len(upstream.Last(t).Body)

	if unbounded <= bounded {
		t.Errorf("the key without a ceiling was trimmed too: %d bytes, want more than %d", unbounded, bounded)
	}
}
