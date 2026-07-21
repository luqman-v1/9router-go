package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

func buildEventStreamFrame(headers map[string]string, payload []byte) []byte {
	var headerBytes []byte
	for k, v := range headers {
		name := []byte(k)
		val := []byte(v)
		headerBytes = append(headerBytes, byte(len(name)))
		headerBytes = append(headerBytes, name...)
		headerBytes = append(headerBytes, 7)
		valLen := make([]byte, 2)
		binary.BigEndian.PutUint16(valLen, uint16(len(val)))
		headerBytes = append(headerBytes, valLen...)
		headerBytes = append(headerBytes, val...)
	}

	headerLen := len(headerBytes)
	payloadLen := len(payload)
	totalLen := 12 + headerLen + payloadLen + 4

	frame := make([]byte, totalLen)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headerLen))
	preludeCRC := crc32.ChecksumIEEE(frame[0:8])
	binary.BigEndian.PutUint32(frame[8:12], preludeCRC)
	copy(frame[12:12+headerLen], headerBytes)
	if payloadLen > 0 {
		copy(frame[12+headerLen:12+headerLen+payloadLen], payload)
	}
	msgCRC := crc32.ChecksumIEEE(frame[0 : totalLen-4])
	binary.BigEndian.PutUint32(frame[totalLen-4:], msgCRC)

	return frame
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardKiro(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-kiro",
		Body:     body,
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "kiro response") {
		t.Errorf("expected content in SSE, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Errorf("expected [DONE] at end, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"finish_reason":"stop"`) {
		t.Errorf("expected stop finish_reason, got %s", rec.Body.String())
	}
}

func TestForwardKiroRequest_ReasoningContent(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{
		"text":    "thinking step by step",
		"content": "fallback content",
	})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "reasoningContentEvent",
	}, payload)

	payload2, _ := json.Marshal(map[string]string{"content": "final answer"})
	frame2 := buildEventStreamFrame(map[string]string{
		":event-type": "assistantResponseEvent",
	}, payload2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(append(frame, frame2...))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := []byte(`{"model":"grok-4","messages":[]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardKiro(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "key",
		Body:     body,
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "thinking step by step") {
		t.Errorf("expected reasoning_content in SSE, got %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "final answer") {
		t.Errorf("expected content in SSE, got %s", bodyStr)
	}
}

func TestForwardKiroRequest_ToolUse(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{
		"toolUseId": "call_abc123",
		"name":      "get_weather",
		"content":   `{"location":"Jakarta"}`,
	})
	frame := buildEventStreamFrame(map[string]string{
		":event-type": "toolUseEvent",
	}, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(frame)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := []byte(`{"model":"grok-4","messages":[]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardKiro(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "key",
		Body:     body,
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "get_weather") {
		t.Errorf("expected tool name in SSE, got %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"finish_reason":"tool_calls"`) {
		t.Errorf("expected tool_calls finish_reason, got %s", bodyStr)
	}
}

func TestForwardKiroRequest_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardKiro(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "bad-key",
		Body:     body,
		IsStream: true,
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", ue.StatusCode)
	}
}

func TestEventStreamReader_ReadFrame(t *testing.T) {
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
