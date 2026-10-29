package thinking

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// apply builds a fresh body, runs Apply, and returns it.
func apply(t *testing.T, target, provider, model string, body map[string]any, override *Intent) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("apply fixture marshal: %v", err)
	}
	out, _ := Apply(m, target, provider, model, override)
	return out
}

func mustAbsent(t *testing.T, body map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := body[k]; ok {
			t.Fatalf("expected no %q in %+v", k, body)
		}
	}
}

func TestMuseSparkContributorClamp(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		body     map[string]any
		override *Intent
	}{
		{"effort-none", "muse-spark-1.3-contributor", map[string]any{"model": "muse-spark-1.3-contributor", "reasoning_effort": "none"}, nil},
		{"thinking-disabled", "muse-spark-1.3-contributor", map[string]any{"model": "muse-spark-1.3-contributor", "thinking": map[string]any{"type": "disabled"}}, nil},
		{"none-suffix", "muse-spark-1.3-contributor(none)", map[string]any{"model": "muse-spark-1.3-contributor"}, nil},
		{"override-off", "muse-spark-1.3-contributor", map[string]any{"model": "muse-spark-1.3-contributor"}, &Intent{Mode: ModeNone}},
	}
	for _, tc := range cases {
		out := apply(t, "openai", "cline", tc.model, tc.body, tc.override)
		if out["reasoning_effort"] != "minimal" {
			t.Fatalf("%s: reasoning_effort = %v, want minimal (full body: %+v)", tc.name, out["reasoning_effort"], out)
		}
	}
}

func TestApplyReasoningEnabledFalseEqualsEffortNone(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		provider string
		model    string
	}{
		{"openai", "openai", "openai", "gpt-5.3-codex"},
		{"openai-responses", "openai-responses", "muse", "muse-spark-1.3-contributor"},
		{"claude-adaptive", "claude", "claude", "claude-opus-4.7"},
		{"claude-budget", "claude", "claude", "claude-haiku-4.5"},
		{"gemini-level", "openai", "gemini", "gemini-3-pro"},
		{"gemini-budget", "openai", "gemini", "gemini-2.5-pro"},
		{"zai", "openai", "glm", "glm-4.6"},
		{"qwen", "openai", "qwen", "qwen3-max"},
		{"deepseek", "openai", "deepseek", "deepseek-v4-pro"},
		{"kimi", "openai", "kimi", "kimi-k2.6"},
		{"minimax", "claude", "minimax", "MiniMax-M3"},
		{"hunyuan", "openai", "hunyuan", "hunyuan-turbos"},
		{"step", "openai", "deepseek", "step-5-preview"},
		{"tokenrouter", "openai", "tokenrouter", "step-5-preview"},
		{"kiro", "openai", "kiro", "claude-sonnet-4.6"},
		{"commandcode", "commandcode", "commandcode", "deepseek-v4-pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled := apply(t, tc.target, tc.provider, tc.model,
				map[string]any{"model": tc.model, "reasoning": map[string]any{"enabled": false}}, nil)
			effortNone := apply(t, tc.target, tc.provider, tc.model,
				map[string]any{"model": tc.model, "reasoning_effort": "none"}, nil)
			if !reflect.DeepEqual(enabled, effortNone) {
				t.Fatalf("reasoning:{enabled:false} = %+v, want reasoning_effort:none = %+v", enabled, effortNone)
			}
		})
	}
}

// TestApplyJSONLeavesABodyWithoutIntentByteIdentical pins the fast path: a
// body that asks for no reasoning is returned byte for byte, so an integer
// above 2^53 in it reaches the upstream unchanged.
func TestApplyJSONLeavesABodyWithoutIntentByteIdentical(t *testing.T) {
	body := []byte(`{"model":"m","user_id":9007199254740993,"messages":[{"role":"user","content":"hi"}]}`)
	out := ApplyJSON(body, "openai", "openai", "m", nil)
	if !bytes.Equal(out, body) {
		t.Fatalf("a body without intent must be returned byte for byte:\nin:  %s\nout: %s", body, out)
	}
}

