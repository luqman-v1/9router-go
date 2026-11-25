package executor

import (
	json "encoding/json/v2"
	"testing"
)

// The Groq gpt-oss Harmony JSON failure: a structured response_format without
// a parsed reasoning stream makes Groq answer 400 json_validate_failed
// ("Failed to validate JSON"). The fix pins reasoning_format=parsed so the
// chain-of-thought lands in message.reasoning and the content stays clean
// JSON.
func TestNormalizeGroqGPTOSSRequest(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantParsed bool
		wantReason string
	}{
		{
			name:       "gpt-oss-20b json_object gets reasoning_format parsed",
			body:       `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
			wantParsed: true,
		},
		{
			name:       "gpt-oss-120b json_schema gets reasoning_format parsed",
			body:       `{"model":"openai/gpt-oss-120b","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"test","schema":{"type":"object"}}}}`,
			wantParsed: true,
		},
		{
			name:       "client reasoning_format respected",
			body:       `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"},"reasoning_format":"hidden"}`,
			wantParsed: false,
			wantReason: "hidden",
		},
		{
			name:       "non-gpt-oss groq untouched",
			body:       `{"model":"llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
			wantParsed: false,
		},
		{
			name:       "text response_format untouched",
			body:       `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"text"}}`,
			wantParsed: false,
		},
		{
			name:       "no response_format untouched",
			body:       `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}]}`,
			wantParsed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := normalizeGroqGPTOSSRequest([]byte(tt.body))
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			rf, has := m["reasoning_format"]
			if tt.wantParsed {
				if !has || rf != "parsed" {
					t.Errorf("reasoning_format = %v, want parsed: %s", rf, out)
				}
			} else if tt.wantReason != "" {
				if rf != tt.wantReason {
					t.Errorf("reasoning_format = %v, want %q: %s", rf, tt.wantReason, out)
				}
			} else if has {
				t.Errorf("reasoning_format should be absent, got %v: %s", rf, out)
			}
			// response_format must survive untouched
			if _, ok := m["response_format"]; tt.body == `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}]}` {
				if ok {
					t.Errorf("response_format invented: %s", out)
				}
			}
		})
	}
}

func TestIsGroqGPTOSSModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"openai/gpt-oss-20b", true},
		{"openai/gpt-oss-120b", true},
		{"gpt-oss-20b", true},
		{"llama-3.3-70b-versatile", false},
		{"qwen/qwen3-32b", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isGroqGPTOSSModel(tt.model); got != tt.want {
				t.Errorf("isGroqGPTOSSModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestClampGroqReasoningEffort(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"valid low kept", "low", ""},
		{"valid medium kept", "medium", ""},
		{"valid high kept", "high", ""},
		{"minimal folds to low", "minimal", "low"},
		{"none folds to low", "none", "low"},
		{"xhigh folds to high", "xhigh", "high"},
		{"max folds to high", "max", "high"},
		{"unknown folds to medium", "bogus", "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := clampGroqReasoningEffort(tt.input)
			if tt.want == "" {
				if changed {
					t.Errorf("clampGroqReasoningEffort(%q) changed, want untouched", tt.input)
				}
				return
			}
			if !changed || got != tt.want {
				t.Errorf("clampGroqReasoningEffort(%q) = (%q, %v), want (%q, true)", tt.input, got, changed, tt.want)
			}
		})
	}
	t.Run("absent effort untouched", func(t *testing.T) {
		if got, changed := clampGroqReasoningEffort(nil); changed || got != "" {
			t.Errorf("clampGroqReasoningEffort(nil) = (%q, %v), want (\"\", false)", got, changed)
		}
	})
}

func TestNormalizeGroqGPTOSSRequest_EffortClamp(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantEff  string
		wantRsnF bool
	}{
		{"minimal without response_format clamps (non-JSON 400)", `{"model":"openai/gpt-oss-120b","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"minimal"}`, "low", false},
		{"xhigh without response_format clamps", `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`, "high", false},
		{"valid effort preserved", `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`, "medium", false},
		{"no effort field stays absent", `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi"}]}`, "", false},
		{"clamp plus parsed compose", `{"model":"openai/gpt-oss-120b","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"minimal","response_format":{"type":"json_object"}}`, "low", true},
		{"non-gpt-oss untouched", `{"model":"llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"minimal"}`, "minimal", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := normalizeGroqGPTOSSRequest([]byte(tt.body))
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := ""
			if v, ok := m["reasoning_effort"]; ok {
				got, _ = v.(string)
			}
			if got != tt.wantEff {
				t.Errorf("reasoning_effort = %q, want %q: %s", got, tt.wantEff, out)
			}
			_, hasRF := m["reasoning_format"]
			if hasRF != tt.wantRsnF {
				t.Errorf("reasoning_format present = %v, want %v: %s", hasRF, tt.wantRsnF, out)
			}
		})
	}
}
