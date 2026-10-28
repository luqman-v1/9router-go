package chat

import (
	"context"
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestApplyTokenSavers_SystemPromptPrefixSuffix(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		prefix       string
		suffix       string
		claudeNative bool
		wantStart    bool // system prompt must start with the prefix
		wantEnd      bool // system prompt must end with the suffix
	}{
		{
			name:      "chat body gets prefix head and suffix tail",
			body:      `{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`,
			prefix:    "HEAD",
			suffix:    "TAIL",
			wantStart: true,
			wantEnd:   true,
		},
		{
			name:         "claude body writes the top-level system field",
			body:         `{"system":"base","messages":[{"role":"user","content":"hi"}]}`,
			prefix:       "HEAD",
			suffix:       "TAIL",
			claudeNative: true,
			wantStart:    true,
			wantEnd:      true,
		},
		{
			name:      "prefix only",
			body:      `{"messages":[{"role":"user","content":"hi"}]}`,
			prefix:    "HEAD",
			wantStart: true,
		},
		{
			name:    "suffix only",
			body:    `{"messages":[{"role":"user","content":"hi"}]}`,
			suffix:  "TAIL",
			wantEnd: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, cleanup := setupHandlerForForward(t)
			defer cleanup()
			h.TokenSaver.SetSystemPrompt(true, tt.prefix, tt.suffix)

			got, _, _, _ := h.applyTokenSavers(context.Background(), []byte(tt.body), tt.claudeNative)
			sys := firstSystemText(t, got, tt.claudeNative)
			if sys == "" {
				t.Fatalf("no system prompt written into %s", got)
			}
			if tt.wantStart && !strings.HasPrefix(sys, tt.prefix) {
				t.Errorf("system prompt %q must start with prefix %q", sys, tt.prefix)
			}
			if tt.wantEnd && !strings.HasSuffix(sys, tt.suffix) {
				t.Errorf("system prompt %q must end with suffix %q", sys, tt.suffix)
			}
		})
	}
}

func TestApplyTokenSavers_SystemPromptDisabled(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetSystemPrompt(false, "HEAD", "TAIL")

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(context.Background(), body, false)
	if string(got) != string(body) {
		t.Errorf("disabled system prompt changed the body:\ngot: %s\nwant: %s", got, body)
	}
}

func TestApplyTokenSavers_SystemPromptEmptySlots(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetSystemPrompt(true, "", "")

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(context.Background(), body, false)
	if string(got) != string(body) {
		t.Errorf("empty prefix and suffix must leave the body alone:\ngot: %s", got)
	}
}

// firstSystemText flattens whichever system-prompt shape was written, so the
// assertions do not depend on key order.
func firstSystemText(t *testing.T, body []byte, claudeNative bool) string {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var parts []string
	if claudeNative {
		switch sys := req["system"].(type) {
		case string:
			parts = append(parts, sys)
		case []any:
			for _, b := range sys {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if s, ok := bm["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
	}
	arr, _ := req["messages"].([]any)
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		if s, ok := mm["content"].(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}
