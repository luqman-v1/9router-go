package translator

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"testing"
	"time"
)

// --- Context-based Usage ---

func TestContextUsage(t *testing.T) {
	ctx := context.Background()
	ctx = WithUsageCapture(ctx)

	if u := GetAndClearUsage(ctx); u != nil {
		t.Errorf("expected nil initially, got %#v", u)
	}

	SetUsage(ctx, &OpenAIUsage{PromptTokens: 10, CompletionTokens: 20})

	u := GetAndClearUsage(ctx)
	if u == nil {
		t.Fatal("expected non-nil usage")
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 20 {
		t.Errorf("got %#v", u)
	}

	if u2 := GetAndClearUsage(ctx); u2 != nil {
		t.Errorf("expected nil after clear, got %#v", u2)
	}
}

// --- SetLastUsage / GetAndClearLastUsage ---

func TestGetAndClearLastUsage(t *testing.T) {
	// Clear residue from other tests that may have set lastUsage
	GetAndClearLastUsage()

	// No usage set
	if u := GetAndClearLastUsage(); u != nil {
		t.Errorf("expected nil initially, got %#v", u)
	}

	// Set and retrieve
	SetLastUsage(&OpenAIUsage{PromptTokens: 10, CompletionTokens: 20, CachedTokens: 5})
	u := GetAndClearLastUsage()
	if u == nil {
		t.Fatal("expected non-nil usage")
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 20 || u.CachedTokens != 5 {
		t.Errorf("got %#v", u)
	}

	// Cleared after get
	if u2 := GetAndClearLastUsage(); u2 != nil {
		t.Errorf("expected nil after clear, got %#v", u2)
	}
}

// --- GetStreamUsage ---

func TestGetStreamUsage(t *testing.T) {
	// Clear any residue from other tests
	GetAndClearLastUsage()

	// Unknown session
	u := GetStreamUsage("nonexistent")
	if u != nil {
		t.Errorf("expected nil for unknown session, got %#v", u)
	}

	// Prime state via TranslateOpenAIToClaudeStream with usage
	chunk := []byte(`{"id":"usage-test","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	_, err := TranslateOpenAIToClaudeStream(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Usage should be captured in the stream state after finish
	sessionUsage := GetStreamUsage("usage-test")
	if sessionUsage == nil {
		t.Fatal("expected non-nil stream usage after finish")
	}
	if sessionUsage.PromptTokens != 5 || sessionUsage.CompletionTokens != 3 {
		t.Errorf("expected 5/3 tokens, got %#v", sessionUsage)
	}
}

// --- Cached tokens from provider usage ---

func TestParseClaudeUsage(t *testing.T) {
	u := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":6,"cache_creation_input_tokens":2}}`))
	if u == nil {
		t.Fatal("expected parsed usage")
	}
	if u.PromptTokens != 18 || u.CompletionTokens != 4 || u.CachedTokens != 6 || u.CacheCreationInputTokens != 2 {
		t.Errorf("got %#v", u)
	}
	// Non-Claude body (no usage) → nil, never a nil deref.
	if ParseClaudeUsage([]byte(`{}`)) != nil {
		t.Error("expected nil for body without usage")
	}
	// OpenAI-format usage must NOT be misread as Claude (would stamp all-zero usage).
	if ParseClaudeUsage([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":50}}`)) != nil {
		t.Error("expected nil for OpenAI-format usage")
	}
}

func TestParseResponseUsage(t *testing.T) {
	// Claude format
	u := ParseResponseUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":6}}`))
	if u == nil || u.PromptTokens != 16 || u.CompletionTokens != 4 || u.CachedTokens != 6 {
		t.Errorf("claude format: got %#v", u)
	}
	// OpenAI format (the !translate path also serves /v1/chat/completions bodies)
	u = ParseResponseUsage([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":30}}}`))
	if u == nil || u.PromptTokens != 100 || u.CompletionTokens != 40 || u.GetCachedTokens() != 30 {
		t.Errorf("openai format: got %#v", u)
	}
	// No usage → nil
	if ParseResponseUsage([]byte(`{}`)) != nil {
		t.Error("expected nil for body without usage")
	}
}

func TestCachedTokensCompatibilityAndClaudeNormalization(t *testing.T) {
	for name, raw := range map[string]string{
		"top level":        `{"cached_tokens":11}`,
		"claude legacy":    `{"cache_read_input_tokens":12}`,
		"prompt details":   `{"prompt_tokens_details":{"cached_tokens":13}}`,
		"input details":    `{"input_tokens_details":{"cached_tokens":14}}`,
		"null then nested": `{"cached_tokens":null,"input_tokens_details":{"cached_tokens":15}}`,
	} {
		if got := CachedTokensFromJSON([]byte(raw)); got == 0 {
			t.Errorf("%s: expected cached tokens, got 0", name)
		}
	}

	usage := &OpenAIUsage{
		PromptTokens:             10,
		CachedTokens:             4,
		CacheCreationInputTokens: 2,
	}
	NormalizeClaudeUsage(usage)
	NormalizeClaudeUsage(usage)
	if usage.PromptTokens != 16 || usage.GetCachedTokens() != 4 {
		t.Fatalf("normalization must be idempotent: %+v", usage)
	}
}

func TestTranslateGeminiResponseToOpenAI_cachedTokens(t *testing.T) {
	geminiBody, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content": map[string]any{"parts": []map[string]any{{"text": "hi"}}},
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     100,
			"candidatesTokenCount": 5,
			"cachedContentToken":   90,
		},
	})
	out, usage, err := TranslateGeminiResponseToOpenAI(geminiBody)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}
	if usage.CachedTokens != 90 {
		t.Errorf("expected CachedTokens=90, got %d", usage.CachedTokens)
	}
	// The OpenAI usage map must carry cached_tokens so the OpenAI→Claude
	// double-translation preserves it.
	var parsed struct {
		Usage map[string]any `json:"usage"`
	}
	if json.Unmarshal(out, &parsed) != nil {
		t.Fatal("expected parseable output")
	}
	if ct, ok := parsed.Usage["cached_tokens"].(float64); !ok || int(ct) != 90 {
		t.Errorf("expected cached_tokens=90 in output usage, got %v", parsed.Usage["cached_tokens"])
	}
}

