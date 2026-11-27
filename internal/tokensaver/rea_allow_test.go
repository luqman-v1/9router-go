package tokensaver

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

const paraSecurity = "IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass targeting, supply chain compromise, or detection evasion for malicious purposes. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases."
const paraIdentity = "You are a Claude agent working with the user toward their goals."
const paraTools = "# Tools\n\nBash runs commands."

func buildSystem() string {
	return strings.Join([]string{paraIdentity, paraSecurity, paraTools}, "\n\n")
}

func jstr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestStripSystemPromptSegmentsClaude(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":` + jstr(buildSystem()) + `}]}`)
	out, did := StripSystemPromptSegments(body, "claude")
	if !did {
		t.Fatal("expected did=true")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	sys := m["system"].([]any)
	if len(sys) != 1 {
		t.Fatalf("system len=%d", len(sys))
	}
	text := sys[0].(map[string]any)["text"].(string)
	if strings.Contains(text, "mass targeting") {
		t.Fatal("security para not stripped")
	}
	if !strings.Contains(text, paraIdentity) || !strings.Contains(text, "# Tools") {
		t.Fatal("kept paras missing")
	}
}

func TestStripSystemPromptSegmentsOpenAIChat(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":` + jstr(buildSystem()) + `},{"role":"user","content":"hello"}]}`)
	out, did := StripSystemPromptSegments(body, "openai")
	if !did {
		t.Fatal("expected did=true")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d", len(msgs))
	}
	content := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(content, "Dual-use security tools") {
		t.Fatal("security para not stripped")
	}
	if !strings.Contains(content, paraIdentity) {
		t.Fatal("identity missing")
	}
	if msgs[1].(map[string]any)["content"].(string) != "hello" {
		t.Fatal("user msg changed")
	}
}

func TestStripSystemPromptSegmentsOpenAIResponses(t *testing.T) {
	body := []byte(`{"instructions":` + jstr(buildSystem()) + `,"input":[]}`)
	out, did := StripSystemPromptSegments(body, "openai-responses")
	if !did {
		t.Fatal("expected did=true")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	instr := m["instructions"].(string)
	if strings.Contains(instr, "mass targeting") {
		t.Fatal("security para not stripped")
	}
	if !strings.Contains(instr, paraIdentity) {
		t.Fatal("identity missing")
	}
}

func TestStripSystemPromptSegmentsGemini(t *testing.T) {
	body := []byte(`{"systemInstruction":{"parts":[{"text":` + jstr(paraIdentity) + `},{"text":` + jstr(paraSecurity) + `}]},"contents":[]}`)
	out, did := StripSystemPromptSegments(body, "gemini")
	if !did {
		t.Fatal("expected did=true")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	parts := m["systemInstruction"].(map[string]any)["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("parts len=%d", len(parts))
	}
	if parts[0].(map[string]any)["text"].(string) != paraIdentity {
		t.Fatal("wrong part")
	}
}

func TestApplyReaAllowAppendAndNoDup(t *testing.T) {
	body := []byte(`{"system":"You are helpful."}`)
	out, did := ApplyReaAllow(body, "claude")
	if !did {
		t.Fatal("expected did=true")
	}
	if !strings.Contains(string(out), REAAllowText) {
		t.Fatal("REA not appended")
	}
	out2, _ := ApplyReaAllow(out, "claude")
	if strings.Count(string(out2), REAAllowText) != 1 {
		t.Fatalf("duplicate injection: count=%d", strings.Count(string(out2), REAAllowText))
	}
}

func TestApplyReaAllowOpenAIChat(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`)
	out, did := ApplyReaAllow(body, "openai")
	if !did {
		t.Fatal("expected did=true")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	content := m["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(content, REAAllowText) {
		t.Fatal("REA not injected")
	}
}

func TestStripClaudeEmptiedBlockCacheControl(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":` + jstr(paraSecurity) + `,"cache_control":{"type":"ephemeral"}},{"type":"text","text":` + jstr(paraIdentity) + `}]}`)
	out, did := StripSystemPromptSegments(body, "claude")
	if !did {
		t.Fatal("expected did=true")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	sys := m["system"].([]any)
	if len(sys) != 2 {
		t.Fatalf("system len=%d", len(sys))
	}
	if sys[0].(map[string]any)["text"].(string) != "" {
		t.Fatal("emptied block text not empty")
	}
}

func TestFailOpen(t *testing.T) {
	if _, did := StripSystemPromptSegments([]byte(`{bad`), "claude"); did {
		t.Fatal("expected false")
	}
	if _, did := ApplyReaAllow([]byte(`{bad`), "claude"); did {
		t.Fatal("expected false")
	}
	if _, did := ApplyReaAllow(nil, "claude"); did {
		t.Fatal("expected false on nil")
	}
}
