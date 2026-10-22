package thinking

import "testing"

// TestApplyOpenAIResponsesWritesNestedReasoning pins the Responses wire
// dialect: a strict Responses upstream rejects a top-level reasoning_effort
// ("unknown parameter `reasoning_effort`") and reads reasoning:{effort,summary}
// instead. The Responses dialect is the target's, not the capability's, so a
// Responses target writes the nested form for every provider on that lane.
func TestApplyOpenAIResponsesWritesNestedReasoning(t *testing.T) {
	// opencode: the capability says "openai", the target says Responses.
	out := apply(t, "openai-responses", "opencode", "muse-spark-1.3-contributor-free",
		map[string]any{"reasoning_effort": "high"}, nil)
	mustAbsent(t, out, "reasoning_effort")
	r, _ := out["reasoning"].(map[string]any)
	if r["effort"] != "high" {
		t.Fatalf("reasoning = %+v, want effort high", out["reasoning"])
	}
	if r["summary"] != "auto" {
		t.Fatalf("reasoning = %+v, want summary auto", out["reasoning"])
	}
	// muse: the same dialect, on the provider that pinned it first.
	out = apply(t, "openai-responses", "muse", "muse-spark-1.3-contributor",
		map[string]any{"reasoning_effort": "high"}, nil)
	mustAbsent(t, out, "reasoning_effort")
	r, _ = out["reasoning"].(map[string]any)
	if r["effort"] != "high" {
		t.Fatalf("reasoning = %+v, want effort high", out["reasoning"])
	}
}

// TestApplyOpenAIResponsesDisableIsOmitted pins the disable on a Responses
// wire: the dialect has no "none", so the field is dropped instead of asking
// the model to think at the lowest level.
func TestApplyOpenAIResponsesDisableIsOmitted(t *testing.T) {
	out := apply(t, "openai-responses", "opencode", "gpt-5.3-codex",
		map[string]any{"reasoning_effort": "none"}, nil)
	mustAbsent(t, out, "reasoning_effort", "reasoning")
}
