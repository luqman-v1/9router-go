package thinking

import (
	"9router/proxy/internal/providers"
)

// applyFormat ports applyFormat: one dialect out. none on a model that
// cannot disable clamps to level minimal before the writer runs.
func applyFormat(fmt string, body map[string]any, cfg *Intent, caps providers.Capabilities, supported []string, display string) {
	none := cfg.Mode == ModeNone
	cd := canDisable(caps)
	eff := cfg
	if none && !cd {
		eff = &Intent{Mode: ModeLevel, Level: "minimal"}
	}

	switch fmt {
	case "openai":
		if none && cd {
			body["reasoning_effort"] = "none"
			break
		}
		if lvl := ToLevel(eff); lvl != "" {
			body["reasoning_effort"] = NormalizeOpenAILevel(lvl, supported)
		}
	case "openai-responses":
		// Responses nests effort: reasoning:{effort,summary}. A top-level
		// reasoning_effort is rejected by strict upstreams. none omits it.
		if none && cd {
			delete(body, "reasoning")
			break
		}
		if lvl := ToLevel(eff); lvl != "" {
			cur, _ := body["reasoning"].(map[string]any)
			if cur == nil {
				cur = map[string]any{}
			}
			cur["effort"] = NormalizeOpenAILevel(lvl, supported)
			if _, ok := cur["summary"]; !ok {
				cur["summary"] = "auto"
			}
			body["reasoning"] = cur
		}
		delete(body, "reasoning_effort")
	case "claude-adaptive":
		if none && cd {
			body["thinking"] = map[string]any{"type": "disabled"}
			break
		}
		// Permanently adaptive models (e.g. Fable 5.1) take effort directly.
		if cd {
			body["thinking"] = withDisplay(map[string]any{"type": "adaptive"}, display)
		} else {
			delete(body, "thinking")
		}
		lvl := ToLevel(eff)
		effort := lvl
		switch {
		case lvl == "auto":
			effort = "high"
		case lvl == "minimal":
			effort = "low"
		case lvl == "xhigh" && !containsLevel(supported, "xhigh"):
			effort = "high"
		}
		body["output_config"] = map[string]any{"effort": effort}
	case "gemini-level":
		{
			tc, floor := GeminiConfig(eff, "gemini-level", caps)
			SetGeminiThinking(body, tc)
			EnsureGeminiOutputFloor(body, floor, caps)
		}
	case "gemini-budget":
		{
			tc, floor := GeminiConfig(eff, "gemini-budget", caps)
			SetGeminiThinking(body, tc)
			EnsureGeminiOutputFloor(body, floor, caps)
		}
	case "claude-budget":
		if none && cd {
			body["thinking"] = map[string]any{"type": "disabled"}
			break
		}
		b, ok := ToBudget(eff, caps.ThinkingRange)
		if b == -1 {
			body["thinking"] = withDisplay(map[string]any{"type": "enabled"}, display)
		} else {
			if !ok || b == 0 {
				b = 8192
			}
			body["thinking"] = withDisplay(map[string]any{"type": "enabled", "budget_tokens": b}, display)
		}
	case "zai":
		// Z.ai ignores thinking.disabled → enable_thinking:false switches off.
		if none && cd {
			body["enable_thinking"] = false
			delete(body, "thinking")
			break
		}
		body["thinking"] = map[string]any{"type": "enabled"}
		// reasoning_effort read only from GLM-5.2 onward (older ignore it).
		if caps.ThinkingEffortSupported {
			switch lvl := ToLevel(eff); lvl {
			case "low", "minimal":
				body["reasoning_effort"] = "low"
			case "high", "medium":
				body["reasoning_effort"] = "high"
			default:
				body["reasoning_effort"] = "max"
			}
		}
	case "qwen":
		if none && cd {
			body["enable_thinking"] = false
			break
		}
		body["enable_thinking"] = true
		if b, ok := ToBudget(eff, caps.ThinkingRange); ok && b > 0 {
			body["thinking_budget"] = b
		}
	case "deepseek":
		if none && cd {
			body["thinking"] = map[string]any{"type": "disabled"}
			break
		}
		body["thinking"] = map[string]any{"type": "enabled"}
		lvl := ToLevel(eff)
		want := "high"
		if lvl == "xhigh" || lvl == "max" {
			want = "max"
		}
		// Some backends 400 on "max" — clamp when levels exclude it.
		if want == "max" && len(supported) > 0 && !containsLevel(supported, "max") {
			want = "high"
		}
		body["reasoning_effort"] = want
	case "kimi":
		if none && cd {
			body["thinking"] = map[string]any{"type": "disabled"}
			break
		}
		if e := ToKimiReasoningEffort(eff); e != "" {
			body["reasoning_effort"] = e
		}
	case "minimax":
		// M3 adaptive; M2.x cannot disable (handled via clamp above).
		typ := "adaptive"
		if none && cd {
			typ = "disabled"
		}
		body["thinking"] = map[string]any{"type": typ}
	case "hunyuan":
		if none && cd {
			body["thinking"] = map[string]any{"type": "disabled"}
			break
		}
		b, ok := ToBudget(eff, caps.ThinkingRange)
		if b == -1 {
			body["thinking"] = map[string]any{"type": "enabled"}
		} else {
			if !ok || b == 0 {
				b = 8192
			}
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": b}
		}
	case "step":
		// none writes nothing: step models mandate reasoning, so omitting
		// the field lets the upstream default apply. This is the reported fix.
		if none && cd {
			break
		}
		if lvl := ToLevel(eff); lvl != "" {
			if lvl == "xhigh" || lvl == "max" {
				lvl = "high"
			}
			body["reasoning_effort"] = lvl
		}
	case "tokenrouter":
		// Enum is low/medium/high/xhigh/max — "none"/"auto" 400, so omit.
		if none || eff.Mode == ModeAuto {
			break
		}
		if lvl := ToLevel(eff); lvl != "" {
			body["reasoning_effort"] = lvl
		}
	case "kiro":
		// Handled via system-tag injection in the kiro translator; no field.
	case "commandcode":
		// Native CLI sends reasoning_effort inside params of the
		// /alpha/generate envelope. Go's flat OpenAI body has no params
		// object — it is wrapped later by buildCommandcodeBody, which copies
		// top-level keys into params — so write top-level there.
		if params, ok := body["params"].(map[string]any); ok {
			if none && cd {
				delete(params, "reasoning_effort")
				break
			}
			if lvl := ToLevel(eff); lvl != "" {
				params["reasoning_effort"] = lvl
			}
			break
		}
		if none && cd {
			break
		}
		if lvl := ToLevel(eff); lvl != "" {
			body["reasoning_effort"] = lvl
		}
	default:
	}
}

func withDisplay(thinking map[string]any, display string) map[string]any {
	if display != "" {
		thinking["display"] = display
	}
	return thinking
}

func containsLevel(levels []string, want string) bool {
	for _, l := range levels {
		if l == want {
			return true
		}
	}
	return false
}
