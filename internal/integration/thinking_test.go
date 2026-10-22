//go:build integration

package integration

import (
	"net/http"
	"testing"

	json "encoding/json/v2"
)

// TestStepModelOmitsDisabledReasoning is the reported bug: a client
// reasoning-disable field forwarded verbatim to a step model makes the
// provider answer 400 (mandatory reasoning). The unified pass must omit the
// field so the upstream default applies.
func TestStepModelOmitsDisabledReasoning(t *testing.T) {
	env, upstream := newProviderEnv(t)

	body := ChatBody("deepseek/step-5-preview", false)
	body["reasoning_effort"] = "none"
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := upstream.Last(t)
	if got := sent.Model(t); got != "step-5-preview" {
		t.Errorf("upstream model = %q, want step-5-preview", got)
	}
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	for _, k := range []string{"reasoning_effort", "thinking", "enable_thinking", "reasoning"} {
		if _, has := wire[k]; has {
			t.Errorf("reasoning field %q reached the upstream: %s", k, truncate(sent.Body))
		}
	}
}

// TestStepModelOmitsReasoningEnabledFalse pins the enabled:false dialect on
// the step wire: no reasoning field may reach the fake upstream.
func TestStepModelOmitsReasoningEnabledFalse(t *testing.T) {
	env, upstream := newProviderEnv(t)

	body := ChatBody("deepseek/step-5-preview", false)
	body["reasoning"] = map[string]any{"enabled": false}
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var wire map[string]any
	if err := json.Unmarshal(upstream.Last(t).Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"reasoning_effort", "thinking", "enable_thinking", "reasoning"} {
		if _, has := wire[k]; has {
			t.Errorf("reasoning field %q reached the upstream: %s", k, truncate(upstream.Last(t).Body))
		}
	}
}

// TestStepModelMapsReasoningEnabledTrueToAuto pins the enabled:true dialect
// on the step wire: reasoning_effort:"auto" must reach the fake upstream.
func TestStepModelMapsReasoningEnabledTrueToAuto(t *testing.T) {
	env, upstream := newProviderEnv(t)

	body := ChatBody("deepseek/step-5-preview", false)
	body["reasoning"] = map[string]any{"enabled": true}
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var wire map[string]any
	if err := json.Unmarshal(upstream.Last(t).Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire["reasoning_effort"] != "auto" {
		t.Errorf("reasoning_effort = %v, want auto", wire["reasoning_effort"])
	}
}

// TestStepModelClampsMaxToHigh pins the step writer clamp (xhigh|max → high).
func TestStepModelClampsMaxToHigh(t *testing.T) {
	env, upstream := newProviderEnv(t)

	body := ChatBody("deepseek/step-5-preview", false)
	body["reasoning_effort"] = "max"
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var wire map[string]any
	if err := json.Unmarshal(upstream.Last(t).Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", wire["reasoning_effort"])
	}
}

// TestSuffixLevelStrippedFromWireModel pins gap B: "(none)" is stripped from
// the wire model and applied as the override.
func TestSuffixLevelStrippedFromWireModel(t *testing.T) {
	env, upstream := newProviderEnv(t)

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/step-5-preview(none)", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := upstream.Last(t)
	if got := sent.Model(t); got != "step-5-preview" {
		t.Errorf("upstream model = %q, want suffix-free step-5-preview", got)
	}
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := wire["reasoning_effort"]; has {
		t.Errorf("reasoning_effort reached the upstream: %s", truncate(sent.Body))
	}
}

// TestSuffixLevelApplies pins a level suffix surviving onto the wire
// (deepseek-reasoner is a reasoning model; deepseek-chat is not).
func TestSuffixLevelApplies(t *testing.T) {
	env, upstream := newProviderEnv(t)

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-reasoner(high)", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := upstream.Last(t)
	if got := sent.Model(t); got != "deepseek-reasoner" {
		t.Errorf("upstream model = %q, want deepseek-reasoner", got)
	}
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// deepseek writer maps high→high.
	if wire["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", wire["reasoning_effort"])
	}
}

