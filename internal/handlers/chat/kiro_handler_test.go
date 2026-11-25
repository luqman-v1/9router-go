package chat

import (
	"bytes"
	"encoding/binary"
	json "encoding/json/v2"
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
		if r.Header.Get("Amz-Sdk-Request") == "" {
			t.Errorf("expected Amz-Sdk-Request header")
		}
		if !strings.Contains(r.Header.Get("User-Agent"), "KiroIDE-") {
			t.Errorf("expected KiroIDE User-Agent, got %q", r.Header.Get("User-Agent"))
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
	payload, _ := json.Marshal(map[string]any{
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

// Kiro streams one tool call across several toolUseEvent frames: the first
// carries only name+toolUseId, the middle ones carry slices of the JSON
// arguments under `input`, and a `stop: true` frame closes it. Emitting per
// frame produced `arguments: "{}"`, so clients could never run the tool.
func TestForwardKiroRequest_ReassemblesFragmentedToolInput(t *testing.T) {
	fragments := []map[string]any{
		{"name": "get_weather", "toolUseId": "tooluse_abc"},
		{"name": "get_weather", "toolUseId": "tooluse_abc", "input": `{"ci`},
		{"name": "get_weather", "toolUseId": "tooluse_abc", "input": `ty": "Jak`},
		{"name": "get_weather", "toolUseId": "tooluse_abc", "input": `arta", "unit": "celsius"}`},
		{"name": "get_weather", "toolUseId": "tooluse_abc", "stop": true},
	}
	var stream []byte
	for _, fragment := range fragments {
		payload, _ := json.Marshal(fragment)
		stream = append(stream, buildEventStreamFrame(map[string]string{
			":event-type": "toolUseEvent",
		}, payload)...)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(stream)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	rec := httptest.NewRecorder()
	if err := executor.ForwardKiro(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "key",
		Body:     []byte(`{"model":"auto","messages":[{"role":"user","content":"weather?"}]}`),
		IsStream: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("expected tool_calls finish_reason, got %s", body)
	}
	// The reassembled arguments must survive SSE escaping as one JSON string.
	if !strings.Contains(body, `{\"city\": \"Jakarta\", \"unit\": \"celsius\"}`) {
		t.Errorf("tool arguments were not reassembled from fragments, got %s", body)
	}
	if strings.Contains(body, `"arguments":"{}"`) {
		t.Errorf("fragmented tool call emitted empty arguments, got %s", body)
	}
}
