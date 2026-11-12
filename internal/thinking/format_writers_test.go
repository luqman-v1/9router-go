package thinking

import (
	"reflect"
	"testing"

	"9router/proxy/internal/providers"
)

func TestApplyClaudeAdaptive(t *testing.T) {
	out := apply(t, "claude", "claude", "claude-opus-4.7",
		map[string]any{"reasoning_effort": "high"}, nil)
	if !reflect.DeepEqual(out["output_config"], map[string]any{"effort": "high"}) {
		t.Fatalf("output_config = %+v", out["output_config"])
	}
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "adaptive"}) {
		t.Fatalf("thinking = %+v", out["thinking"])
	}
	// Permanently adaptive (cannot disable): no thinking switch.
	out = apply(t, "claude", "claude", "claude-fable-5-1",
		map[string]any{"reasoning_effort": "high"}, nil)
	mustAbsent(t, out, "thinking")
	if !reflect.DeepEqual(out["output_config"], map[string]any{"effort": "high"}) {
		t.Fatalf("fable output_config = %+v", out["output_config"])
	}
	// auto → high.
	out = apply(t, "claude", "claude", "claude-opus-4.7",
		map[string]any{"thinking": map[string]any{"type": "adaptive"}}, nil)
	if !reflect.DeepEqual(out["output_config"], map[string]any{"effort": "high"}) {
		t.Fatalf("auto output_config = %+v", out["output_config"])
	}
}

func TestApplyClaudeBudget(t *testing.T) {
	out := apply(t, "claude", "claude", "claude-haiku-4.5",
		map[string]any{"reasoning_effort": "high"}, nil)
	th, _ := out["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != 24576 {
		t.Fatalf("thinking = %+v", out["thinking"])
	}
}

// TestApplyClaudeAdaptiveOutputFloor pins the level-sized output floor:
// on a model whose thinking can never be switched off (MiniMax-M3.1-Flash
// on minimax-code), a small client max_tokens is eaten whole by reasoning,
// so the answer never starts and the client reads finish_reason "length"
// with no text (OMP's compaction snapshot retries 3x and gives up). The
// writer raises max_tokens to the level budget + 1024 headroom, capped at
// the model's own output limit, so the answer is no longer starved.
func TestApplyClaudeAdaptiveOutputFloor(t *testing.T) {
	out := apply(t, "claude", "minimax-code", "MiniMax-M3.1-Flash-Preview",
		map[string]any{"reasoning_effort": "high", "max_tokens": 512}, nil)
	// high → budget 24576 + 1024 headroom = 25600.
	if n, _ := numberField(out["max_tokens"]); n != 25600 {
		t.Errorf("max_tokens = %v, want 25600 (high budget + headroom)", n)
	}

	// A client budget already above the floor is left alone.
	out = apply(t, "claude", "minimax-code", "MiniMax-M3.1-Flash-Preview",
		map[string]any{"reasoning_effort": "low", "max_tokens": 8000}, nil)
	if n, _ := numberField(out["max_tokens"]); n != 8000 {
		t.Errorf("max_tokens = %v, want 8000 untouched (low floor is 1024+1024)", n)
	}

	// Explicit none on a model that can disable: no floor, the client's
	// small budget is honoured as written.
	out = apply(t, "claude", "claude", "claude-opus-4.7",
		map[string]any{"reasoning_effort": "none", "max_tokens": 512}, nil)
	if n, _ := numberField(out["max_tokens"]); n != 512 {
		t.Errorf("max_tokens = %v, want 512 untouched (disabled reasoning)", n)
	}

	// The other output spellings are raised too: a client that sent
	// max_completion_tokens or max_output_tokens gets the same floor.
	for _, field := range []string{"max_completion_tokens", "max_output_tokens"} {
		out = apply(t, "claude", "minimax-code", "MiniMax-M3.1-Flash-Preview",
			map[string]any{"reasoning_effort": "high", field: 512}, nil)
		if n, _ := numberField(out[field]); n != 25600 {
			t.Errorf("%s = %v, want 25600", field, n)
		}
	}

	// A client budget already above the floor is left alone (the cap
	// branch is pinned at unit level below; the registry has no
	// claude-format model with a MaxOutput under the high floor).
	out = apply(t, "claude", "minimax-code", "MiniMax-M3.1-Flash-Preview",
		map[string]any{"reasoning_effort": "high", "max_tokens": 150000}, nil)
	if n, _ := numberField(out["max_tokens"]); n != 150000 {
		t.Errorf("max_tokens = %v, want 150000 untouched (above the floor)", n)
	}

	// A client that sent no output budget gets one written: the
	// reasoning would otherwise consume an unbounded share.
	out = apply(t, "claude", "minimax-code", "MiniMax-M3.1-Flash-Preview",
		map[string]any{"reasoning_effort": "high"}, nil)
	if n, _ := numberField(out["max_tokens"]); n != 25600 {
		t.Errorf("absent max_tokens = %v, want 25600 (floor written)", n)
	}
}

