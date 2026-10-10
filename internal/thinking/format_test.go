package thinking

import (
	"9router/proxy/internal/providers"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// apply builds a fresh body, runs Apply, and returns it.
func apply(target, provider, model string, body map[string]any, override *Intent) map[string]any {
	b, _ := json.Marshal(body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		panic(err)
	}
	return Apply(m, target, provider, model, override)
}

func mustAbsent(t *testing.T, body map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := body[k]; ok {
			t.Fatalf("expected no %q in %+v", k, body)
		}
	}
}

func TestApplyStepOmitsDisable(t *testing.T) {
	out := apply("openai", "cline", "step-5-preview",
		map[string]any{"model": "step-5-preview", "reasoning_effort": "none"}, nil)
	mustAbsent(t, out, "reasoning_effort", "thinking", "enable_thinking")
}

func TestApplyStepClampsMaxToHigh(t *testing.T) {
	for _, lvl := range []string{"max", "xhigh"} {
		out := apply("openai", "deepseek", "step-5-preview",
			map[string]any{"reasoning_effort": lvl}, nil)
		if out["reasoning_effort"] != "high" {
			t.Fatalf("%s → %+v", lvl, out)
		}
	}
	out := apply("openai", "deepseek", "step-5-preview",
		map[string]any{"reasoning_effort": "medium"}, nil)
	if out["reasoning_effort"] != "medium" {
		t.Fatalf("medium → %+v", out)
	}
}

func TestApplyRewritesSuffixedModel(t *testing.T) {
	out := apply("openai", "deepseek", "deepseek-v4-pro(high)",
		map[string]any{"model": "deepseek-v4-pro(high)", "messages": []any{}}, nil)
	if out["model"] != "deepseek-v4-pro" {
		t.Fatalf("model = %v", out["model"])
	}
	// deepseek format maps high→high on the deepseek writer.
	if out["reasoning_effort"] != "high" {
		t.Fatalf("effort = %+v", out)
	}
	out = apply("openai", "deepseek", "step-5-preview(none)",
		map[string]any{"model": "step-5-preview(none)", "reasoning_effort": "high"}, nil)
	if out["model"] != "step-5-preview" {
		t.Fatalf("model = %v", out["model"])
	}
	mustAbsent(t, out, "reasoning_effort")
}

func TestApplyClaudeAdaptive(t *testing.T) {
	out := apply("claude", "claude", "claude-opus-4.7",
		map[string]any{"reasoning_effort": "high"}, nil)
	if !reflect.DeepEqual(out["output_config"], map[string]any{"effort": "high"}) {
		t.Fatalf("output_config = %+v", out["output_config"])
	}
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "adaptive"}) {
		t.Fatalf("thinking = %+v", out["thinking"])
	}
	// Permanently adaptive (cannot disable): no thinking switch.
	out = apply("claude", "claude", "claude-fable-5-1",
		map[string]any{"reasoning_effort": "high"}, nil)
	mustAbsent(t, out, "thinking")
	if !reflect.DeepEqual(out["output_config"], map[string]any{"effort": "high"}) {
		t.Fatalf("fable output_config = %+v", out["output_config"])
	}
	// auto → high.
	out = apply("claude", "claude", "claude-opus-4.7",
		map[string]any{"thinking": map[string]any{"type": "adaptive"}}, nil)
	if !reflect.DeepEqual(out["output_config"], map[string]any{"effort": "high"}) {
		t.Fatalf("auto output_config = %+v", out["output_config"])
	}
}

func TestApplyClaudeBudget(t *testing.T) {
	out := apply("claude", "claude", "claude-haiku-4.5",
		map[string]any{"reasoning_effort": "high"}, nil)
	th, _ := out["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != 24576 {
		t.Fatalf("thinking = %+v", out["thinking"])
	}
}

