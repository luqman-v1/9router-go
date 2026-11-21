package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

func TestKiroUpstreamBody_ConvertsOpenAIToConversationState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		// The gateway answers 400 on anything without conversationState; assert it here.
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Errorf("upstream got non-JSON: %v", err)
		}
		if _, ok := m["conversationState"]; !ok {
			t.Errorf("upstream body missing conversationState (would 400): %s", data)
		}
		if _, ok := m["systemPrompt"]; ok {
			t.Error("upstream body must not carry top-level systemPrompt")
		}
		// Empty eventstream so handleKiroStream completes without events.
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"kr/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	_ = ForwardKiro(rec, &Request{
		Client:    srv.Client(),
		Config:    cfg,
		APIKey:    "bearer-token",
		Body:      body,
		IsStream:  false,
		ModelName: "claude-sonnet-4.5",
	})
	// Error is fine (empty eventstream); the wire-format assertion is the point.
}

func TestKiroUpstreamBody_PassesThroughConversationState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		// cleanKiroBody may normalize the envelope, but it must stay an
		// envelope (not be re-wrapped from an OpenAI body).
		cs, ok := m["conversationState"].(map[string]any)
		if !ok {
			t.Errorf("MITM passthrough lost conversationState: %s", data)
		}
		if _, hasMessages := m["messages"]; hasMessages {
			t.Error("MITM passthrough should not inject an OpenAI messages array")
		}
		if cs["conversationId"] != "x" {
			t.Errorf("passthrough conversationId changed: %v", cs["conversationId"])
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	rec := httptest.NewRecorder()
	body := []byte(`{"conversationState":{"chatTriggerType":"MANUAL","conversationId":"x","currentMessage":{"userInputMessage":{"content":"hi","modelId":"m","origin":"AI_EDITOR"}},"history":[]}}`)
	_ = ForwardKiro(rec, &Request{
		Client: srv.Client(),
		Config: cfg,
		APIKey: "bearer-token",
		Body:   body,
	})
}