// TestEnsureClaudeOutputFloorCapped pins the cap branch: the floor is
// clipped to caps.MaxOutput and the raise only lands on the field the
// client actually sent.
func TestEnsureClaudeOutputFloorCapped(t *testing.T) {
	caps := providers.Capabilities{MaxOutput: 4000}
	body := map[string]any{"max_tokens": 512}
	ensureClaudeOutputFloor(body, 25600, caps)
	if n, _ := numberField(body["max_tokens"]); n != 4000 {
		t.Errorf("max_tokens = %v, want 4000 (capped at MaxOutput)", n)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Errorf("max_completion_tokens appeared: %+v", body)
	}
}

// TestApplyOpenAIOutputFloor pins the level-sized output floor on the
// Chat Completions wire: a model whose thinking can never be switched
// off (e.g. glm-5.3-flash-free) spends reasoning inside
// max_completion_tokens, so a small client budget is eaten whole and the
// answer never starts. The writer raises the field the client actually
// sent to level budget + 1024 headroom, capped at MaxOutput.
func TestApplyOpenAIOutputFloor(t *testing.T) {
	out := apply(t, "openai", "glm", "glm-5.3-flash-free",
		map[string]any{"reasoning_effort": "high", "max_completion_tokens": 512}, nil)
	// high → budget 24576 + 1024 headroom = 25600.
	if n, _ := numberField(out["max_completion_tokens"]); n != 25600 {
		t.Errorf("max_completion_tokens = %v, want 25600 (high budget + headroom)", n)
	}
	if _, ok := out["max_tokens"]; ok {
		t.Errorf("max_tokens appeared: %+v", out)
	}

	// A client budget already above the floor is left alone.
	out = apply(t, "openai", "glm", "glm-5.3-flash-free",
		map[string]any{"reasoning_effort": "low", "max_completion_tokens": 8000}, nil)
	if n, _ := numberField(out["max_completion_tokens"]); n != 8000 {
		t.Errorf("max_completion_tokens = %v, want 8000 untouched (low floor is 1024+1024)", n)
	}

	// Explicit none on a model that can disable: no floor, the client's
	// small budget is honoured as written.
	out = apply(t, "openai", "openai", "gpt-5.3-codex",
		map[string]any{"reasoning_effort": "none", "max_tokens": 512}, nil)
	if n, _ := numberField(out["max_tokens"]); n != 512 {
		t.Errorf("max_tokens = %v, want 512 untouched (disabled reasoning)", n)
	}

	// No budget sent: max_tokens (the Chat Completions canonical) is
	// written with the floor.
	out = apply(t, "openai", "glm", "glm-5.3-flash-free",
		map[string]any{"reasoning_effort": "high"}, nil)
	if n, _ := numberField(out["max_tokens"]); n != 25600 {
		t.Errorf("absent max_tokens = %v, want 25600 (floor written)", n)
	}
}

// TestApplyOpenAIResponsesOutputFloor pins the floor on the Responses
// wire, where reasoning is billed out of max_output_tokens.
func TestApplyOpenAIResponsesOutputFloor(t *testing.T) {
	out := apply(t, "openai-responses", "glm", "glm-5.3-flash-free",
		map[string]any{"reasoning_effort": "high", "max_output_tokens": 512}, nil)
	if n, _ := numberField(out["max_output_tokens"]); n != 25600 {
		t.Errorf("max_output_tokens = %v, want 25600 (high budget + headroom)", n)
	}
	if _, ok := out["max_tokens"]; ok {
		t.Errorf("max_tokens appeared: %+v", out)
	}

	// Disabled reasoning on a model that can disable: no floor.
	out = apply(t, "openai-responses", "openai", "gpt-5.3-codex",
		map[string]any{"reasoning_effort": "none", "max_output_tokens": 512}, nil)
	if n, _ := numberField(out["max_output_tokens"]); n != 512 {
		t.Errorf("max_output_tokens = %v, want 512 untouched (disabled reasoning)", n)
	}
}

