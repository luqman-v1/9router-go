package translator

import (
	"context"
	"testing"
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

	// Usage should be captured after finish
	global := GetAndClearLastUsage()
	if global == nil {
		t.Fatal("expected non-nil last usage after stream finish")
	}
	if global.PromptTokens != 5 || global.CompletionTokens != 3 {
		t.Errorf("expected 5/3 tokens, got %#v", global)
	}
}
