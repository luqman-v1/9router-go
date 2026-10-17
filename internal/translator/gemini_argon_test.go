package translator

import (
	"encoding/json"
	"testing"
)

func TestGemini4Argon_SamplingParamsStripped(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		hasTemp bool
	}{
		{
			name:    "gemini-4-argon strips temp",
			model:   "gemini-4-argon",
			hasTemp: false,
		},
		{
			name:    "gemini-4-argon-high strips temp",
			model:   "gemini-4-argon-high",
			hasTemp: false,
		},
		{
			name:    "gemini-2.5-pro preserves temp",
			model:   "gemini-2.5-pro",
			hasTemp: true,
		},
		{
			name:    "gemini-3.8-flash preserves temp",
			model:   "gemini-3.8-flash",
			hasTemp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			temp := 0.7
			req := OpenAIRequest{
				Model:       tt.model,
				Temperature: &temp,
				Messages: []OpenAIMessage{
					{Role: "user", Content: "hello"},
				},
			}
			rawReq, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("failed to marshal request: %v", err)
			}

			res, err := TranslateOpenAIToGemini(rawReq)
			if err != nil {
				t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
			}

			var geminiReq struct {
				GenerationConfig json.RawMessage `json:"generationConfig"`
			}
			if err := json.Unmarshal(res, &geminiReq); err != nil {
				t.Fatalf("failed to unmarshal gemini req: %v", err)
			}

			if len(geminiReq.GenerationConfig) == 0 {
				if tt.hasTemp {
					t.Fatalf("expected generationConfig but got empty")
				}
				return
			}

			var gc map[string]any
			if err := json.Unmarshal(geminiReq.GenerationConfig, &gc); err != nil {
				t.Fatalf("failed to unmarshal generationConfig: %v", err)
			}

			_, gotTemp := gc["temperature"]

			if gotTemp != tt.hasTemp {
				t.Errorf("temperature presence = %v, want %v", gotTemp, tt.hasTemp)
			}
		})
	}
}

func TestNormalizeGeminiContents_FunctionResponseLast(t *testing.T) {
	contents := []GeminiContent{
		{
			Role: "user",
			Parts: []GeminiPart{
				{Text: "some text before"},
				{FunctionResponse: &GeminiFunctionResp{Name: "test_func"}},
			},
		},
	}

	normalized := NormalizeGeminiContents(contents)
	if len(normalized) == 0 {
		t.Fatalf("expected normalized contents")
	}

	parts := normalized[0].Parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	if parts[0].FunctionResponse != nil {
		t.Errorf("expected functionResponse to be last, but it was first")
	}
	if parts[1].FunctionResponse == nil {
		t.Errorf("expected functionResponse to be last")
	}
}
