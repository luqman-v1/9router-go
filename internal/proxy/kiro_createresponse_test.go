package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKiroCreateResponseURL_RegionSupport(t *testing.T) {
	if got := KiroCreateResponseURL(""); got != "https://runtime.us-east-1.kiro.dev/v1/responses" {
		t.Errorf("empty region: got %s", got)
	}
	if got := KiroCreateResponseURL("us-east-1"); got != "https://runtime.us-east-1.kiro.dev/v1/responses" {
		t.Errorf("us-east-1: got %s", got)
	}
	if got := KiroCreateResponseURL("eu-central-1"); got != "https://runtime.eu-central-1.kiro.dev/v1/responses" {
		t.Errorf("eu-central-1: got %s", got)
	}
}

func TestKiroCreateResponseURL_RegionGuard(t *testing.T) {
	for _, r := range []string{"ap-south-1", "evil.com/x", "us-east-1@evil.com", "US-EAST-1", ""} {
		got := KiroCreateResponseURL(r)
		if got != "https://runtime.us-east-1.kiro.dev/v1/responses" {
			t.Errorf("region %q leaked: got %s", r, got)
		}
	}
}

func TestKiroCreateResponseHeaders(t *testing.T) {
	creds := map[string]any{
		"accessToken": "token_123",
		"providerSpecificData": map[string]any{
			"profileArn": "arn:aws:codewhisperer:us-east-1:12345:profile/TEST",
		},
	}
	h := KiroCreateResponseHeaders(creds, map[string]any{})
	if h["Authorization"] != "Bearer token_123" {
		t.Errorf("Authorization: %s", h["Authorization"])
	}
	if h["x-amzn-kiro-origin"] != "AI_EDITOR" {
		t.Errorf("origin: %s", h["x-amzn-kiro-origin"])
	}
	if h["x-amzn-kiro-profile"] != "arn:aws:codewhisperer:us-east-1:12345:profile/TEST" {
		t.Errorf("profile: %s", h["x-amzn-kiro-profile"])
	}
	if _, ok := h["TokenType"]; ok {
		t.Errorf("TokenType should be absent for bearer, got %s", h["TokenType"])
	}

	// API key auth
	creds2 := map[string]any{
		"apiKey":               "secret_key",
		"providerSpecificData": map[string]any{"authMethod": "api_key"},
	}
	h2 := KiroCreateResponseHeaders(creds2, map[string]any{})
	if h2["Authorization"] != "Bearer secret_key" {
		t.Errorf("api key auth: %s", h2["Authorization"])
	}
	if h2["TokenType"] != "API_KEY" {
		t.Errorf("TokenType: %s", h2["TokenType"])
	}

	// External IdP
	creds3 := map[string]any{
		"accessToken":          "token_ext",
		"providerSpecificData": map[string]any{"authMethod": "external_idp"},
	}
	h3 := KiroCreateResponseHeaders(creds3, map[string]any{})
	if h3["TokenType"] != "EXTERNAL_IDP" {
		t.Errorf("TokenType: %s", h3["TokenType"])
	}
}

