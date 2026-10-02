package translator

import (
	json "encoding/json/v2"
	"testing"
)

// DeepSeek answers 400 "Tool names must be unique" when the same tool is
// declared twice, which kills the whole request (upstream commit 7f5bd155,
// open-sse/utils/toolDeduper.js). GLM/MiniMax/Kimi accept duplicates, so the
// collapse is scoped to DeepSeek model ids.

func claudeTool(name, desc string) map[string]any {
	return map[string]any{
		"name":         name,
		"description":  desc,
		"input_schema": map[string]any{"type": "object"},
	}
}

func openAITool(name, desc string) map[string]any {
	return map[string]any{
		"type":     "function",
		"function": map[string]any{"name": name, "description": desc},
	}
}

func TestIsDeepSeekModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{"bare deepseek id", "deepseek-v4-pro", true},
		{"thinking override suffix", "deepseek-v4-flash(max)", true},
		{"vendor-prefixed id", "deepseek/deepseek-chat", true},
		{"vendor-prefixed with override", "some-gateway/deepseek-chat(low)", true},
		{"uppercase", "DeepSeek-V4", true},
		{"leading space", "  deepseek-v4-pro  ", true},
		{"deepseek without the dash prefix", "deepseek", false},
		{"another provider serving a deepseek model", "openrouter/deepseek-chat", true},
		{"non-deepseek model", "glm-5.2", false},
		{"empty", "", false},
		{"model that merely contains deepseek", "my-deepseek-lookalike", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDeepSeekModel(tt.model); got != tt.want {
				t.Errorf("IsDeepSeekModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// TestDedupeToolsDeepSeek proves both the collapse and its scope: the first
// definition survives with its own description, and nothing changes for any
// model DeepSeek does not serve.
func TestDedupeToolsDeepSeek(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		tools         []any
		wantNames     []string
		wantUntouched bool
	}{
		{
			name:      "duplicate Claude-shaped names keep the first",
			model:     "deepseek-v4-flash",
			tools:     []any{claudeTool("Bash", "first"), claudeTool("Bash", "dup"), claudeTool("Read", "other")},
			wantNames: []string{"Bash", "Read"},
		},
		{
			name:      "thinking override suffix still dedupes",
			model:     "deepseek-v4-flash(max)",
			tools:     []any{claudeTool("Bash", "first"), claudeTool("Bash", "dup")},
			wantNames: []string{"Bash"},
		},
		{
			name:      "three declarations collapse to one",
			model:     "deepseek-v4-pro",
			tools:     []any{claudeTool("Bash", "a"), claudeTool("Bash", "b"), claudeTool("Bash", "c")},
			wantNames: []string{"Bash"},
		},
		{
			name:      "OpenAI function shape dedupes by function name",
			model:     "deepseek-v4-flash",
			tools:     []any{openAITool("Bash", "first"), openAITool("Bash", "dup")},
			wantNames: []string{"Bash"},
		},
		{
			name:      "vendor-prefixed deepseek id dedupes",
			model:     "some-gateway/deepseek-chat",
			tools:     []any{claudeTool("Bash", "first"), claudeTool("Bash", "dup")},
			wantNames: []string{"Bash"},
		},
		{
			name:          "non-DeepSeek model is left alone",
			model:         "glm-5.2",
			tools:         []any{claudeTool("Bash", "first"), claudeTool("Bash", "dup")},
			wantNames:     []string{"Bash", "Bash"},
			wantUntouched: true,
		},
		{
			name:          "no model declared leaves the tools alone",
			model:         "",
			tools:         []any{claudeTool("Bash", "first"), claudeTool("Bash", "dup")},
			wantNames:     []string{"Bash", "Bash"},
			wantUntouched: true,
		},
		{
			name:          "distinct names are not touched",
			model:         "deepseek-v4-flash",
			tools:         []any{claudeTool("Bash", "a"), claudeTool("Read", "b")},
			wantNames:     []string{"Bash", "Read"},
			wantUntouched: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"model":    tt.model,
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
				"tools":    tt.tools,
			})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}

			got := DedupeToolsDeepSeek(body, tt.model)
			if tt.wantUntouched {
				if string(got) != string(body) {
					t.Errorf("body was rewritten for model %q:\n got %s\nwant %s", tt.model, got, body)
				}
				return
			}

			var decoded struct {
				Tools []struct {
					Name     string `json:"name"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
					Description string `json:"description"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatalf("unmarshal deduped body: %v", err)
			}
			if len(decoded.Tools) != len(tt.wantNames) {
				t.Fatalf("kept %d tools, want %d (%s)", len(decoded.Tools), len(tt.wantNames), got)
			}
			for i, want := range tt.wantNames {
				got := decoded.Tools[i].Name
				if got == "" {
					got = decoded.Tools[i].Function.Name
				}
				if got != want {
					t.Errorf("tool %d = %q, want %q", i, got, want)
				}
			}
			if len(decoded.Tools) > 0 && decoded.Tools[0].Description == "dup" {
				t.Errorf("the duplicate definition won; the first declaration must survive: %s", got)
			}
		})
	}
}

// TestDedupeToolsDeepSeek_KeepsFirstDefinition pins the winner explicitly: the
// surviving tool carries the first declaration's description.
func TestDedupeToolsDeepSeek_KeepsFirstDefinition(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"tools": []any{claudeTool("Bash", "first wins"), claudeTool("Bash", "second loses")},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var decoded struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(DedupeToolsDeepSeek(body, "deepseek-v4-flash"), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Tools) != 1 || decoded.Tools[0].Description != "first wins" {
		t.Errorf("surviving tools = %+v, want the single first definition", decoded.Tools)
	}
}

// TestDedupeToolsDeepSeek_MalformedBodies keeps the dispatcher from panicking on
// what a client can actually put on the wire.
func TestDedupeToolsDeepSeek_MalformedBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not JSON", `{"tools":[`},
		{"tools is not an array", `{"tools":{"name":"Bash"}}`},
		{"tools holds non-objects", `{"tools":["Bash","Bash"]}`},
		{"no tools key", `{"model":"deepseek-v4-flash"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if got := DedupeToolsDeepSeek(body, "deepseek-v4-flash"); string(got) != tt.body {
				t.Errorf("DedupeToolsDeepSeek(%s) = %s, want it returned unchanged", tt.body, got)
			}
		})
	}
}