func TestApplyOpenAI(t *testing.T) {
	out := apply(t, "openai", "openai", "gpt-5.3-codex",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["reasoning_effort"] != "none" {
		t.Fatalf("none → %+v", out)
	}
	// max unsupported → xhigh.
	out = apply(t, "openai", "openai", "gpt-5.3-codex",
		map[string]any{"reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "xhigh" {
		t.Fatalf("max → %+v", out)
	}
	// suffix overrides body.
	out = apply(t, "openai", "openai", "gpt-5(low)",
		map[string]any{"reasoning_effort": "high"}, nil)
	if out["reasoning_effort"] != "low" {
		t.Fatalf("suffix → %+v", out)
	}
}

func TestApplyOpenAIResolvesAutoToHigh(t *testing.T) {
	// "auto" asks the upstream to decide, but strict OpenAI-compatible
	// upstreams reject the literal "auto", so it resolves to "high".
	out := apply(t, "openai", "openai", "gpt-5.3-codex",
		map[string]any{"reasoning": map[string]any{"enabled": true}}, nil)
	if out["reasoning_effort"] != "high" {
		t.Fatalf("auto → %+v", out)
	}
}

func TestApplyZai(t *testing.T) {
	out := apply(t, "openai", "glm", "glm-4.6",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["enable_thinking"] != false {
		t.Fatalf("off → %+v", out)
	}
	mustAbsent(t, out, "thinking")
	for in, want := range map[string]string{
		"high": "high", "max": "max", "xhigh": "max",
		"low": "low", "medium": "high", "minimal": "low",
	} {
		out := apply(t, "openai", "glm-cn", "glm-5.3",
			map[string]any{"reasoning_effort": in}, nil)
		if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "enabled"}) {
			t.Fatalf("%s thinking = %+v", in, out["thinking"])
		}
		if out["reasoning_effort"] != want {
			t.Fatalf("%s → %v, want %s", in, out["reasoning_effort"], want)
		}
	}
	// Pre-5.2: no reasoning_effort.
	out = apply(t, "openai", "glm-cn", "glm-4.7",
		map[string]any{"reasoning_effort": "low"}, nil)
	mustAbsent(t, out, "reasoning_effort")
}

func TestApplyQwenDeepseekKimiMinimaxHunyuan(t *testing.T) {
	out := apply(t, "openai", "qwen", "qwen3-max",
		map[string]any{"reasoning_effort": "medium"}, nil)
	if out["enable_thinking"] != true || out["thinking_budget"] != 8192 {
		t.Fatalf("qwen → %+v", out)
	}
	// QwQ cannot disable → clamped on.
	out = apply(t, "openai", "qwen", "qwq-32b",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["enable_thinking"] != true {
		t.Fatalf("qwq → %+v", out)
	}
	out = apply(t, "openai", "deepseek", "deepseek-v4-pro",
		map[string]any{"reasoning_effort": "low"}, nil)
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "enabled"}) || out["reasoning_effort"] != "high" {
		t.Fatalf("deepseek → %+v", out)
	}
	// Strict DeepSeek-compatible nodes preserve explicit budget_tokens (decolua/9router #5108)
	out = apply(t, "openai", "openai-compatible-sglang", "deepseek-v4-pro",
		map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 4096.0}}, nil)
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "enabled", "budget_tokens": 4096}) {
		t.Fatalf("openai-compatible deepseek budget → %+v", out["thinking"])
	}
	// Registered deepseek provider keeps legacy bare marker
	out = apply(t, "openai", "deepseek", "deepseek-v4-pro",
		map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 4096.0}}, nil)
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "enabled"}) {
		t.Fatalf("registered deepseek budget → %+v", out["thinking"])
	}
	out = apply(t, "openai", "kimi", "kimi-k2.6",
		map[string]any{"reasoning_effort": "high"}, nil)
	if out["reasoning_effort"] != "high" {
		t.Fatalf("kimi → %+v", out)
	}
	out = apply(t, "openai", "kimchi", "kimi-k2.7",
		map[string]any{"reasoning_effort": "auto"}, nil)
	if out["reasoning_effort"] != "high" {
		t.Fatalf("kimi auto → %+v", out)
	}
	out = apply(t, "openai", "kimchi", "kimi-k2.7",
		map[string]any{"reasoning_effort": "minimal"}, nil)
	if out["reasoning_effort"] != "low" {
		t.Fatalf("kimi minimal → %+v", out)
	}
	out = apply(t, "claude", "minimax", "MiniMax-M3",
		map[string]any{"reasoning_effort": "high"}, nil)
	if !reflect.DeepEqual(out["thinking"], map[string]any{"type": "adaptive"}) {
		t.Fatalf("minimax → %+v", out)
	}
	out = apply(t, "openai", "hunyuan", "hunyuan-turbos",
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
		out := apply(t, "openai", "tokenrouter", "step-5-preview", body, nil)
		mustAbsent(t, out, "reasoning_effort")
	}
	out := apply(t, "openai", "tokenrouter", "step-5-preview",
		map[string]any{"reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "max" {
		t.Fatalf("tokenrouter max → %+v", out)
	}
}

