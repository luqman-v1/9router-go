package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// buildEventStreamFrame builds a valid AWS EventStream binary frame.
// Pass nil payload for a frame with no payload body (just headers).
func buildEventStreamFrame(headers map[string]string, payload []byte) []byte {
	var headerBytes []byte
	for k, v := range headers {
		name := []byte(k)
		val := []byte(v)
		headerBytes = append(headerBytes, byte(len(name)))
		headerBytes = append(headerBytes, name...)
		headerBytes = append(headerBytes, 7) // 7 = string type
		valLen := make([]byte, 2)
		binary.BigEndian.PutUint16(valLen, uint16(len(val)))
		headerBytes = append(headerBytes, valLen...)
		headerBytes = append(headerBytes, val...)
	}

	headerLen := len(headerBytes)
	payloadLen := len(payload)
	// total = 12 (prelude) + headerLen + payloadLen + 4 (trailing CRC)
	totalLen := 12 + headerLen + payloadLen + 4

	frame := make([]byte, totalLen)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headerLen))
	// prelude CRC (bytes 0-7)
	preludeCRC := crc32.ChecksumIEEE(frame[0:8])
	binary.BigEndian.PutUint32(frame[8:12], preludeCRC)
	// headers
	copy(frame[12:12+headerLen], headerBytes)
	// payload
	if payloadLen > 0 {
		copy(frame[12+headerLen:12+headerLen+payloadLen], payload)
	}
	// trailing CRC (everything before this)
	msgCRC := crc32.ChecksumIEEE(frame[0 : totalLen-4])
	binary.BigEndian.PutUint32(frame[totalLen-4:], msgCRC)

	return frame
}

func TestHandleKiroStream_AssistantResponse(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"content": "hello from kiro"})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "assistantResponseEvent",
	}, payload)

	upstream := bytes.NewReader(frame)
	rec := httptest.NewRecorder()
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	err := h.handleKiroStream(rec, upstream, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello from kiro") {
		t.Errorf("expected content in SSE, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Errorf("expected [DONE] at end, got %s", rec.Body.String())
	}
	// Non-stream translateResponse=false, metrics=nil — just check output
	if !strings.Contains(rec.Body.String(), `"finish_reason":"stop"`) {
		t.Errorf("expected stop finish_reason, got %s", rec.Body.String())
	}
}

func TestHandleKiroStream_ReasoningContent(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{
		"text":    "thinking step by step",
		"content": "fallback content",
	})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "reasoningContentEvent",
	}, payload)

	// Also emit assistant response so stream doesn't end silently
	payload2, _ := json.Marshal(map[string]string{"content": "final answer"})
	frame2 := buildEventStreamFrame(map[string]string{
		":event-type": "assistantResponseEvent",
	}, payload2)

	upstream := bytes.NewReader(append(frame, frame2...))
	rec := httptest.NewRecorder()
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	err := h.handleKiroStream(rec, upstream, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "thinking step by step") {
		t.Errorf("expected reasoning_content in SSE, got %s", body)
	}
	if !strings.Contains(body, "final answer") {
		t.Errorf("expected content in SSE, got %s", body)
	}
}

func TestHandleKiroStream_ToolUse(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{
		"toolUseId": "call_abc123",
		"name":      "get_weather",
		"input":     map[string]interface{}{"location": "Jakarta"},
	})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "toolUseEvent",
	}, payload)

	upstream := bytes.NewReader(frame)
	rec := httptest.NewRecorder()
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	err := h.handleKiroStream(rec, upstream, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "get_weather") {
		t.Errorf("expected tool name in SSE, got %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("expected tool_calls finish_reason, got %s", body)
	}
}

func TestHandleKiroStream_CodeEvent(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"content": "func main() {}"})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "codeEvent",
	}, payload)

	upstream := bytes.NewReader(frame)
	rec := httptest.NewRecorder()
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	err := h.handleKiroStream(rec, upstream, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "func main() {}") {
		t.Errorf("expected code in SSE, got %s", rec.Body.String())
	}
}

func TestHandleKiroStream_MultipleFrames(t *testing.T) {
	payload1, _ := json.Marshal(map[string]string{"content": "hello "})
	payload2, _ := json.Marshal(map[string]string{"content": "world"})
	f1 := buildEventStreamFrame(map[string]string{":event-type": "assistantResponseEvent"}, payload1)
	f2 := buildEventStreamFrame(map[string]string{":event-type": "assistantResponseEvent"}, payload2)

	upstream := bytes.NewReader(append(f1, f2...))
	rec := httptest.NewRecorder()
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	err := h.handleKiroStream(rec, upstream, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hello ") || !strings.Contains(body, "world") {
		t.Errorf("expected both chunks, got %s", body)
	}
}

func TestHandleKiroStream_EmptyContent(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"content": ""})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "assistantResponseEvent",
	}, payload)

	// Empty content should be skipped — frame with no content
	upstream := bytes.NewReader(frame)
	rec := httptest.NewRecorder()
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	err := h.handleKiroStream(rec, upstream, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only [DONE] and finish_reason should be present
	if strings.Contains(rec.Body.String(), `"content"`) && strings.Count(rec.Body.String(), `"content"`) > 2 {
		t.Errorf("expected no content delta for empty content, got %s", rec.Body.String())
	}
}

func TestKiroEventStream_ReadFrame(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"content": "test"})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "assistantResponseEvent",
	}, payload)

	reader := providers.NewEventStreamReader(bytes.NewReader(frame))
	parsed, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame error: %v", err)
	}
	if parsed == nil {
		t.Fatal("expected frame, got nil")
	}
	if parsed.Headers[":event-type"] != "assistantResponseEvent" {
		t.Errorf("expected event-type header, got %v", parsed.Headers)
	}
	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(parsed.Payload, &result); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if result.Content != "test" {
		t.Errorf("expected content 'test', got %q", result.Content)
	}
}

func TestForwardKiroRequest_Success(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"content": "kiro response"})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "assistantResponseEvent",
	}, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
			t.Errorf("expected X-Amz-Target header")
		}
		if r.Header.Get("Amz-Sdk-Request") == "" {
			t.Errorf("expected Amz-Sdk-Request header")
		}
		w.WriteHeader(http.StatusOK)
		w.Write(frame)
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.forwardKiroRequest(rec, cfg, "sk-kiro", body, true, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "kiro response") {
		t.Errorf("expected content in SSE, got %s", rec.Body.String())
	}
}
