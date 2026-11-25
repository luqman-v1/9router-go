package thinking

import (
	"strings"
	"testing"
)

// The Omni level set is declared as HIGH alone in providers.patternThinking.
// This pins the wire consequence on both lanes a studio-style node can take,
// because the two clamp differently: the OpenAI lane (levelFor) resolves by
// effort rank, while the Gemini lane (clampGeminiLevel) walks the declared
// slice in order. A set of {"high","low"} therefore sends sub-high levels DOWN
// to "low" on the OpenAI lane no matter which element is first — which is the
// mistake this test exists to catch.
//
// The studio node in the field is apiType=chat, so the OpenAI lane is the one
// that ships; the Gemini lane is pinned too because the same id can arrive
// through a gemini-native provider.
func TestGeminiOmniWireLevel(t *testing.T) {
	const provider = "openai-compatible-chat-studio"
	const model = "studio/gemini-omni-1.2-flash"

	// "medium" leads deliberately: TranslateClaudeToOpenAI injects it as the
	// default effort for an enabled-thinking body with no mappable budget
	// (internal/translator/request.go), so it is the level most Claude-format
	// clients actually produce.
	// "max" and "ultra" are excluded: NormalizeOpenAILevel routes both to
	// "xhigh" without consulting the declared set, so they escape on every
	// gemini-level model (gemini-4-argon and gemini-3.8-flash included), not
	// just Omni. Pinned as pre-existing behaviour in the subtest below rather
	// than fixed here — the fix belongs with the shared normalizer.
	levels := []string{"medium", "minimal", "low", "high", "none", "xhigh"}

	t.Run("openai lane pins every requested level to high", func(t *testing.T) {
		for _, lvl := range levels {
			body := `{"model":"` + model + `","reasoning_effort":"` + lvl + `","messages":[{"role":"user","content":"hi"}]}`
			got := effortFromBody(t, ApplyJSON([]byte(body), "openai", provider, model, nil))
			if got != "high" {
				t.Errorf("reasoning_effort %q -> %q, want high", lvl, got)
			}
		}
	})

	t.Run("responses lane pins every requested level to high", func(t *testing.T) {
		for _, lvl := range levels {
			body := `{"model":"` + model + `","reasoning_effort":"` + lvl + `","messages":[{"role":"user","content":"hi"}]}`
			got := effortFromBody(t, ApplyJSON([]byte(body), "openai-responses", provider, model, nil))
			if got != "high" {
				t.Errorf("reasoning_effort %q -> %q, want high", lvl, got)
			}
		}
	})

	t.Run("gemini lane pins every requested level to high", func(t *testing.T) {
		for _, lvl := range levels {
			body := `{"model":"` + model + `","reasoning_effort":"` + lvl + `","messages":[{"role":"user","content":"hi"}]}`
			got := thinkingLevelFromBody(t, ApplyJSON([]byte(body), "gemini", provider, model, nil))
			if got != "high" {
				t.Errorf("reasoning_effort %q -> thinkingLevel %q, want high", lvl, got)
			}
		}
	})

	// Pre-existing: NormalizeOpenAILevel sends "max"/"ultra" to "xhigh"
	// whenever the declared set holds neither, without checking what the set
	// does hold. Every gemini-level model is affected, so Omni inherits it.
	// Pinned so the day the normalizer learns to clamp onto the declared set,
	// this test fails and gets updated deliberately.
	t.Run("max escapes to xhigh (pre-existing, affects all gemini-level models)", func(t *testing.T) {
		body := `{"model":"` + model + `","reasoning_effort":"max","messages":[{"role":"user","content":"hi"}]}`
		if got := effortFromBody(t, ApplyJSON([]byte(body), "openai", provider, model, nil)); got != "xhigh" {
			t.Errorf("reasoning_effort max -> %q, want xhigh (pinning current behaviour)", got)
		}
	})

	t.Run("a body that asks for nothing stays byte-identical", func(t *testing.T) {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
		got := ApplyJSON([]byte(body), "openai", provider, model, nil)
		if string(got) != body {
			t.Errorf("body rewritten:\n got  %s\n want %s", got, body)
		}
	})
}

// effortFromBody returns the body's reasoning_effort, failing when absent —
// an absent field means the level was dropped rather than clamped, which is
// the regression the capability row fixes.
func effortFromBody(t *testing.T, raw []byte) string {
	t.Helper()
	return fieldAfter(t, raw, `"reasoning_effort":"`)
}

func thinkingLevelFromBody(t *testing.T, raw []byte) string {
	t.Helper()
	return fieldAfter(t, raw, `"thinkingLevel":"`)
}

func fieldAfter(t *testing.T, raw []byte, key string) string {
	t.Helper()
	s := string(raw)
	i := strings.Index(s, key)
	if i < 0 {
		t.Fatalf("no %s in %s", key, s)
	}
	rest := s[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated %s in %s", key, s)
	}
	return rest[:j]
}