func TestApplyOpenAI(t *testing.T) {
	out := apply("openai", "openai", "gpt-5.3-codex",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["reasoning_effort"] != "none" {
		t.Fatalf("none → %+v", out)
	}
	// max unsupported → xhigh.
	out = apply("openai", "openai", "gpt-5.3-codex",
		map[string]any{"reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "xhigh" {
		t.Fatalf("max → %+v", out)
	}
	// suffix overrides body.
	out = apply("openai", "openai", "gpt-5(low)",
		map[string]any{"reasoning_effort": "high"}, nil)
	if out["reasoning_effort"] != "low" {
		t.Fatalf("suffix → %+v", out)
	}
}

func TestApplyZai(t *testing.T) {
	out := apply("openai", "glm", "glm-4.6",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["enable_thinking"] != false {
		t.Fatalf("off → %+v", out)
	}
	mustAbsent(t, out, "thinking")
	for in, want := range map[string]string{
		"high": "high", "max": "max", "xhigh": "max",
		"low": "low", "medium": "high", "minimal": "low",
	} {
		out := apply("openai", "glm-cn", "glm-5.3",
			map[string]any{"reasoning_effort": in}, nil)
		if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "enabled"}) {
			t.Fatalf("%s thinking = %+v", in, out["thinking"])
		}
		if out["reasoning_effort"] != want {
			t.Fatalf("%s → %v, want %s", in, out["reasoning_effort"], want)
		}
	}
	// Pre-5.2: no reasoning_effort.
	out = apply("openai", "glm-cn", "glm-4.7",
		map[string]any{"reasoning_effort": "low"}, nil)
	mustAbsent(t, out, "reasoning_effort")
}

func TestApplyQwenDeepseekKimiMinimaxHunyuan(t *testing.T) {
	out := apply("openai", "qwen", "qwen3-max",
		map[string]any{"reasoning_effort": "medium"}, nil)
	if out["enable_thinking"] != true || out["thinking_budget"] != 8192 {
		t.Fatalf("qwen → %+v", out)
	}
	// QwQ cannot disable → clamped on.
	out = apply("openai", "qwen", "qwq-32b",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["enable_thinking"] != true {
		t.Fatalf("qwq → %+v", out)
	}
	out = apply("openai", "deepseek", "deepseek-v4-pro",
		map[string]any{"reasoning_effort": "low"}, nil)
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "enabled"}) || out["reasoning_effort"] != "high" {
		t.Fatalf("deepseek → %+v", out)
	}
	out = apply("openai", "kimi", "kimi-k2.6",
		map[string]any{"reasoning_effort": "high"}, nil)
	if out["reasoning_effort"] != "high" {
		t.Fatalf("kimi → %+v", out)
	}
	out = apply("openai", "kimchi", "kimi-k2.7",
		map[string]any{"reasoning_effort": "auto"}, nil)
	if out["reasoning_effort"] != "high" {
		t.Fatalf("kimi auto → %+v", out)
	}
	out = apply("openai", "kimchi", "kimi-k2.7",
		map[string]any{"reasoning_effort": "minimal"}, nil)
	if out["reasoning_effort"] != "low" {
		t.Fatalf("kimi minimal → %+v", out)
	}
	out = apply("claude", "minimax", "MiniMax-M3",
		map[string]any{"reasoning_effort": "high"}, nil)
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "adaptive"}) {
		t.Fatalf("minimax → %+v", out)
	}
	out = apply("openai", "hunyuan", "hunyuan-turbos",
		map[string]any{"reasoning_effort": "medium"}, nil)
	th, _ := out["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != 8192 {
		t.Fatalf("hunyuan → %+v", out)
	}
}

