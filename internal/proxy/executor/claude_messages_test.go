package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/translator"
)

func TestHandleClaudeMessagesStream_ClaudeClientCapturesUsage(t *testing.T) {
	sseData := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":500}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\n"

	ctx := context.Background()
	req := &Request{
		Ctx:          ctx,
		ClaudeClient: true,
	}
	rec := httptest.NewRecorder()
	if err := handleClaudeMessagesStream(rec, req, strings.NewReader(sseData)); err != nil {
		t.Fatalf("handleClaudeMessagesStream: %v", err)
	}

	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("expected usage in context, got nil")
	}
	if usage.PromptTokens != 620 {
		t.Errorf("PromptTokens = %d, want 620 (100 + 500 + 20)", usage.PromptTokens)
	}
	if usage.CachedTokens != 500 {
		t.Errorf("CachedTokens = %d, want 500", usage.CachedTokens)
	}
	if usage.CacheCreationInputTokens != 20 {
		t.Errorf("CacheCreationInputTokens = %d, want 20", usage.CacheCreationInputTokens)
	}
	if usage.CompletionTokens != 25 {
		t.Errorf("CompletionTokens = %d, want 25", usage.CompletionTokens)
	}
}
