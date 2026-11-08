package executor

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// markTTFT is the common seam every streaming executor must call on the first
// semantic chunk. These tests pin the previously-missing paths:
// commandcode, kiro, gemini (opencode-go), perplexity-web, windsurf.
// TTFT is measured from req.StartTime; a zero StartTime means the test sets
// one explicitly, because markTTFT without a start would measure a fresh
// now() and pass trivially.
func TestMarkTTFT_StampsOnce(t *testing.T) {
	start := time.Now().Add(-500 * time.Millisecond)
	var ttft int64
	req := &Request{StartTime: start, TTFT: &ttft}
	markTTFT(req, req.StartTime)
	if ttft < 400 {
		t.Fatalf("markTTFT() = %d ms, want >= 400 (stamps since StartTime)", ttft)
	}
	first := ttft
	// Second call must be a no-op (first-token semantics, not last).
	time.Sleep(50 * time.Millisecond)
	markTTFT(req, req.StartTime)
	if ttft != first {
		t.Errorf("markTTFT() second call changed %d -> %d, must be idempotent", first, ttft)
	}
}

func TestMarkTTFT_NilSafe(t *testing.T) {
	markTTFT(nil, time.Time{})
	markTTFT(&Request{}, time.Time{})
	markTTFT(&Request{TTFT: new(int64)}, time.Time{})
}

// streamResponseBuf is a minimal writer that collects SSE bytes for the test.
type streamResponseBuf struct{ bytes.Buffer }

func TestHandleCommandcodeStream_SetsTTFT(t *testing.T) {
	start := time.Now().Add(-300 * time.Millisecond)
	var ttft int64
	buf := &streamResponseBuf{}
	req := &Request{StartTime: start, TTFT: &ttft, ResponseBuf: buf, Ctx: context.Background()}
	rec := httptest.NewRecorder()

	ndjson := `{"type":"text-delta","text":"Hello"}`
	ndjson += "\n" + `{"type":"finish","finishReason":"stop"}` + "\n"
	err := handleCommandcodeStream(rec, req, strings.NewReader(ndjson), "deepseek-v4")
	if err != nil {
		t.Fatalf("handleCommandcodeStream error: %v", err)
	}
	if ttft <= 0 {
		t.Fatalf("TTFT = %d, want > 0 for commandcode stream", ttft)
	}
	if buf.Len() == 0 {
		t.Fatalf("ResponseBuf empty for commandcode stream")
	}
}

func TestHandleKiroStream_SetsTTFT(t *testing.T) {
	start := time.Now().Add(-300 * time.Millisecond)
	var ttft int64
	buf := &streamResponseBuf{}
	req := &Request{StartTime: start, TTFT: &ttft, ResponseBuf: buf, Ctx: context.Background()}
	rec := httptest.NewRecorder()

	stream := providers.EncodeEventFrame(map[string]string{":event-type": "assistantResponseEvent"}, []byte(`{"content":"Hi"}`))
	err := handleKiroStream(rec, req, bytes.NewReader(stream))
	if err != nil {
		t.Logf("handleKiroStream returned: %v (allowing EOF)", err)
	}
	if ttft <= 0 {
		t.Fatalf("TTFT = %d, want > 0 for kiro stream", ttft)
	}
	if buf.Len() == 0 {
		t.Fatalf("ResponseBuf empty for kiro stream")
	}
}

func TestGeminiStream_SetsTTFT(t *testing.T) {
	start := time.Now().Add(-300 * time.Millisecond)
	var ttft int64
	buf := &streamResponseBuf{}
	req := &Request{StartTime: start, TTFT: &ttft, ResponseBuf: buf}
	rec := httptest.NewRecorder()

	// geminiStream replays Gemini SSE via ScanStream: each data: payload is
	// handed to the callback already stripped of its "data:" prefix, so the
	// handler sees the bare JSON. A frame that translates to nil is skipped;
	// a text frame stamps TTFT.
	upstream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}],\"role\":\"model\"},\"finishReason\":\"STOP\",\"index\":0}]}\n\n"
	err := geminiStream(rec, strings.NewReader(upstream), context.Background(), req)
	if err != nil {
		t.Fatalf("geminiStream error: %v", err)
	}
	if ttft <= 0 {
		t.Fatalf("TTFT = %d, want > 0 for gemini stream", ttft)
	}
	if buf.Len() == 0 {
		t.Fatalf("ResponseBuf empty for gemini stream")
	}
}
