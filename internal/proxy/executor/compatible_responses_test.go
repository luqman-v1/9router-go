package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// compatibleResponsesWire is what the fake /responses upstream received.
type compatibleResponsesWire struct {
	path string
	body string
}

// newCompatibleResponsesUpstream serves a minimal Responses SSE reply and
// records the request the gateway sent.
func newCompatibleResponsesUpstream(t *testing.T) (*httptest.Server, *compatibleResponsesWire) {
	t.Helper()
	wire := &compatibleResponsesWire{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		wire.path = r.URL.Path
		wire.body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, wire
}

// TestForwardCompatibleResponsesSendsResponsesBody pins the contract a custom
// openai-compatible node with apiType "responses" depends on: the body that
// reaches its /responses endpoint must be a Responses payload, not the Chat
// Completions payload the client sent. Before the executor existed, the generic
// chat forwarder sent messages[] and nested tools, and the upstream answered
// "function tool name is required".
func TestForwardCompatibleResponsesSendsResponsesBody(t *testing.T) {
	chatBody := []byte(`{
		"model": "gemini-4-argon",
		"messages": [
			{"role": "system", "content": "be terse"},
			{"role": "user", "content": "weather in Jakarta?"}
		],
		"tools": [
			{"type": "function", "function": {
				"name": "get_weather",
				"description": "Get weather for a city",
				"parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}
			}}
		]
	}`)

	srv, wire := newCompatibleResponsesUpstream(t)
	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/v1/responses"}

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   cfg,
		Provider: "openai-compatible-responses-studio",
		APIKey:   "as_test",
		Body:     chatBody,
		IsStream: false,
	}

	if err := ForwardCompatibleResponses(rec, req); err != nil {
		t.Fatalf("ForwardCompatibleResponses() error = %v", err)
	}

	if wire.body == "" {
		t.Fatal("upstream received no body")
	}

	var sent struct {
		Input    []map[string]any `json:"input"`
		Messages []map[string]any `json:"messages"`
		Tools    []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal([]byte(wire.body), &sent); err != nil {
		t.Fatalf("upstream body is not JSON: %v (body=%s)", err, wire.body)
	}

	t.Run("messages are converted to input", func(t *testing.T) {
		if len(sent.Input) == 0 {
			t.Errorf("input is empty; the Responses API rejects a body without it (body=%s)", wire.body)
		}
		if len(sent.Messages) != 0 {
			t.Errorf("messages[] leaked to a Responses endpoint: %v", sent.Messages)
		}
	})

	t.Run("tool declaration is flat", func(t *testing.T) {
		if len(sent.Tools) != 1 {
			t.Fatalf("tools length = %d, want 1 (body=%s)", len(sent.Tools), wire.body)
		}
		tool := sent.Tools[0]
		// The name must sit at the top level. Nested under "function" is the
		// Chat Completions shape, which is exactly what the upstream rejects
		// with "function tool name is required".
		if _, nested := tool["function"]; nested {
			t.Errorf("tool kept its nested Chat shape: %v", tool)
		}
		if got, _ := tool["name"].(string); got != "get_weather" {
			t.Errorf("tool name = %q, want %q", got, "get_weather")
		}
		if got, _ := tool["type"].(string); got != "function" {
			t.Errorf("tool type = %q, want %q", got, "function")
		}
		if tool["parameters"] == nil {
			t.Error("tool lost its parameters schema")
		}
	})
}

// TestForwardCompatibleResponsesPassesResponsesBodyThrough guards the
// idempotency buildResponsesBody provides: a client that already speaks
// Responses must not have its body rebuilt into a second, nested shape.
func TestForwardCompatibleResponsesPassesResponsesBodyThrough(t *testing.T) {
	responsesBody := []byte(`{
		"model": "gemini-4-argon",
		"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}],
		"tools": [{"type": "function", "name": "get_weather", "parameters": {"type": "object", "properties": {}}}]
	}`)

	srv, wire := newCompatibleResponsesUpstream(t)
	cfg := &providers.ProviderConfig{BaseURL: srv.URL + "/v1/responses"}

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   cfg,
		Provider: "openai-compatible-responses-studio",
		APIKey:   "as_test",
		Body:     responsesBody,
		IsStream: false,
	}

	if err := ForwardCompatibleResponses(rec, req); err != nil {
		t.Fatalf("ForwardCompatibleResponses() error = %v", err)
	}

	var sent struct {
		Tools []map[string]any `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(wire.body), &sent); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	if len(sent.Input) != 1 {
		t.Errorf("input length = %d, want 1 (body=%s)", len(sent.Input), wire.body)
	}
	if len(sent.Tools) != 1 {
		t.Fatalf("tools length = %d, want 1", len(sent.Tools))
	}
	if _, nested := sent.Tools[0]["function"]; nested {
		t.Errorf("an already-flat tool was re-wrapped: %v", sent.Tools[0])
	}
	if got, _ := sent.Tools[0]["name"].(string); got != "get_weather" {
		t.Errorf("tool name = %q, want %q", got, "get_weather")
	}
}

// TestForwardCompatibleResponsesSurfacesUpstreamStatus proves a provider error
// reaches the caller as an upstreamError, so account fallback can try the next
// connection instead of streaming an error page to the client as content.
func TestForwardCompatibleResponsesSurfacesUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)

	rec := httptest.NewRecorder()
	err := ForwardCompatibleResponses(rec, &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL + "/v1/responses"},
		Provider: "openai-compatible-responses-studio",
		APIKey:   "as_test",
		Body:     []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		IsStream: false,
	})
	if err == nil {
		t.Fatal("a 429 upstream returned no error")
	}
}
