package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestSSEStream_ClaudePassthroughWithToolMapCapturesUsage covers the other
// passthrough branch: translate=false with a decloaker tool map, i.e. a
// Claude-native combo member whose tool names were fitted. That branch used to
// skip usage capture entirely, so every such turn logged cached_tokens=0.
func TestSSEStream_ClaudePassthroughWithToolMapCapturesUsage(t *testing.T) {
	sseData := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":500}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()
	var ttft int64
	err := sseStream(sseStreamOpts{
		W:           rec,
		Upstream:    strings.NewReader(sseData),
		Translate:   false,
		StartTime:   time.Now(),
		TTFT:        &ttft,
		Ctx:         ctx,
		ToolNameMap: map[string]string{"Read": "Read"},
	})
	if err != nil {
		t.Fatalf("sseStream: %v", err)
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
