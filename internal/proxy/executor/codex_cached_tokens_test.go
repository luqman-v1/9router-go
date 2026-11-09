package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/translator"
)

// TestCaptureUsageFromSSEChunk_ResponsesSSE verifies that captureUsageFromSSEChunk
// parses usage from Responses API terminal events (response.completed, response.done)
// so that cached_tokens and token counts are preserved when Codex CLI streams via /v1/responses.
func TestCaptureUsageFromSSEChunk_ResponsesSSE(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())

	chunk := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":5200,\"output_tokens\":180,\"total_tokens\":5380,\"input_tokens_details\":{\"cached_tokens\":4096},\"output_tokens_details\":{\"reasoning_tokens\":64}}}}\n\n")

	captureUsageFromSSEChunk(ctx, chunk)

	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("expected usage to be recorded in context, got nil")
	}
	if usage.PromptTokens != 5200 {
		t.Errorf("PromptTokens = %d, want 5200", usage.PromptTokens)
	}
	if usage.CompletionTokens != 180 {
		t.Errorf("CompletionTokens = %d, want 180", usage.CompletionTokens)
	}
	if usage.GetCachedTokens() != 4096 {
		t.Errorf("CachedTokens = %d, want 4096", usage.GetCachedTokens())
	}
	if usage.ReasoningTokens() != 64 {
		t.Errorf("ReasoningTokens = %d, want 64", usage.ReasoningTokens())
	}
}

// TestCodexStream_PreservesCachedTokens verifies that ProcessCodexEvent records usage
// on state, and writeSSEFinish transfers it to the request context.
func TestCodexStream_PreservesCachedTokens(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())
	state := &CodexStreamState{}

	eventData := `{"type":"response.completed","response":{"id":"resp_cx","usage":{"input_tokens":10240,"output_tokens":300,"total_tokens":10540,"input_tokens_details":{"cached_tokens":8192}}}}`
	out := ProcessCodexEvent(eventData, state, "chatcmpl-test", time.Now().Unix())
	if len(out) == 0 {
		t.Fatal("expected chunks from ProcessCodexEvent, got none")
	}

	rec := httptest.NewRecorder()
	req := &Request{Ctx: ctx}
	if err := writeSSEFinish(rec, nil, req, state, "chatcmpl-test", time.Now().Unix()); err != nil {
		t.Fatalf("writeSSEFinish error: %v", err)
	}

	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("expected usage from writeSSEFinish, got nil")
	}
	if usage.PromptTokens != 10240 {
		t.Errorf("PromptTokens = %d, want 10240", usage.PromptTokens)
	}
	if usage.CompletionTokens != 300 {
		t.Errorf("CompletionTokens = %d, want 300", usage.CompletionTokens)
	}
	if usage.GetCachedTokens() != 8192 {
		t.Errorf("CachedTokens = %d, want 8192", usage.GetCachedTokens())
	}
}

// TestStreamChatToResponses_RecordsUsageInContext verifies that StreamChatToResponses
// sets usage with cached tokens into the request context when translating Chat SSE to Responses SSE.
func TestStreamChatToResponses_RecordsUsageInContext(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()

	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hello"}}]}`,
		`data: {"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":1500,"completion_tokens":50,"total_tokens":1550,"prompt_tokens_details":{"cached_tokens":1200}}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	var ttft int64
	if err := StreamChatToResponses(ctx, rec, strings.NewReader(chatStream), time.Now(), &ttft, nil); err != nil {
		t.Fatalf("StreamChatToResponses failed: %v", err)
	}

	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("expected usage recorded by StreamChatToResponses, got nil")
	}
	if usage.PromptTokens != 1500 {
		t.Errorf("PromptTokens = %d, want 1500", usage.PromptTokens)
	}
	if usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", usage.CompletionTokens)
	}
	if usage.GetCachedTokens() != 1200 {
		t.Errorf("CachedTokens = %d, want 1200", usage.GetCachedTokens())
	}
}