func TestKiroUpstreamBody_StripsProviderPrefixAndSuffixes(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		sent = string(data)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"kr/claude-sonnet-4.5-thinking","messages":[{"role":"user","content":"hi"}]}`)
	_ = ForwardKiro(rec, &Request{
		Client: srv.Client(),
		Config: cfg,
		APIKey: "bearer-token",
		Body:   body,
	})
	if !strings.Contains(sent, `"modelId":"claude-sonnet-4.5"`) {
		t.Errorf("expected stripped modelId claude-sonnet-4.5, got %s", sent)
	}
	if strings.Contains(sent, "kr/") {
		t.Errorf("provider prefix leaked into body: %s", sent)
	}
}

// kiroEventFrame builds one AWS EventStream frame. The reader in internal/providers
// verifies both CRCs, so the fixture has to write them.
func kiroEventFrame(eventType, payload string) []byte {
	return providers.EncodeEventFrame(map[string]string{":event-type": eventType}, []byte(payload))
}

// A `stream:false` request must be answered with JSON. Kiro is EventStream
// only, so before the fix this replied `text/event-stream` with an SSE body
// and the client's JSON.parse failed on `data: {...}` (issue #41).
func TestForwardKiro_NonStreamReturnsJSONCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":"Hey"}`))
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":" there"}`))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	err := ForwardKiro(rec, &Request{
		Client:    srv.Client(),
		Config:    &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"},
		APIKey:    "bearer-token",
		Body:      []byte(`{"model":"kr/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		IsStream:  false,
		ModelName: "claude-sonnet-4.5",
	})
	if err != nil {
		t.Fatalf("ForwardKiro: %v", err)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON, so a client JSON.parse fails: %v\nbody: %s", err, rec.Body.String())
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", out.Object)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("got %d choices, want 1", len(out.Choices))
	}
	if out.Choices[0].Message.Content != "Hey there" {
		t.Errorf("content = %q, want %q", out.Choices[0].Message.Content, "Hey there")
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", out.Choices[0].FinishReason)
	}
}

// The fix must not disturb the streaming path: a `stream:true` request still
// has to answer with an event stream.
func TestForwardKiro_StreamStillEmitsSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":"Hey"}`))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	err := ForwardKiro(rec, &Request{
		Client:    srv.Client(),
		Config:    &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"},
		APIKey:    "bearer-token",
		Body:      []byte(`{"model":"kr/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream:  true,
		ModelName: "claude-sonnet-4.5",
	})
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ForwardKiro: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(rec.Body.String(), `"content":"Hey"`) {
		t.Errorf("stream body missing the assistant delta: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Errorf("stream body missing the terminal frame: %s", rec.Body.String())
	}
}

func TestKiroIntegrityGateToggle(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "on", want: true},
		{value: "off", want: false},
		{value: " OFF ", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "no", want: false},
	}
	for _, tt := range tests {
		t.Run("KIRO_INTEGRITY_GATE="+tt.value, func(t *testing.T) {
			t.Setenv(kiroIntegrityGateEnv, tt.value)
			if got := kiroIntegrityGateEnabled(); got != tt.want {
				t.Errorf("kiroIntegrityGateEnabled() with %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// A tool call is fragmented across frames, so the gate holds it until the
// stream ends. With the gate off the fragments must reach the client as they
// arrive instead — the reason the toggle exists.
func TestHandleKiroStream_IntegrityGateOffStreamsToolFragments(t *testing.T) {
	t.Setenv(kiroIntegrityGateEnv, "off")

	stream := kiroEventFrame("toolUseEvent", `{"name":"read","toolUseId":"t1","input":"{\"pa"}`)
	stream = append(stream, kiroEventFrame("toolUseEvent", `{"name":"read","toolUseId":"t1","input":"th\":\"a\"}"}`)...)
	stream = append(stream, kiroEventFrame("assistantResponseEvent", `{"content":"done"}`)...)

	rec := httptest.NewRecorder()
	req := &Request{Ctx: context.Background()}
	if err := handleKiroStream(rec, req, bytes.NewReader(stream)); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("handleKiroStream: %v", err)
	}

	body := rec.Body.String()
	// Every fragment is its own chunk, so the call is emitted more than once.
	if got := strings.Count(body, `"tool_calls"`); got < 2 {
		t.Errorf("gate off should emit each fragment separately, got %d tool_calls chunks: %s", got, body)
	}
	// The first fragment carries the identity, the continuation only arguments.
	if !strings.Contains(body, `"id":"t1"`) || !strings.Contains(body, `"name":"read"`) {
		t.Errorf("first fragment must carry id and name: %s", body)
	}
	if !strings.Contains(body, `{\"pa`) {
		t.Errorf("first argument fragment missing from stream: %s", body)
	}
}

// With the gate on (the default) the fragments are reassembled and the client
// sees exactly one complete call, which is what a client that does not
// concatenate arguments needs.
func TestHandleKiroStream_IntegrityGateOnEmitsCompleteToolCall(t *testing.T) {
	t.Setenv(kiroIntegrityGateEnv, "")

	stream := kiroEventFrame("toolUseEvent", `{"name":"read","toolUseId":"t1","input":"{\"path\":"}`)
	stream = append(stream, kiroEventFrame("toolUseEvent", `{"name":"read","toolUseId":"t1","input":"\"a\"}"}`)...)

	rec := httptest.NewRecorder()
	req := &Request{Ctx: context.Background()}
	if err := handleKiroStream(rec, req, bytes.NewReader(stream)); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("handleKiroStream: %v", err)
	}

	body := rec.Body.String()
	if got := strings.Count(body, `"tool_calls":[`); got != 1 {
		t.Errorf("gate on should emit one reassembled tool_calls chunk, got %d: %s", got, body)
	}
	if !strings.Contains(body, `{\"path\":\"a\"}`) {
		t.Errorf("reassembled arguments missing from stream: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("tool turn must finish with tool_calls: %s", body)
	}
}

// A /v1/messages client asked for Anthropic SSE. Kiro's frames are synthesized
// as chat chunks, so they must be translated on the way out — a
// chat.completion.chunk body is not something an Anthropic client can read.
func TestHandleKiroStream_ClaudeClientGetsAnthropicSSE(t *testing.T) {
	stream := kiroEventFrame("assistantResponseEvent", `{"content":"KIRO-OK"}`)

	rec := httptest.NewRecorder()
	req := &Request{Ctx: context.Background(), ClaudeClient: true, IsStream: true}
	if err := handleKiroStream(rec, req, bytes.NewReader(stream)); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("handleKiroStream: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_stop", "KIRO-OK"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "chat.completion.chunk") || strings.Contains(body, "[DONE]") {
		t.Errorf("chat-shaped SSE reached a Claude client:\n%s", body)
	}
}

// The non-streaming fold reads these same chunks with sseToOpenAIJSON, so the
// Claude translation must stay off when the client is not streaming.
func TestHandleKiroNonStream_ClaudeClientStillFoldsToClaudeJSON(t *testing.T) {
	stream := kiroEventFrame("assistantResponseEvent", `{"content":"KIRO-OK"}`)

	rec := httptest.NewRecorder()
	req := &Request{Ctx: context.Background(), ClaudeClient: true, TranslateResp: true}
	if err := handleKiroNonStream(rec, req, bytes.NewReader(stream)); err != nil {
		t.Fatalf("handleKiroNonStream: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "event: ") || strings.Contains(body, "chat.completion") {
		t.Errorf("non-streaming answer must be one folded JSON document: %s", body)
	}
	if !strings.Contains(body, "KIRO-OK") {
		t.Errorf("answer text missing: %s", body)
	}
}
