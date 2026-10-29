//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// The custom-provider vision report, end to end: a conversation whose image
// lives in an OLD turn reaches a text-only openai-compatible upstream with the
// image part still inside, and the upstream answers 400
// `content[1].type has unsupported value "image_url"`. The request never
// registered as a vision request (the trailing turn is text-only, and the
// required-capabilities scan is deliberately trailing-only), so the
// capacity-adapter vision pool was never consulted and the combo had nothing
// to fall back to.
//
// crownUpstream mimics that upstream: it rejects any body still carrying an
// image_url part with the exact crown.ai.id error, and answers a text-only
// body with a canned completion. The harness drains r.Body before the handler
// runs, so the check reads the harness's own record of the request rather
// than the request body.
func crownUpstream(t *testing.T, env *Env) *Upstream {
	t.Helper()
	const crownError = `{"error":{"message":"messages[302].content[1].type has unsupported value \"image_url\"","type":"upstream_error","param":"","code":null}}`
	var up *Upstream
	up = env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		up.mu.Lock()
		var body []byte
		if n := len(up.requests); n > 0 {
			body = up.requests[n-1].Body
		}
		up.mu.Unlock()
		if strings.Contains(string(body), "image_url") {
			JSONResponder(http.StatusBadRequest, crownError)(w, r)
			return
		}
		chatCompletionResponder()(w, r)
	})
	return up
}

// legacyMediaBody is the reported shape: a system prompt, an old user turn
// that asked about an image, the assistant's answer, and a text-only current
// turn.
func legacyMediaBody(model string) map[string]any {
	return map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": "what is in this picture?"},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
			}},
			{"role": "assistant", "content": "a cat"},
			{"role": "user", "content": "thanks, now write me a poem"},
		},
	}
}

// TestLegacyMediaStrippedForTextOnlyModel is the report's exact scenario on
// the single-model lane: the turn succeeds and the upstream sees a body with
// no image_url in it. Before the strip, this request died with the crown 400.
func TestLegacyMediaStrippedForTextOnlyModel(t *testing.T) {
	env := newEnv(t)
	upstream := crownUpstream(t, env)
	env.AddConnection(t, "conn-deepseek", "deepseek", "Text Only Account", upstream, "sk-upstream")

	res := env.Post(t, "/v1/chat/completions", legacyMediaBody("deepseek-chat"))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := string(upstream.Last(t).Body)
	if strings.Contains(sent, "image_url") {
		t.Fatalf("upstream still received an image_url part: %s", truncate(upstream.Last(t).Body))
	}
	if !strings.Contains(sent, "media from an earlier turn removed") {
		t.Fatalf("stripped media left no placeholder behind: %s", truncate(upstream.Last(t).Body))
	}
	// The current turn's prose must survive the strip verbatim.
	if !strings.Contains(sent, "thanks, now write me a poem") {
		t.Fatalf("trailing turn lost in transit: %s", truncate(upstream.Last(t).Body))
	}
}

// TestLegacyMediaStrippedThroughCombo runs the same scenario through the combo
// lane, the shape the report actually came through (a named combo whose only
// member is a custom openai-compatible provider).
func TestLegacyMediaStrippedThroughCombo(t *testing.T) {
	env := newEnv(t)
	upstream := crownUpstream(t, env)
	env.AddConnection(t, "conn-deepseek", "deepseek", "Text Only Account", upstream, "sk-upstream")
	env.AddCombo(t, "combo-bansos", "bansos", []string{"deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/chat/completions", legacyMediaBody("bansos"))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions (combo) = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := string(upstream.Last(t).Body)
	if strings.Contains(sent, "image_url") {
		t.Fatalf("combo forwarded an image_url part to a text-only upstream: %s", truncate(upstream.Last(t).Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Errorf("upstream received %d requests, want 1 — the strip should have prevented the 400 entirely", got)
	}
}

// TestLegacyMediaKeptWhenComboHasVisionMember pins the other side of the
// contract: when a combo member CAN see images, the old media stays in the
// history and that member serves the turn — the strip must not cost context
// fidelity for combos that do not need it.
func TestLegacyMediaKeptWhenComboHasVisionMember(t *testing.T) {
	env := newEnv(t)
	vision := env.NewUpstream(t, chatCompletionResponder())
	textOnly := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-openai", "openai", "Vision Account", vision, "sk-openai")
	env.AddConnection(t, "conn-deepseek", "deepseek", "Text Only Account", textOnly, "sk-deepseek")
	env.AddCombo(t, "combo-mixed", "mixed", []string{"openai/gpt-4o", "deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/chat/completions", legacyMediaBody("mixed"))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions (mixed combo) = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := vision.Count(); got != 1 {
		t.Fatalf("vision member upstream received %d requests, want 1", got)
	}
	sent := string(vision.Last(t).Body)
	if !strings.Contains(sent, "image_url") {
		t.Fatalf("vision member received a stripped history; the image should have been kept: %s", truncate(vision.Last(t).Body))
	}
	if got := textOnly.Count(); got != 0 {
		t.Errorf("text-only member received %d requests, want 0", got)
	}
}

// TestCurrentTurnImageStillReachesUpstream pins that the strip never touches
// the CURRENT turn: an image the client is asking about right now must go out
// even to a text-only upstream — answering it from a text-only model with the
// image quietly deleted would be worse than the error. The client sees the
// upstream failure; the vision pool was the intended escape hatch and this
// env has no reachable member for it.
func TestCurrentTurnImageStillReachesUpstream(t *testing.T) {
	env := newEnv(t)
	// The adapter pool's default vision model resolves to a live keyless
	// provider, and this test's point is the text-only member's behaviour —
	// turn the pool off so the request has nowhere to go but the fake
	// text-only upstream.
	if err := env.Repo.UpdateSettingsRaw(map[string]any{
		"capacityAdapter": map[string]any{
			"vision": map[string]any{"enabled": false},
		},
	}); err != nil {
		t.Fatalf("disable capacity adapter: %v", err)
	}
	upstream := crownUpstream(t, env)
	env.AddConnection(t, "conn-deepseek", "deepseek", "Text Only Account", upstream, "sk-upstream")

	body := map[string]any{
		"model":  "deepseek-chat",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": "what is in this picture?"},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
			}},
		},
	}
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status == http.StatusOK {
		t.Fatalf("a text-only upstream answered a current-turn image request with 200 — the image was silently dropped")
	}
	sent := string(upstream.Last(t).Body)
	if !strings.Contains(sent, "image_url") {
		t.Fatalf("current-turn image was stripped in transit: %s", truncate(upstream.Last(t).Body))
	}
}