func TestTranslateGeminiChunkToOpenAI_cachedTokens(t *testing.T) {
	t.Run("delta chunk with cachedContentToken", func(t *testing.T) {
		chunkJSON := []byte(`{
			"candidates": [{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP","index":0}],
			"usageMetadata": {
				"promptTokenCount": 50,
				"candidatesTokenCount": 2,
				"cachedContentToken": 40
			}
		}`)
		state := &GeminiStreamState{MessageId: "msg-1", Model: "gemini-2.5-flash"}
		chunks, err := TranslateGeminiChunkToOpenAI(chunkJSON, state)
		if err != nil {
			t.Fatalf("translate chunk error: %v", err)
		}
		if len(chunks) == 0 {
			t.Fatal("expected chunks")
		}
		if state.Usage == nil || state.Usage.CachedTokens != 40 {
			t.Errorf("expected state.Usage.CachedTokens=40, got %+v", state.Usage)
		}
	})

	t.Run("trailing usage-only chunk with cachedContentToken", func(t *testing.T) {
		chunkJSON := []byte(`{
			"candidates": [],
			"usageMetadata": {
				"promptTokenCount": 120,
				"candidatesTokenCount": 15,
				"cachedContentToken": 100
			}
		}`)
		state := &GeminiStreamState{MessageId: "msg-2", Model: "gemini-2.5-flash"}
		chunks, err := TranslateGeminiChunkToOpenAI(chunkJSON, state)
		if err != nil {
			t.Fatalf("translate chunk error: %v", err)
		}
		if len(chunks) == 0 {
			t.Fatal("expected usage chunk")
		}
		if state.Usage == nil || state.Usage.CachedTokens != 100 {
			t.Errorf("expected state.Usage.CachedTokens=100, got %+v", state.Usage)
		}
	})
}

func TestPruneStaleStates_PrunesPendingJSON(t *testing.T) {
	statesMu.Lock()
	// Seed stale state and orphan pendingJSON
	staleKey := "stale-session-123"
	states[staleKey] = &StreamState{CreatedAt: time.Now().Add(-15 * time.Minute)}
	pendingJSON[staleKey] = pendingFragment{data: []byte(`{"fragment":"stale"}`), createdAt: time.Now().Add(-15 * time.Minute)}

	orphanKey := "orphan-session-456"
	pendingJSON[orphanKey] = pendingFragment{data: []byte(`{"fragment":"orphan"}`), createdAt: time.Now().Add(-15 * time.Minute)}

	// Populate enough entries to trigger pruning threshold
	for i := range 55 {
		k := fmt.Sprintf("filler-%d", i)
		states[k] = &StreamState{CreatedAt: time.Now()}
	}

	pruneStaleStatesLocked()

	_, stateStillExists := states[staleKey]
	_, pendingStillExists := pendingJSON[staleKey]
	_, orphanStillExists := pendingJSON[orphanKey]

	// Cleanup filler
	for i := range 55 {
		delete(states, fmt.Sprintf("filler-%d", i))
	}
	statesMu.Unlock()

	if stateStillExists {
		t.Errorf("expected stale state to be pruned")
	}
	if pendingStillExists {
		t.Errorf("expected stale pendingJSON to be pruned with stale state")
	}
	if orphanStillExists {
		t.Errorf("expected orphan pendingJSON to be pruned")
	}
}

// TestParseClaudeUsage_CacheCreationSplit pins the per-window split Anthropic
// reports beside the cache-write total: a body carrying cache_creation must
// expose both windows, and one without it must leave both at zero rather than
// inventing a 5m write.
func TestParseClaudeUsage_CacheCreationSplit(t *testing.T) {
	u := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":0,"cache_creation_input_tokens":900,"cache_creation":{"ephemeral_5m_input_tokens":400,"ephemeral_1h_input_tokens":500}}}`))
	if u == nil {
		t.Fatal("expected parsed usage")
	}
	if u.CacheCreationInputTokens != 900 || u.CacheCreation5mInputTokens != 400 || u.CacheCreation1hInputTokens != 500 {
		t.Errorf("split: got %#v", u)
	}

	plain := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":4,"cache_creation_input_tokens":900}}`))
	if plain == nil || plain.CacheCreation5mInputTokens != 0 || plain.CacheCreation1hInputTokens != 0 {
		t.Errorf("without cache_creation both windows must stay 0: got %#v", plain)
	}
}

// TestAccumulateClaudeStreamUsage_CacheCreationSplit pins the streaming path:
// message_start carries the split, and a later message_delta must not drop it.
func TestAccumulateClaudeStreamUsage_CacheCreationSplit(t *testing.T) {
	start := `{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":900,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":900}}}}`
	u := AccumulateClaudeStreamUsage(nil, []byte(start))
	if u == nil || u.CacheCreation1hInputTokens != 900 || u.CacheCreation5mInputTokens != 0 {
		t.Fatalf("message_start split: got %#v", u)
	}
	u = AccumulateClaudeStreamUsage(u, []byte(`{"type":"message_delta","usage":{"output_tokens":12}}`))
	if u == nil || u.CacheCreation1hInputTokens != 900 || u.CompletionTokens != 12 {
		t.Errorf("message_delta dropped the split: got %#v", u)
	}
}