// TestApplyReportsWhetherItChangedTheBody pins the change report the byte
// passthrough is built on: a body the pass did not touch reports no change,
// so the caller can return the client's bytes untouched.
func TestApplyReportsWhetherItChangedTheBody(t *testing.T) {
	plain := map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if _, changed := Apply(plain, "openai", "openai", "m", nil); changed {
		t.Fatal("a body without a reasoning field must report no change")
	}
	withEffort := map[string]any{
		"model":            "m",
		"reasoning_effort": "high",
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if _, changed := Apply(withEffort, "openai", "openai", "m", nil); !changed {
		t.Fatal("a body the pass rewrites must report a change")
	}
}

// TestResolveFormatPrefersTheWireOverTheProviderRegistry pins the dialect
// order: the wire the request goes out on is authoritative. A strict
// Responses upstream rejects a top-level reasoning_effort, so a provider the
// registry records as "openai" must still use the Responses dialect when the
// request goes to a /responses endpoint. Upstream pins the same rule for one
// provider by name; this port applies it to every provider on that lane.
func TestResolveFormatPrefersTheWireOverTheProviderRegistry(t *testing.T) {
	out := apply(t, "openai-responses", "openrouter", "openai/gpt-5.4",
		map[string]any{"model": "openai/gpt-5.4", "reasoning_effort": "high"}, nil)
	r, _ := out["reasoning"].(map[string]any)
	if r == nil || r["effort"] != "high" {
		t.Fatalf("a Responses wire must use the Responses dialect, got %+v", out)
	}
	if _, has := out["reasoning_effort"]; has {
		t.Fatalf("a Responses wire must not carry a top-level reasoning_effort, got %+v", out)
	}
}

// TestResolveFormatUsesTheProviderRegistryOffTheResponsesWire pins the rest
// of the order: off that lane the provider registry decides, and a provider
// it describes keeps the dialect it speaks.
func TestResolveFormatUsesTheProviderRegistryOffTheResponsesWire(t *testing.T) {
	out := apply(t, "openai", "openrouter", "openai/gpt-5.4",
		map[string]any{"model": "openai/gpt-5.4", "reasoning_effort": "high"}, nil)
	if out["reasoning_effort"] != "high" {
		t.Fatalf("a provider the registry records as openai keeps its dialect, got %+v", out)
	}
}

func TestApplyStepOmitsDisable(t *testing.T) {
	out := apply(t, "openai", "cline", "step-5-preview",
		map[string]any{"model": "step-5-preview", "reasoning_effort": "none"}, nil)
	mustAbsent(t, out, "reasoning_effort", "thinking", "enable_thinking")
}

func TestApplyStepClampsMaxToHigh(t *testing.T) {
	for _, lvl := range []string{"max", "xhigh"} {
		out := apply(t, "openai", "deepseek", "step-5-preview",
			map[string]any{"reasoning_effort": lvl}, nil)
		if out["reasoning_effort"] != "high" {
			t.Fatalf("%s → %+v", lvl, out)
		}
	}
	out := apply(t, "openai", "deepseek", "step-5-preview",
		map[string]any{"reasoning_effort": "medium"}, nil)
	if out["reasoning_effort"] != "medium" {
		t.Fatalf("medium → %+v", out)
	}
}

func TestApplyRewritesSuffixedModel(t *testing.T) {
	out := apply(t, "openai", "deepseek", "deepseek-v4-pro(high)",
		map[string]any{"model": "deepseek-v4-pro(high)", "messages": []any{}}, nil)
	if out["model"] != "deepseek-v4-pro" {
		t.Fatalf("model = %v", out["model"])
	}
	// deepseek format maps high→high on the deepseek writer.
	if out["reasoning_effort"] != "high" {
		t.Fatalf("effort = %+v", out)
	}
	out = apply(t, "openai", "deepseek", "step-5-preview(none)",
		map[string]any{"model": "step-5-preview(none)", "reasoning_effort": "high"}, nil)
	if out["model"] != "step-5-preview" {
		t.Fatalf("model = %v", out["model"])
	}
	mustAbsent(t, out, "reasoning_effort")
}

func TestApplyGuards(t *testing.T) {
	// Non-reasoning model strips everything.
	out := apply(t, "openai", "openai", "gpt-4o", map[string]any{
		"reasoning_effort": "high",
		"thinking":         map[string]any{"type": "enabled"},
	}, nil)
	mustAbsent(t, out, "reasoning_effort", "thinking")
	// No intent on reasoning-capable model: only model key survives decode,
	// nothing added.
	in := map[string]any{"model": "step-5-preview", "messages": []any{}}
	out = apply(t, "openai", "deepseek", "step-5-preview", in, nil)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("intent-free changed: %+v", out)
	}
	// none on cannot-disable model clamps to minimal.
	out = apply(t, "openai", "qwen", "qwq-32b",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["enable_thinking"] != true {
		t.Fatalf("clamp → %+v", out)
	}
	// Guard assertion: cline step model resolves step format.
	if got := providers.GetCapabilitiesForModel("cline", "step-5-preview").ThinkingFormat; got != "step" {
		t.Fatalf("cline step format = %q", got)
	}
}

func TestApplyJSON(t *testing.T) {
	raw := []byte(`{"model":"step-5-preview","reasoning_effort":"none"}`)
	out := ApplyJSON(raw, "openai", "cline", "step-5-preview", nil)
	if strings.Contains(string(out), "reasoning_effort") {
		t.Fatalf("ApplyJSON kept field: %s", out)
	}
	// Fast path: identical bytes back.
	plain := []byte(`{"model":"m","messages":[]}`)
	if got := ApplyJSON(plain, "openai", "openai", "m", nil); string(got) != string(plain) {
		t.Fatalf("fast path changed: %s", got)
	}
	// Body-carried suffix strips even when the passed model is already clean
	// (dispatch callers write the requested id into the body).
	suffixed := []byte(`{"model":"step-5-preview(none)","messages":[]}`)
	if got := ApplyJSON(suffixed, "openai", "deepseek", "step-5-preview", nil); strings.Contains(string(got), "(none)") {
		t.Fatalf("body suffix kept: %s", got)
	}
	// Malformed: unchanged.
	bad := []byte(`{nope`)
	if got := ApplyJSON(bad, "openai", "openai", "m(none)", nil); string(got) != string(bad) {
		t.Fatalf("malformed changed: %s", got)
	}
	// Idempotent.
	once := ApplyJSON([]byte(`{"model":"m","reasoning_effort":"high"}`), "openai", "openai", "gpt-5", nil)
	twice := ApplyJSON(once, "openai", "openai", "gpt-5", nil)
	if !reflect.DeepEqual(mustUnmarshal(t, string(once)), mustUnmarshal(t, string(twice))) {
		t.Fatalf("not idempotent:\n%s\n%s", once, twice)
	}
}

func mustUnmarshal(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func sameValue(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ab) == string(bb)
}