// TestProviderThinkingOffOnStepModel pins the operator override: with
// providerThinking deepseek=off, a step-model body with no reasoning field
// still omits the field (mandated reasoning).
func TestProviderThinkingOffOnStepModel(t *testing.T) {
	env, upstream := newProviderEnv(t)

	res := env.Put(t, "/api/settings", map[string]any{
		"providerThinking": map[string]any{"deepseek": map[string]any{"mode": "off"}},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	res = env.Post(t, "/v1/chat/completions", ChatBody("deepseek/step-5-preview", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var wire map[string]any
	if err := json.Unmarshal(upstream.Last(t).Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := wire["reasoning_effort"]; has {
		t.Errorf("reasoning_effort reached the upstream: %s", truncate(upstream.Last(t).Body))
	}
}

// TestEmptyToolsArrayDropped pins the OpenAI filter on the same-format path.
func TestEmptyToolsArrayDropped(t *testing.T) {
	env, upstream := newProviderEnv(t)

	body := ChatBody("deepseek/deepseek-chat", false)
	body["tools"] = []any{}
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var wire map[string]any
	if err := json.Unmarshal(upstream.Last(t).Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := wire["tools"]; has {
		t.Errorf("empty tools array reached the upstream: %s", truncate(upstream.Last(t).Body))
	}
}

// TestClaudeUpstreamMapsEffortToThinking pins OpenAI-client intent surviving
// onto a Claude-shaped upstream. minimax-code serves Claude Messages
// (Format: claude) with a claude-adaptive thinkingFormat, so high becomes
// thinking{adaptive} + output_config{high} with no reasoning_effort.
// (A "claude" provider id cannot be used here: with a fake baseUrl the
// gateway classifies it as an OpenAI upstream.)
func TestClaudeUpstreamMapsEffortToThinking(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, JSONResponder(http.StatusOK, claudeMessagesResponseJSON))
	addMCodeConnection(t, env, "conn-mcode", upstream)

	body := ChatBody("minimax-code/MiniMax-M3", false)
	body["reasoning_effort"] = "high"
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var wire map[string]any
	if err := json.Unmarshal(upstream.Last(t).Body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := wire["reasoning_effort"]; has {
		t.Errorf("reasoning_effort reached a Claude upstream: %s", truncate(upstream.Last(t).Body))
	}
	th, ok := wire["thinking"].(map[string]any)
	if !ok || th["type"] != "adaptive" {
		t.Errorf("thinking = %v, want {adaptive}", wire["thinking"])
	}
	oc, ok := wire["output_config"].(map[string]any)
	if !ok || oc["effort"] != "high" {
		t.Errorf("output_config = %v, want {high}", wire["output_config"])
	}
}

// TestSuffixOnAComboNameResolvesAndApplies pins that a "(level)" suffix on a
// combo name resolves. The suffix is a request override, not part of the model
// id, so it comes off the name before the name is resolved against the
// aliases and the combos — otherwise "mycombo(high)" resolves to neither.
// The intent it carries then travels beside the request, because the combo
// path replaces the body's model field with the member it is about to call.
func TestSuffixOnAComboNameResolvesAndApplies(t *testing.T) {
	env, upstream := newProviderEnv(t)
	env.AddCombo(t, "combo-1", "mycombo", []string{"deepseek/deepseek-v4-pro"})

	body := ChatBody("mycombo(high)", false)
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := upstream.Last(t)
	if got := sent.Model(t); got != "deepseek-v4-pro" {
		t.Errorf("upstream model = %q, want the combo member", got)
	}
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if wire["reasoning_effort"] != "high" {
		t.Errorf("the suffix level must reach the upstream, got %s", truncate(sent.Body))
	}
}
