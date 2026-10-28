package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestApplyFingerprintSystemPrompt(t *testing.T) {
	t.Run("upgrades Anthropic system string", func(t *testing.T) {
		body := []byte(`{"system":"You are an assistant.\n<env>\n  Working directory: /tmp/ocprobe\n</env>"}`)
		out := ApplyFingerprintSystemPrompt(body)
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		sys := m["system"].(string)
		if !strings.Contains(sys, "Workspace root folder: /tmp/ocprobe") {
			t.Errorf("expected upgraded env block with workspace root folder, got %s", sys)
		}
		if !strings.Contains(sys, "You are an assistant.") {
			t.Errorf("expected original content preserved, got %s", sys)
		}
		// Idempotent
		out2 := ApplyFingerprintSystemPrompt(out)
		if string(out2) != string(out) {
			t.Errorf("expected idempotent upgrade, got %s != %s", string(out2), string(out))
		}
	})

	t.Run("appends env marker to OpenAI messages without system", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
		out := ApplyFingerprintSystemPrompt(body)
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		msgs := m["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(msgs))
		}
		first := msgs[0].(map[string]any)
		if first["role"] != "system" || first["content"] != OpenCodeEnvMarker {
			t.Errorf("expected prepended system message, got %#v", first)
		}
	})

	t.Run("upgrades existing OpenAI system message", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"hi"}]}`)
		out := ApplyFingerprintSystemPrompt(body)
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		msgs := m["messages"].([]any)
		first := msgs[0].(map[string]any)
		content := first["content"].(string)
		if !strings.Contains(content, "Be concise.") || !strings.Contains(content, OpenCodeEnvMarker) {
			t.Errorf("expected content to include both instruction and marker, got %s", content)
		}
	})

	t.Run("upgrades Responses API instructions", func(t *testing.T) {
		body := []byte(`{"instructions":"Instructions."}`)
		out := ApplyFingerprintSystemPrompt(body)
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		inst := m["instructions"].(string)
		if !strings.Contains(inst, "Instructions.") || !strings.Contains(inst, OpenCodeEnvMarker) {
			t.Errorf("expected instructions to include both, got %s", inst)
		}
	})
}
