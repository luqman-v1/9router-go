package executor

import (
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// TestEnsureClaudeMessages_ThinkingDisplay covers the request-side half of
// upstream's "return Claude thinking text to OpenAI-format clients": an
// OpenAI client asks for reasoning with reasoning_effort (Chat Completions) or
// reasoning.summary (Responses), and that ask has to become
// thinking.display "summarized" before the OpenAI-only keys are stripped —
// otherwise Claude returns signature-only thinking and the client sees nothing.
func TestEnsureClaudeMessages_ThinkingDisplay(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantDisplay string
	}{
		{
			name:        "chat completions effort becomes summarized",
			body:        `{"model":"m","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "summarized",
		},
		{
			name:        "minimal effort still means reasoning",
			body:        `{"model":"m","reasoning_effort":"minimal","messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "summarized",
		},
		{
			name:        "responses summary becomes summarized",
			body:        `{"model":"m","reasoning":{"summary":"auto"},"messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "summarized",
		},
		{
			name:        "no reasoning ask leaves the field alone",
			body:        `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "",
		},
		{
			name:        "an explicit opt-out asks for no thinking at all",
			body:        `{"model":"m","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "",
		},
		{
			name:        "responses summary none is not a request",
			body:        `{"model":"m","reasoning":{"summary":"none"},"messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "",
		},
		{
			name:        "an explicit display on the body wins",
			body:        `{"model":"m","thinking":{"display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "summarized",
		},
		{
			name:        "an explicit display is not overwritten",
			body:        `{"model":"m","thinking":{"display":"none"},"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
			wantDisplay: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := EnsureClaudeMessages([]byte(tt.body), "m")

			wantSummaries := tt.wantDisplay == "summarized"
			if gotSummaries := WantsThinkingSummaries(out); gotSummaries != wantSummaries {
				t.Fatalf("WantsThinkingSummaries = %v, want %v (body %s)", gotSummaries, wantSummaries, out)
			}
			if tt.wantDisplay == "" {
				return
			}
			display := thinkingDisplay(t, out)
			if display != tt.wantDisplay {
				t.Errorf("thinking.display = %q, want %q", display, tt.wantDisplay)
			}
		})
	}
}

// TestEnsureClaudeMessages_StripsOpenAIOnlyFields makes sure capturing the
// intent did not reintroduce the keys the Messages API rejects.
func TestEnsureClaudeMessages_StripsOpenAIOnlyFields(t *testing.T) {
	out := EnsureClaudeMessages(
		[]byte(`{"model":"m","reasoning_effort":"high","stream_options":{"x":1},"store":true,"messages":[{"role":"user","content":"hi"}]}`),
		"m",
	)

	for _, key := range []string{"reasoning_effort", "stream_options", "store"} {
		if hasJSONKey(out, key) {
			t.Errorf("%q survived the conversion: %s", key, out)
		}
	}
	if !WantsThinkingSummaries(out) {
		t.Errorf("thinking.display was dropped along with reasoning_effort: %s", out)
	}
}

// TestWithoutBetaFlag_AnthropicRedactThinking is the other half: the flag that
// asks Anthropic for signature-only thinking has to leave the header when the
// body wants summaries, without touching the shared registry map.
func TestWithoutBetaFlag_AnthropicRedactThinking(t *testing.T) {
	shared := map[string]string{
		"anthropic-version": "2023-06-01",
		"Anthropic-Beta":    "claude-code-20250219," + providers.AnthropicBetaRedactThinking + ",fast-mode-2026-02-01",
	}

	got := providers.WithoutBetaFlag(shared, providers.AnthropicBetaRedactThinking)

	if hasBeta(got["Anthropic-Beta"], providers.AnthropicBetaRedactThinking) {
		t.Errorf("redact-thinking still present in %q", got["Anthropic-Beta"])
	}
	if !hasBeta(got["Anthropic-Beta"], "claude-code-20250219") || !hasBeta(got["Anthropic-Beta"], "fast-mode-2026-02-01") {
		t.Errorf("unrelated flags lost: %q", got["Anthropic-Beta"])
	}
	if got["anthropic-version"] != "2023-06-01" {
		t.Errorf("other headers lost: %v", got)
	}
	if !hasBeta(shared["Anthropic-Beta"], providers.AnthropicBetaRedactThinking) {
		t.Error("the shared registry header map was mutated")
	}

	t.Run("a header without the flag is returned unchanged", func(t *testing.T) {
		plain := map[string]string{"Anthropic-Beta": "fast-mode-2026-02-01"}
		if out := providers.WithoutBetaFlag(plain, providers.AnthropicBetaRedactThinking); out["Anthropic-Beta"] != "fast-mode-2026-02-01" {
			t.Errorf("got %q", out["Anthropic-Beta"])
		}
	})
	t.Run("a missing Anthropic-Beta header is left alone", func(t *testing.T) {
		if out := providers.WithoutBetaFlag(map[string]string{}, "x"); len(out) != 0 {
			t.Errorf("got %v", out)
		}
	})
}

// hasBeta reports whether a comma-separated Anthropic-Beta header carries flag.
func hasBeta(header, flag string) bool {
	for f := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(f) == flag {
			return true
		}
	}
	return false
}

// thinkingDisplay reads thinking.display out of a converted Claude body.
func thinkingDisplay(t *testing.T, body []byte) string {
	t.Helper()
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	thinking, _ := reqMap["thinking"].(map[string]any)
	display, _ := thinking["display"].(string)
	return display
}

// hasJSONKey reports whether a top-level key is present.
func hasJSONKey(body []byte, key string) bool {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return false
	}
	_, ok := reqMap[key]
	return ok
}

