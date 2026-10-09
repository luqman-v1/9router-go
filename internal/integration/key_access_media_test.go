//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// A key restricted to a model must be refused on every inference endpoint, not
// only on chat. These routes all sit behind middleware.RequireApiKey, so before
// the gate was extended they authenticated the restricted key and then served
// it unconditionally — the allowlist was a chat-only concept in practice.
//
// Each case asserts the status the CLIENT sees. A 400 ("no such model") would
// mean the request was refused for the wrong reason, so the test also pins the
// body to the access denial.

// mediaEndpoint is one engine route that takes a model and previously had no gate.
type mediaEndpoint struct {
	name string
	path string
	body map[string]any
}

// mediaEndpoints covers the ten entrypoints the media lane owns. Images, TTS,
// STT, video, search, scrape and fetch all funnel through forwardMediaRequest;
// embeddings and systemone dispatch on their own.
var mediaEndpoints = []mediaEndpoint{
	{name: "embeddings", path: "/v1/embeddings", body: map[string]any{"model": "denied-model", "input": "hi"}},
	{name: "images", path: "/v1/images/generations", body: map[string]any{"model": "denied-model", "prompt": "a cat"}},
	{name: "tts", path: "/v1/audio/speech", body: map[string]any{"model": "denied-model", "input": "hi", "voice": "alloy"}},
	{name: "stt", path: "/v1/audio/transcriptions", body: map[string]any{"model": "denied-model"}},
	{name: "systemone", path: "/v1/systemone", body: map[string]any{"model": "denied-model", "state": "s", "questions": map[string]any{}}},
	{name: "video generations", path: "/v1/videos/generations", body: map[string]any{"model": "denied-model", "prompt": "a cat"}},
	{name: "video edits", path: "/v1/videos/edits", body: map[string]any{"model": "denied-model", "prompt": "a cat"}},
	{name: "video extensions", path: "/v1/videos/extensions", body: map[string]any{"model": "denied-model", "prompt": "a cat"}},
	{name: "search", path: "/v1/search", body: map[string]any{"model": "denied-model", "query": "hi"}},
	{name: "scrape", path: "/v1/scrape", body: map[string]any{"model": "denied-model", "url": "https://example.com"}},
}

// restrictTestKey narrows the suite's key to an allowlist that matches nothing
// the media endpoints ask for, so any request they serve is a policy escape.
func restrictTestKey(t *testing.T, env *Env, patterns ...string) {
	t.Helper()
	body := map[string]any{"models": patterns}
	res := env.Put(t, "/api/keys/integration-key/models", body)
	if res.Status != http.StatusOK {
		t.Fatalf("PUT allowlist = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
}

// TestRestrictedKeyDeniedOnEveryMediaEndpoint is the security contract: a key
// restricted to one model gets 403 from every inference endpoint, not just
// chat. The allowlist names a model no endpoint below requests, so a 200 here
// would mean the key was served a model it was never granted.
func TestRestrictedKeyDeniedOnEveryMediaEndpoint(t *testing.T) {
	env := newEnv(t)
	restrictTestKey(t, env, "only-this-model")

	for _, ep := range mediaEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			res := env.Post(t, ep.path, ep.body)
			if res.Status != http.StatusForbidden {
				t.Fatalf("POST %s = %d, want 403 (body: %s)", ep.path, res.Status, truncate(res.Body))
			}
			if msg := res.ErrorMessage(t); !strings.Contains(msg, "not permitted") {
				t.Errorf("error = %q, want it to name the access denial", msg)
			}
		})
	}
}

// TestRestrictedKeyStillDeniedOnChatLanes pins that the media gate did not
// weaken the chat one: the same key, on the same allowlist, is still refused
// where it already was.
func TestRestrictedKeyStillDeniedOnChatLanes(t *testing.T) {
	env := newEnv(t)
	restrictTestKey(t, env, "only-this-model")

	lanes := []struct {
		name string
		path string
		body map[string]any
	}{
		{name: "chat completions", path: "/v1/chat/completions", body: ChatBody("denied-model", false)},
		{name: "messages", path: "/v1/messages", body: map[string]any{"model": "denied-model", "max_tokens": 16, "messages": []map[string]any{{"role": "user", "content": "hi"}}}},
		{name: "responses", path: "/v1/responses", body: map[string]any{"model": "denied-model", "input": "hi"}},
	}

	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			res := env.Post(t, lane.path, lane.body)
			if res.Status != http.StatusForbidden {
				t.Fatalf("POST %s = %d, want 403 (body: %s)", lane.path, res.Status, truncate(res.Body))
			}
		})
	}
}

// TestUnrestrictedKeyReachesEveryMediaEndpoint is the other half: the gate must
// not deny a key that has no allowlist. A gate that rejected everything would
// pass the test above while breaking every existing deployment, and this is the
// case that catches it.
//
// These assert the request gets PAST the gate — the upstream is not seeded, so
// the failure is a resolution or connection error rather than a 200. A 403 would
// mean the gate fired.
func TestUnrestrictedKeyReachesEveryMediaEndpoint(t *testing.T) {
	env := newEnv(t)

	for _, ep := range mediaEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			res := env.Post(t, ep.path, ep.body)
			if res.Status == http.StatusForbidden {
				t.Fatalf("POST %s = 403 for a key with no allowlist (body: %s)", ep.path, truncate(res.Body))
			}
		})
	}
}

// TestMediaGateIgnoresUnresolvableModels pins that an allowlist is a policy on
// MODELS, not a free pass. A key restricted to `gpt-4o` asking for a model that
// resolves nowhere is still refused: the denial is about the policy, not about
// whether the model happens to exist.
func TestMediaGateIgnoresUnresolvableModels(t *testing.T) {
	env := newEnv(t)
	restrictTestKey(t, env, "gpt-4o")

	res := env.Post(t, "/v1/embeddings", map[string]any{"model": "no-such-model-anywhere", "input": "hi"})
	if res.Status != http.StatusForbidden {
		t.Fatalf("POST /v1/embeddings = %d, want 403 (body: %s)", res.Status, truncate(res.Body))
	}
}