// TestApplyTokenRouterClampsDisableOnAModelThatCannotDisable pins that the
// tokenrouter dialect honours the none-on-cannot-disable clamp: the model
// cannot switch reasoning off, so the request lands on the lowest effort
// instead of on no field at all. The lowest effort is "low": the dialect's
// enum is low/medium/high/xhigh/max, and "minimal" is not one of them.
func TestApplyTokenRouterClampsDisableOnAModelThatCannotDisable(t *testing.T) {
	out := apply(t, "openai", "tokenrouter", "z-ai/glm-5.3-free",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["reasoning_effort"] != "low" {
		t.Fatalf("a model that cannot disable must receive the enum floor, got %+v", out)
	}
}

// TestApplyClaudeBudgetStaysAboveTheApiFloor pins the budget floor: the
// Messages API rejects a budget below 1024, and the none→minimal clamp feeds
// this writer the "minimal" level, whose budget is 512.
func TestApplyClaudeBudgetStaysAboveTheApiFloor(t *testing.T) {
	out := apply(t, "claude", "claude", "claude-haiku-4.5",
		map[string]any{"reasoning_effort": "minimal"}, nil)
	th, _ := out["thinking"].(map[string]any)
	if th["type"] != "enabled" {
		t.Fatalf("claude budget → %+v", out)
	}
	if th["budget_tokens"] != ClaudeMinThinkingBudget {
		t.Fatalf("claude budget = %v, want the API floor %d", th["budget_tokens"], ClaudeMinThinkingBudget)
	}
}

// TestApplyCommandcodeThroughTheCapabilityOverlay pins the route production
// takes: the dispatch never names a "commandcode" target, so the writer is
// reached through the provider capability overlay at the ordinary target.
func TestApplyCommandcodeThroughTheCapabilityOverlay(t *testing.T) {
	out := apply(t, "openai", "commandcode", "deepseek/deepseek-v4-pro",
		map[string]any{"model": "deepseek/deepseek-v4-pro", "reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "max" {
		t.Fatalf("overlay route → %+v", out)
	}
}

func TestApplyCommandcode(t *testing.T) {
	out := apply(t, "commandcode", "commandcode", "deepseek/deepseek-v4.1-flash", map[string]any{
		"params":           map[string]any{"model": "deepseek/deepseek-v4.1-flash"},
		"reasoning_effort": "high",
	}, nil)
	if out["params"].(map[string]any)["reasoning_effort"] != "high" {
		t.Fatalf("envelope → %+v", out)
	}
	mustAbsent(t, out, "reasoning_effort", "thinking")
	// Flat Go body: top-level write, wrapped later by buildCommandcodeBody.
	out = apply(t, "commandcode", "commandcode", "deepseek-v4-pro",
		map[string]any{"reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "max" {
		t.Fatalf("flat → %+v", out)
	}
}

func TestApplyDisplay(t *testing.T) {
	out := apply(t, "claude", "claude", "claude-haiku-4.5",
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
			// The exact glm-5.2 entry repeats the pattern's
			// ThinkingEffortSupported (upstream b6a9ca43), so the clamp carries
			// an effort level the way glm-5.3's does. Before that upstream fix
			// the entry omitted the flag and the effort was dropped.
			"glm-5.2 clamps none and carries the effort", "glm-5.2",
			`{"model":"glm-5.2","reasoning_effort":"none"}`,
			map[string]any{"reasoning_effort": "low", "thinking": map[string]any{"type": "enabled"}},
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
			out := apply(t, "openai", "", tt.model, mustUnmarshal(t, tt.in), nil)
			for _, key := range []string{"thinking", "reasoning_effort", "enable_thinking", "reasoning"} {
				if !sameValue(out[key], tt.want[key]) {
					t.Errorf("%s = %#v, want %#v (full: %#v)", key, out[key], tt.want[key], out)
				}
			}
		})
	}
}