// TestEnsureClaudeMessages_ThinkingType: an OpenAI client's reasoning_effort
// must yield a Claude `thinking` object that carries `type` — Anthropic
// answers `400 thinking.type: Field required` otherwise.
func TestEnsureClaudeMessages_ThinkingType(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		model      string
		wantType   string
		wantBudget float64
		wantEffort string
	}{
		{"adaptive model", `{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "adaptive", 0, "high"},
		{"adaptive minimal clamps to low", `{"reasoning_effort":"minimal","messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "adaptive", 0, "low"},
		{"budget model", `{"reasoning_effort":"low","max_tokens":8000,"messages":[{"role":"user","content":"hi"}]}`, "claude-haiku-4-5", "enabled", 1024, ""},
		{"max_tokens raised instead of crushing the budget", `{"reasoning_effort":"high","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`, "claude-haiku-4-5", "enabled", 24576, ""},
		{"fable 5 is adaptive, not budget", `{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, "claude-fable-5", "adaptive", 0, "high"},
		{"responses effort", `{"reasoning":{"effort":"medium"},"messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "adaptive", 0, "medium"},
		{"explicit type is kept", `{"reasoning_effort":"high","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "disabled", 0, ""},
		{"none sends no thinking", `{"reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "", 0, ""},
		{"responses none+summary is disabled, never typeless", `{"reasoning":{"summary":"auto","effort":"none"},"messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "disabled", 0, ""},
		{"responses summary without effort is typed", `{"reasoning":{"summary":"auto"},"messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "adaptive", 0, "medium"},
		{"no ask sends no thinking", `{"messages":[{"role":"user","content":"hi"}]}`, "claude-sonnet-5", "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := EnsureClaudeMessages([]byte(tt.body), tt.model)
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatal(err)
			}
			th, _ := m["thinking"].(map[string]any)
			gotType, _ := th["type"].(string)
			if gotType != tt.wantType {
				t.Fatalf("thinking.type = %q, want %q (body %s)", gotType, tt.wantType, out)
			}
			if tt.wantType == "" && th != nil {
				t.Errorf("thinking should be absent: %s", out)
			}
			if tt.wantBudget != 0 {
				if b, _ := th["budget_tokens"].(float64); b != tt.wantBudget {
					t.Errorf("budget_tokens = %v, want %v", b, tt.wantBudget)
				}
			}
			if tt.wantEffort != "" {
				oc, _ := m["output_config"].(map[string]any)
				if oc["effort"] != tt.wantEffort {
					t.Errorf("output_config.effort = %v, want %q", oc["effort"], tt.wantEffort)
				}
			}
			if _, ok := m["reasoning_effort"]; ok {
				t.Errorf("reasoning_effort survived: %s", out)
			}
		})
	}
}

// TestEnsureClaudeMessages_ThinkingMaxTokens: the budget is kept and max_tokens
// is raised to budget+1024 (capped at the model's output limit), so the answer
// is not starved; only a budget that alone reaches the ceiling is shrunk.
func TestEnsureClaudeMessages_ThinkingMaxTokens(t *testing.T) {
	maxTokensOf := func(out []byte) (budget, maxTokens float64) {
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		th, _ := m["thinking"].(map[string]any)
		budget, _ = th["budget_tokens"].(float64)
		maxTokens, _ = m["max_tokens"].(float64)
		return
	}

	t.Run("default max_tokens is raised", func(t *testing.T) {
		out := EnsureClaudeMessages([]byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`), "claude-haiku-4-5")
		if b, mt := maxTokensOf(out); b != 24576 || mt != 25600 {
			t.Errorf("budget=%v max_tokens=%v, want 24576/25600 (%s)", b, mt, out)
		}
	})
	t.Run("a larger client max_tokens is untouched", func(t *testing.T) {
		out := EnsureClaudeMessages([]byte(`{"reasoning_effort":"low","max_tokens":8000,"messages":[{"role":"user","content":"hi"}]}`), "claude-haiku-4-5")
		if b, mt := maxTokensOf(out); b != 1024 || mt != 8000 {
			t.Errorf("budget=%v max_tokens=%v, want 1024/8000 (%s)", b, mt, out)
		}
	})
}

// TestEnsureClaudeMessagesFor_UsesLaneProvider: minimax-code declares its own
// thinking format; looking the model up under "claude" picks the wrong one.
func TestEnsureClaudeMessagesFor_UsesLaneProvider(t *testing.T) {
	model := "MiniMax-M3"
	want := providers.GetCapabilitiesForModel("minimax-code", model).ThinkingFormat
	if want == providers.GetCapabilitiesForModel("claude", model).ThinkingFormat {
		t.Skip("capability tables agree for this model")
	}
	out := EnsureClaudeMessagesFor([]byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`), "minimax-code", model)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	th, _ := m["thinking"].(map[string]any)
	if want == "claude-adaptive" && th["type"] != "adaptive" {
		t.Errorf("thinking.type = %v, want adaptive (%s)", th["type"], out)
	}
	if want != "claude-adaptive" && th["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled (%s)", th["type"], out)
	}
}

// TestEnsureClaudeMessagesFor_zedHaikuUsesAdaptive: Zed's Anthropic lane
// serves claude-haiku-5-5 with adaptive thinking. The global *claude*haiku*
// pattern emits the budget shape (thinking.type.enabled + budget_tokens),
// which Zed rejects with 400 — the exact error this fix ships against. The
// "zed" lane id must resolve claude-adaptive, while the same id under
// "anthropic" keeps the budget shape other providers expect. Mirrors upstream
// open-sse/providers/capabilities.js PROVIDER_CAPABILITIES override semantics
// (see tests/unit/opencode-go-claude-haiku-55-thinking.test.js).
func TestEnsureClaudeMessagesFor_zedHaikuUsesAdaptive(t *testing.T) {
	model := "claude-haiku-5-5"
	zedCaps := providers.GetCapabilitiesForModel("zed", model)
	if zedCaps.ThinkingFormat != "claude-adaptive" {
		t.Fatalf("zed capability ThinkingFormat = %q, want claude-adaptive", zedCaps.ThinkingFormat)
	}
	otherCaps := providers.GetCapabilitiesForModel("anthropic", model)
	if otherCaps.ThinkingFormat == "claude-adaptive" {
		t.Fatalf("anthropic capability ThinkingFormat = %q; the override must not leak past the zed lane", otherCaps.ThinkingFormat)
	}
	if otherCaps.ThinkingFormat == "" {
		t.Fatalf("anthropic capability ThinkingFormat is empty, want a budget shape for the same id")
	}

	out := EnsureClaudeMessagesFor([]byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`), "zed", model)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	th, _ := m["thinking"].(map[string]any)
	if th["type"] != "adaptive" {
		t.Errorf("zed thinking.type = %v, want adaptive (%s)", th["type"], out)
	}
	if _, hasBudget := th["budget_tokens"]; hasBudget {
		t.Errorf("zed thinking carries budget_tokens = %v; adaptive must have none (%s)", th["budget_tokens"], out)
	}
	oc, _ := m["output_config"].(map[string]any)
	if oc["effort"] == nil || oc["effort"] == "" {
		t.Errorf("zed output_config.effort missing, want the effort depth (%s)", out)
	}

	// The same id on another lane must keep its own thinking shape: Zed's
	// override must not leak into the shared Anthropic conversion.
	otherOut := EnsureClaudeMessagesFor([]byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`), "anthropic", model)
	var other map[string]any
	if err := json.Unmarshal(otherOut, &other); err != nil {
		t.Fatal(err)
	}
	oth, _ := other["thinking"].(map[string]any)
	if oth["type"] == "adaptive" {
		t.Errorf("anthropic thinking.type = %v, want the budget shape (%s)", oth["type"], otherOut)
	}
	if _, hasBudget := oth["budget_tokens"]; !hasBudget {
		t.Errorf("anthropic thinking lacks budget_tokens; the override leaked (%s)", otherOut)
	}
}

// TestOpenAIThinkingDisplaySecondCopy dispositions every client key read by the
// executor's own copy of the display reader (claude_thinking.go), which the
// Claude path keeps because it runs during translation, before the OpenAI-only
// keys are stripped. The fall-through rows differ from
// thinking.TestOpenAIThinkingDisplayDialects, which short-circuits once a
// `reasoning` object is present: the divergence is documented as tested, not
// changed.
func TestOpenAIThinkingDisplaySecondCopy(t *testing.T) {
	tests := []struct {
		name   string
		reqMap map[string]any
		want   string
	}{
		{"reasoning.summary detailed", map[string]any{"reasoning": map[string]any{"summary": "detailed"}}, "summarized"},
		{"reasoning.summary auto", map[string]any{"reasoning": map[string]any{"summary": "auto"}}, "summarized"},
		{"reasoning.summary none alone is not a request", map[string]any{"reasoning": map[string]any{"summary": "none"}}, ""},
		{"reasoning empty summary falls through to effort", map[string]any{"reasoning": map[string]any{"summary": ""}, "reasoning_effort": "high"}, "summarized"},
		{"reasoning summary none falls through to effort", map[string]any{"reasoning": map[string]any{"summary": "none"}, "reasoning_effort": "high"}, "summarized"},
		{"reasoning.effort only falls through to top-level effort", map[string]any{"reasoning": map[string]any{"effort": "high"}, "reasoning_effort": "high"}, "summarized"},
		{"reasoning non-map falls through to effort", map[string]any{"reasoning": "x", "reasoning_effort": "high"}, "summarized"},
		{"reasoning_effort high", map[string]any{"reasoning_effort": "high"}, "summarized"},
		{"reasoning_effort minimal still asks for reasoning", map[string]any{"reasoning_effort": "minimal"}, "summarized"},
		{"reasoning_effort none opts out", map[string]any{"reasoning_effort": "none"}, ""},
		{"reasoning_effort NONE opts out case-insensitively", map[string]any{"reasoning_effort": "NONE"}, ""},
		{"reasoning_effort off opts out", map[string]any{"reasoning_effort": "off"}, ""},
		{"reasoning_effort minimal_off opts out", map[string]any{"reasoning_effort": "minimal_off"}, ""},
		{"reasoning_effort empty is not a request", map[string]any{"reasoning_effort": ""}, ""},
		{"reasoning_effort non-string is not a request", map[string]any{"reasoning_effort": 5.0}, ""},
		{"claude-native thinking block asks for nothing", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1000.0}}, ""},
		{"body without reasoning keys asks for nothing", map[string]any{"model": "m"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openAIThinkingDisplay(tt.reqMap); got != tt.want {
				t.Fatalf("openAIThinkingDisplay() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyClaudeThinkingDisplayDialectKeys dispositions how the second copy
// writes its result back: an explicit `thinking.display` is client-owned and
// survives, and no block is created unless the body actually asked.
func TestApplyClaudeThinkingDisplayDialectKeys(t *testing.T) {
	tests := []struct {
		name     string
		reqMap   map[string]any
		wantKeys map[string]any // top-level keys that must remain absent
		want     map[string]any // full expected body, when non-nil
	}{
		{
			name: "explicit summarized display wins",
			reqMap: map[string]any{"thinking": map[string]any{"display": "summarized"},
				"reasoning_effort": "high"},
			want: map[string]any{"thinking": map[string]any{"display": "summarized"}, "reasoning_effort": "high"},
		},
		{
			name: "explicit none display is not overwritten",
			reqMap: map[string]any{"thinking": map[string]any{"display": "none"},
				"reasoning_effort": "high"},
			want: map[string]any{"thinking": map[string]any{"display": "none"}, "reasoning_effort": "high"},
		},
		{
			name:   "empty display counts as unset",
			reqMap: map[string]any{"thinking": map[string]any{"display": ""}, "reasoning_effort": "high"},
			want:   map[string]any{"thinking": map[string]any{"display": "summarized"}, "reasoning_effort": "high"},
		},
		{
			name:   "non-string display counts as unset",
			reqMap: map[string]any{"thinking": map[string]any{"display": 5.0}, "reasoning_effort": "high"},
			want:   map[string]any{"thinking": map[string]any{"display": "summarized"}, "reasoning_effort": "high"},
		},
		{
			name:   "thinking non-map is replaced",
			reqMap: map[string]any{"thinking": "x", "reasoning_effort": "high"},
			want:   map[string]any{"thinking": map[string]any{"display": "summarized"}, "reasoning_effort": "high"},
		},
		{
			name:   "claude-native thinking fields survive untouched",
			reqMap: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1000.0}},
			want:   map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1000.0}},
		},
		{
			name:     "no ask creates no thinking block",
			reqMap:   map[string]any{"model": "m"},
			wantKeys: map[string]any{"thinking": true},
		},
		{
			name:     "opt-out effort creates no thinking block",
			reqMap:   map[string]any{"model": "m", "reasoning_effort": "none"},
			wantKeys: map[string]any{"thinking": true},
		},
		{
			name:   "responses summary ask writes the display",
			reqMap: map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}},
			want: map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"},
				"thinking": map[string]any{"display": "summarized"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyClaudeThinkingDisplay(tt.reqMap)
			for k := range tt.wantKeys {
				if _, has := tt.reqMap[k]; has {
					t.Errorf("%q written: %+v", k, tt.reqMap)
				}
			}
			if tt.want != nil && !reflect.DeepEqual(tt.reqMap, tt.want) {
				t.Errorf("body = %+v, want %+v", tt.reqMap, tt.want)
			}
		})
	}
}