func TestKiroCreateResponseBody_Basic(t *testing.T) {
	body := map[string]any{
		"conversationState": map[string]any{
			"conversationId": "conv-uuid-123",
			"history": []any{
				map[string]any{"userInputMessage": map[string]any{"content": "What is the weather?"}},
				map[string]any{"assistantResponseMessage": map[string]any{
					"content":  "Let me check.",
					"toolUses": []any{map[string]any{"toolUseId": "tool_call_1", "name": "get_weather", "input": map[string]any{"location": "Tokyo"}}},
				}},
				map[string]any{"userInputMessage": map[string]any{
					"userInputMessageContext": map[string]any{
						"toolResults": []any{map[string]any{"toolUseId": "tool_call_1", "content": []any{map[string]any{"text": "Sunny, 22C"}}}},
					},
				}},
			},
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": "And in Kyoto?",
					"modelId": "claude-opus-5.5",
					"origin":  "AI_EDITOR",
					"userInputMessageContext": map[string]any{
						"tools": []any{map[string]any{"toolSpecification": map[string]any{
							"name": "get_weather", "description": "Get weather for location",
							"inputSchema": map[string]any{"json": map[string]any{"type": "object"}}},
						}},
					},
				},
			},
		},
		"profileArn": "arn:aws:codewhisperer:us-east-1:12345:profile/TEST",
		"additionalModelRequestFields": map[string]any{
			"thinking":      map[string]any{"type": "adaptive", "display": "summarized"},
			"output_config": map[string]any{"effort": "high"},
		},
	}
	crBody := KiroCreateResponseBody(body, map[string]any{}, "kr/claude-opus-5.5")
	if crBody["model"] != "claude-opus-5.5" {
		t.Errorf("model: %v", crBody["model"])
	}
	if crBody["stream"] != true {
		t.Errorf("stream: %v", crBody["stream"])
	}
	if crBody["origin"] != "AI_EDITOR" {
		t.Errorf("origin: %v", crBody["origin"])
	}
	if crBody["profileArn"] != "arn:aws:codewhisperer:us-east-1:12345:profile/TEST" {
		t.Errorf("profileArn: %v", crBody["profileArn"])
	}
	input, _ := crBody["input"].([]any)
	if len(input) != 5 {
		t.Errorf("input len: %d", len(input))
	}
	tools, _ := crBody["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("tools len: %d", len(tools))
	}
	md, _ := crBody["metadata"].(map[string]any)
	if md["kiro:agent_mode"] != "vibe" {
		t.Errorf("metadata: %v", md)
	}
	if md["kiro:conversation_id"] != "conv-uuid-123" {
		t.Errorf("conv id: %v", md)
	}
	reasoning, _ := crBody["reasoning"].(map[string]any)
	if reasoning["type"] != "adaptive" || reasoning["effort"] != "high" || reasoning["summary"] != "summarized" {
		t.Errorf("reasoning: %v", reasoning)
	}
}

func TestKiroCreateResponseBody_Images(t *testing.T) {
	body := map[string]any{
		"conversationState": map[string]any{
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": "Describe this image",
					"modelId": "claude-opus-5.5",
					"images":  []any{map[string]any{"format": "jpeg", "source": map[string]any{"bytes": "aGVsbG8="}}},
				},
			},
		},
	}
	crBody := KiroCreateResponseBody(body, map[string]any{}, "claude-opus-5.5")
	input, _ := crBody["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input len: %d", len(input))
	}
	m, _ := input[0].(map[string]any)
	if m["role"] != "user" {
		t.Errorf("role: %v", m["role"])
	}
	content, _ := m["content"].([]any)
	if len(content) != 2 {
		t.Errorf("content len: %d", len(content))
	}
}

func TestTransformCreateResponseStreamToSSE(t *testing.T) {
	sse := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello \"}\n\n" +
		"data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"world!\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":25}}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	defer resp.Body.Close()
	tr := TransformCreateResponseStreamToSSE(resp.Body, "claude-opus-5.5")
	b, _ := json.MarshalIndent(nil, "", "")
	_ = b
	data, _ := readAllReader(tr)
	if !strings.Contains(string(data), "Hello ") {
		t.Errorf("missing Hello: %s", data)
	}
	if !strings.Contains(string(data), "world!") {
		t.Errorf("missing world: %s", data)
	}
	if !strings.Contains(string(data), "thinking") {
		t.Errorf("missing thinking: %s", data)
	}
	if !strings.Contains(string(data), `"finish_reason":"stop"`) {
		t.Errorf("missing finish: %s", data)
	}
	if !strings.Contains(string(data), `"prompt_tokens":10`) {
		t.Errorf("missing prompt_tokens: %s", data)
	}
	if !strings.Contains(string(data), "data: [DONE]") {
		t.Errorf("missing DONE: %s", data)
	}
}

func TestTransformCreateResponseStreamToolCalls(t *testing.T) {
	sse := "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_123\",\"name\":\"lookup\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\\\"query\\\\\": \\\\\"test\\\\\"}\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_123\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sse))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	defer resp.Body.Close()
	tr := TransformCreateResponseStreamToSSE(resp.Body, "claude-opus-5.5")
	data, _ := readAllReader(tr)
	if !strings.Contains(string(data), "call_123") {
		t.Errorf("missing call_123: %s", data)
	}
	if !strings.Contains(string(data), "lookup") {
		t.Errorf("missing lookup: %s", data)
	}
	if !strings.Contains(string(data), `"finish_reason":"tool_calls"`) {
		t.Errorf("missing tool_calls finish: %s", data)
	}
}

func readAllReader(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return buf, err
		}
	}
	return buf, nil
}
