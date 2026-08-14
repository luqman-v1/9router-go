package translator

import (
	"testing"
)

func TestTranslateGeminiChunkToOpenAI_CachedTokens(t *testing.T) {
	chunkJSON := `{
		"candidates": [
			{
				"content": {
					"parts": [{"text": "Hello world"}]
				},
				"finishReason": "STOP"
			}
		],
		"usageMetadata": {
			"promptTokenCount": 1000,
			"candidatesTokenCount": 50,
			"cachedContentTokenCount": 800
		}
	}`

	state := &GeminiStreamState{
		MessageId: "test-msg-1",
		Model:     "gemini-3.7-flash-high",
	}

	chunks, err := TranslateGeminiChunkToOpenAI([]byte(chunkJSON), state)
	if err != nil {
		t.Fatalf("TranslateGeminiChunkToOpenAI failed: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected chunks, got 0")
	}

	if state.Usage == nil {
		t.Fatal("expected state.Usage to be non-nil")
	}

	if state.Usage.PromptTokens != 1000 {
		t.Errorf("expected 1000 prompt tokens, got %d", state.Usage.PromptTokens)
	}
	if state.Usage.CompletionTokens != 50 {
		t.Errorf("expected 50 completion tokens, got %d", state.Usage.CompletionTokens)
	}
	if state.Usage.CachedTokens != 800 {
		t.Errorf("expected 800 cached tokens, got %d", state.Usage.CachedTokens)
	}
}
