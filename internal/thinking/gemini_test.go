package thinking

import (
	"testing"

	"9router/proxy/internal/providers"
)

func tcFor(t *testing.T, intent *Intent, format string, supported []string) (map[string]any, int) {
	t.Helper()
	caps := providers.Capabilities{} // CanDisableThinking defaults to true
	return GeminiConfig(intent, format, caps, supported)
}

func TestGeminiConfigIncludeThoughtsDefault(t *testing.T) {
	supported := []string{"minimal", "low", "medium", "high"}

	t.Run("thinking active defaults includeThoughts to true", func(t *testing.T) {
		intent := &Intent{Mode: ModeLevel, Level: "high"}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != true {
			t.Fatalf("includeThoughts = %v, want true", tc["includeThoughts"])
		}
		if tc["thinkingLevel"] != "high" {
			t.Fatalf("thinkingLevel = %v, want high", tc["thinkingLevel"])
		}
	})

	t.Run("no thinking defaults includeThoughts to false", func(t *testing.T) {
		intent := &Intent{Mode: ModeNone}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != false {
			t.Fatalf("includeThoughts = %v, want false", tc["includeThoughts"])
		}
		if tc["thinkingLevel"] != "minimal" {
			t.Fatalf("thinkingLevel = %v, want minimal", tc["thinkingLevel"])
		}
	})

	t.Run("auto defaults to high level and includeThoughts true", func(t *testing.T) {
		intent := &Intent{Mode: ModeAuto}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != true {
			t.Fatalf("includeThoughts = %v, want true", tc["includeThoughts"])
		}
		if tc["thinkingLevel"] != "high" {
			t.Fatalf("thinkingLevel = %v, want high", tc["thinkingLevel"])
		}
	})
}

func TestGeminiConfigIncludeThoughtsOptOut(t *testing.T) {
	supported := []string{"minimal", "low", "medium", "high"}

	t.Run("client includeThoughts false is respected", func(t *testing.T) {
		intent := &Intent{Mode: ModeLevel, Level: "high", IncludeThoughts: ptrBool(false)}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != false {
			t.Fatalf("includeThoughts = %v, want false", tc["includeThoughts"])
		}
		if tc["thinkingLevel"] != "high" {
			t.Fatalf("thinkingLevel = %v, want high", tc["thinkingLevel"])
		}
	})

	t.Run("client includeThoughts true is respected", func(t *testing.T) {
		intent := &Intent{Mode: ModeLevel, Level: "high", IncludeThoughts: ptrBool(true)}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != true {
			t.Fatalf("includeThoughts = %v, want true", tc["includeThoughts"])
		}
	})

	t.Run("nil IncludeThoughts uses default", func(t *testing.T) {
		intent := &Intent{Mode: ModeLevel, Level: "high"} // IncludeThoughts nil
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != true {
			t.Fatalf("includeThoughts = %v, want true", tc["includeThoughts"])
		}
	})

	t.Run("includeThoughts true has no effect when level is minimal", func(t *testing.T) {
		intent := &Intent{Mode: ModeNone, IncludeThoughts: ptrBool(true)}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != false {
			t.Fatalf("includeThoughts = %v, want false (no thinking on wire)", tc["includeThoughts"])
		}
		if tc["thinkingLevel"] != "minimal" {
			t.Fatalf("thinkingLevel = %v, want minimal", tc["thinkingLevel"])
		}
	})
}

func TestGeminiConfigIncludeThoughtsClamp(t *testing.T) {
	// gemini-3.8 / gemini-4-argon do not advertise "minimal"; it clamps to "low",
	// so thinking IS active on the wire and includeThoughts defaults to true.
	supported := []string{"low", "medium", "high"}

	t.Run("minimal clamps to low and includeThoughts defaults true", func(t *testing.T) {
		intent := &Intent{Mode: ModeNone}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["thinkingLevel"] != "low" {
			t.Fatalf("thinkingLevel = %v, want low", tc["thinkingLevel"])
		}
		if tc["includeThoughts"] != true {
			t.Fatalf("includeThoughts = %v, want true (thinking active on wire)", tc["includeThoughts"])
		}
	})

	t.Run("client opt-out respected after clamp", func(t *testing.T) {
		intent := &Intent{Mode: ModeNone, IncludeThoughts: ptrBool(false)}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["includeThoughts"] != false {
			t.Fatalf("includeThoughts = %v, want false (client opted out)", tc["includeThoughts"])
		}
		if tc["thinkingLevel"] != "low" {
			t.Fatalf("thinkingLevel = %v, want low", tc["thinkingLevel"])
		}
	})

	t.Run("high effort passes through with includeThoughts true", func(t *testing.T) {
		intent := &Intent{Mode: ModeLevel, Level: "high"}
		tc, _ := tcFor(t, intent, "gemini-level", supported)
		if tc["thinkingLevel"] != "high" {
			t.Fatalf("thinkingLevel = %v, want high", tc["thinkingLevel"])
		}
		if tc["includeThoughts"] != true {
			t.Fatalf("includeThoughts = %v, want true", tc["includeThoughts"])
		}
	})
}

func ptrBool(b bool) *bool {
	return &b
}