func TestApplyTokenRouter(t *testing.T) {
	// step-5-preview is reasoning-capable; the provider override resolves
	// the tokenrouter writer (commit 2 populates the registry value).
	for _, body := range []map[string]any{
		{"reasoning_effort": "none"}, {"reasoning_effort": "auto"},
	} {
		out := apply("openai", "tokenrouter", "step-5-preview", body, nil)
		mustAbsent(t, out, "reasoning_effort")
	}
	out := apply("openai", "tokenrouter", "step-5-preview",
		map[string]any{"reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "max" {
		t.Fatalf("tokenrouter max → %+v", out)
	}
}

func TestApplyCommandcode(t *testing.T) {
	out := apply("commandcode", "commandcode", "deepseek/deepseek-v4.1-flash", map[string]any{
		"params":           map[string]any{"model": "deepseek/deepseek-v4.1-flash"},
		"reasoning_effort": "high",
	}, nil)
	if out["params"].(map[string]any)["reasoning_effort"] != "high" {
		t.Fatalf("envelope → %+v", out)
	}
	mustAbsent(t, out, "reasoning_effort", "thinking")
	// Flat Go body: top-level write, wrapped later by buildCommandcodeBody.
	out = apply("commandcode", "commandcode", "deepseek-v4-pro",
		map[string]any{"reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "max" {
		t.Fatalf("flat → %+v", out)
	}
}

func TestApplyGuards(t *testing.T) {
	// Non-reasoning model strips everything.
	out := apply("openai", "openai", "gpt-4o", map[string]any{
		"reasoning_effort": "high",
		"thinking":         map[string]any{"type": "enabled"},
	}, nil)
	mustAbsent(t, out, "reasoning_effort", "thinking")
	// No intent on reasoning-capable model: only model key survives decode,
	// nothing added.
	in := map[string]any{"model": "step-5-preview", "messages": []any{}}
	out = apply("openai", "deepseek", "step-5-preview", in, nil)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("intent-free changed: %+v", out)
	}
	// none on cannot-disable model clamps to minimal.
	out = apply("openai", "qwen", "qwq-32b",
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
	if string(once) != string(twice) {
		t.Fatalf("not idempotent:\n%s\n%s", once, twice)
	}
}

func TestApplyDisplay(t *testing.T) {
	out := apply("claude", "claude", "claude-haiku-4.5",
		map[string]any{
			"thinking":         map[string]any{"type": "enabled", "budget_tokens": 4096.0, "display": "summarized"},
			"reasoning_effort": "high",
		}, nil)
	th, _ := out["thinking"].(map[string]any)
	if th["display"] != "summarized" {
		t.Fatalf("display lost: %+v", out["thinking"])
	}
}

// TestApplyZaiPorted pins every expectation the retired ForwardZai rewrite
// (internal/proxy/executor/zai.go) encoded, now owned by the unified writer.
func TestApplyZaiPorted(t *testing.T) {
	tests := []struct {
		name  string
		model string
		in    string
		want  map[string]any
	}{
		{
			"glm-5.3 clamps none to low", "glm-5.3",
			`{"model":"glm-5.3","reasoning_effort":"none"}`,
			map[string]any{"reasoning_effort": "low", "thinking": map[string]any{"type": "enabled"}},
		},
		{
			"glm-5.3-flash clamps none, drops effort", "glm-5.3-flash",
			`{"model":"glm-5.3-flash","reasoning_effort":"none"}`,
			map[string]any{"thinking": map[string]any{"type": "enabled"}},
		},
		{
			"glm-5.2 clamps none", "glm-5.2",
			`{"model":"glm-5.2","reasoning_effort":"none"}`,
			map[string]any{"thinking": map[string]any{"type": "enabled"}},
		},
		{
			"switchable model disables via enable_thinking", "glm-4.6v",
			`{"model":"glm-4.6v","reasoning_effort":"none"}`,
			map[string]any{"enable_thinking": false},
		},
		{
			"off reads as none", "glm-4.6v",
			`{"model":"glm-4.6v","reasoning_effort":"off"}`,
			map[string]any{"enable_thinking": false},
		},
		{
			"medium maps to high", "glm-5.3",
			`{"model":"glm-5.3","reasoning_effort":"medium"}`,
			map[string]any{"reasoning_effort": "high", "thinking": map[string]any{"type": "enabled"}},
		},
		{
			"xhigh becomes max", "glm-5.3",
			`{"model":"glm-5.3","reasoning_effort":"xhigh"}`,
			map[string]any{"reasoning_effort": "max", "thinking": map[string]any{"type": "enabled"}},
		},
		{
			"older model drops reasoning_effort", "glm-4.6v",
			`{"model":"glm-4.6v","reasoning_effort":"high"}`,
			map[string]any{"thinking": map[string]any{"type": "enabled"}},
		},
		{
			"claude disabled becomes enable_thinking", "glm-4.6v",
			`{"model":"glm-4.6v","thinking":{"type":"disabled"}}`,
			map[string]any{"enable_thinking": false},
		},
		{
			"nested reasoning consumed", "glm-5.3",
			`{"model":"glm-5.3","reasoning":{"effort":"high"}}`,
			map[string]any{"reasoning": nil, "reasoning_effort": "high", "thinking": map[string]any{"type": "enabled"}},
		},
		{
			"output_config wins", "glm-5.3",
			`{"model":"glm-5.3","output_config":{"effort":"low"},"reasoning_effort":"high"}`,
			map[string]any{"reasoning_effort": "low", "thinking": map[string]any{"type": "enabled"}},
		},
		{
			"non reasoning stripped", "gpt-4o",
			`{"model":"gpt-4o","reasoning_effort":"high"}`,
			map[string]any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := apply("openai", "", tt.model, mustUnmarshal(t, tt.in), nil)
			for _, key := range []string{"thinking", "reasoning_effort", "enable_thinking", "reasoning"} {
				if !sameValue(out[key], tt.want[key]) {
					t.Errorf("%s = %#v, want %#v (full: %#v)", key, out[key], tt.want[key], out)
				}
			}
		})
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
