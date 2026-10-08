package tokensaver

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestGetADHDPrompt(t *testing.T) {
	tests := []struct {
		name     string
		level    string
		expected string
	}{
		{
			name:     "lite level",
			level:    "lite",
			expected: ADHDLite,
		},
		{
			name:     "full level",
			level:    "full",
			expected: ADHDFull,
		},
		{
			name:     "empty level defaults to full",
			level:    "",
			expected: ADHDFull,
		},
		{
			name:     "unknown level defaults to full",
			level:    "unknown",
			expected: ADHDFull,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetADHDPrompt(tt.level)
			if got != tt.expected {
				t.Fatalf("GetADHDPrompt(%q) = %q; want %q", tt.level, got, tt.expected)
			}
		})
	}
}

func TestADHDPromptContent(t *testing.T) {
	if !strings.Contains(ADHDLite, "# I have ADHD (lite)") {
		t.Errorf("ADHDLite missing expected header")
	}
	if !strings.Contains(ADHDLite, "Lead with the action") {
		t.Errorf("ADHDLite missing core action instruction")
	}
	if !strings.Contains(ADHDFull, "# I have ADHD — action-first output") {
		t.Errorf("ADHDFull missing expected header")
	}
	if !strings.Contains(ADHDFull, "10. No preamble, no recap, no closers") {
		t.Errorf("ADHDFull missing rule 10")
	}
	if ADHDPrompt != ADHDFull {
		t.Errorf("expected ADHDPrompt to equal ADHDFull default")
	}
}

func TestInjectADHDPrompt(t *testing.T) {
	prompt := GetADHDPrompt("full")

	// OpenAI format
	body := []byte(`{"messages":[{"role":"user","content":"help me build a CLI"}]}`)
	injected, did := InjectSystemPrompt(body, prompt)
	if !did {
		t.Fatal("expected prompt injection to succeed")
	}

	var req map[string]any
	if err := json.Unmarshal(injected, &req); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	firstMsg, ok := msgs[0].(map[string]any)
	if !ok || firstMsg["role"] != "system" {
		t.Fatalf("expected first message to be system, got %v", firstMsg)
	}
	if !strings.Contains(firstMsg["content"].(string), "action-first output") {
		t.Fatalf("expected system prompt to contain ADHD rules")
	}

	// Claude format
	claudeBody := []byte(`{"messages":[{"role":"user","content":"help me build a CLI"}]}`)
	claudeInjected, didClaude := InjectSystemPromptClaude(claudeBody, prompt)
	if !didClaude {
		t.Fatal("expected claude prompt injection to succeed")
	}

	var claudeReq map[string]any
	if err := json.Unmarshal(claudeInjected, &claudeReq); err != nil {
		t.Fatalf("failed to parse claude JSON: %v", err)
	}
	sysStr, ok := claudeReq["system"].(string)
	if !ok || !strings.Contains(sysStr, "action-first output") {
		t.Fatalf("expected system string in claude request, got %v", claudeReq["system"])
	}
}
