package headroom

import (
	"fmt"
	"strings"
	"testing"

	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
)

// mcpRoundTrip performs one JSON-RPC request against the server and returns the
// decoded response object. The response is decoded into a generic map because
// a wire-level test is about the envelope, not about Go types.
func mcpRoundTrip(t *testing.T, srv *MCPServer, method, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.HandlePOST(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, rec.Body.String())
	}
	return out
}

// mcpError extracts the error object from a response.
func mcpError(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	err, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error object, got %#v", out)
	}
	return err
}

// mcpFirstText extracts the first text content block of a tool result.
func mcpFirstText(t *testing.T, out map[string]any) string {
	t.Helper()
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result object, got %#v", out)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected content blocks, got %#v", result)
	}
	block, ok := content[0].(map[string]any)
	if !ok || block["type"] != "text" {
		t.Fatalf("expected a text content block, got %#v", content[0])
	}
	text, ok := block["text"].(string)
	if !ok {
		t.Fatalf("text block has no text: %#v", block)
	}
	return text
}

func TestMCPToolsList(t *testing.T) {
	srv := NewMCPServer(NewBuiltinEngine())
	out := mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	if id := out["id"]; id != float64(1) {
		t.Errorf("id = %v, want 1", id)
	}
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %#v", out)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %#v", result["tools"])
	}
	want := map[string]bool{
		"headroom_compress": false,
		"headroom_stats":    false,
	}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is not an object: %#v", raw)
		}
		name, _ := tool["name"].(string)
		if _, known := want[name]; !known {
			t.Errorf("unexpected tool name %q", name)
			continue
		}
		want[name] = true
		if _, ok := tool["inputSchema"].(map[string]any); !ok {
			t.Errorf("tool %s has no inputSchema", name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %s missing from tools/list", name)
		}
	}
}

func TestMCPCompress(t *testing.T) {
	srv := NewMCPServer(NewBuiltinEngine())

	// Build a compressible request body exactly as the client would, then pass
	// its messages array to the tool.
	var reqBody struct {
		Messages []any `json:"messages"`
	}
	if err := json.Unmarshal(buildToolBody(t, "run_tests", "test output line\n"), &reqBody); err != nil {
		t.Fatalf("buildToolBody: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"session_id": "s1",
		"messages":   reqBody.Messages,
	}, json.Deterministic(true))
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	call := fmt.Sprintf(`{"jsonrpc":"2.0","id":"c1","method":"tools/call","params":{"name":"headroom_compress","arguments":%s}}`, args)
	out := mcpRoundTrip(t, srv, http.MethodPost, call)

	if id := out["id"]; id != "c1" {
		t.Errorf("id = %v, want c1", id)
	}
	text := mcpFirstText(t, out)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool result is not JSON: %v\n%s", err, text)
	}
	if payload["compressed"] != true {
		t.Errorf("compressed = %v, want true", payload["compressed"])
	}
	if saved, ok := payload["tokens_saved"].(float64); !ok || saved <= 0 {
		t.Errorf("tokens_saved = %v, want > 0", payload["tokens_saved"])
	}
	if messages, ok := payload["messages"].([]any); !ok || len(messages) == 0 {
		t.Errorf("compress result carries no messages: %#v", payload)
	}
}

func TestMCPCompressFailOpen(t *testing.T) {
	// A conversation with nothing foldable must not fail the call; it answers
	// compressed=false with the original messages.
	srv := NewMCPServer(NewBuiltinEngine())
	call := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"headroom_compress","arguments":{"session_id":"s1","messages":[{"role":"user","content":"short"}]}}}`
	out := mcpRoundTrip(t, srv, http.MethodPost, call)
	if _, hasErr := out["error"]; hasErr {
		t.Fatalf("fail-open call must not error: %#v", out)
	}
	text := mcpFirstText(t, out)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool result is not JSON: %v", err)
	}
	if payload["compressed"] != false {
		t.Errorf("compressed = %v, want false", payload["compressed"])
	}
}

func TestMCPStats(t *testing.T) {
	srv := NewMCPServer(NewBuiltinEngine())

	// One successful compression first, so the counters are non-zero.
	var reqBody struct {
		Messages []any `json:"messages"`
	}
	if err := json.Unmarshal(buildToolBody(t, "run_tests", "test output line\n"), &reqBody); err != nil {
		t.Fatalf("buildToolBody: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"session_id": "s1", "messages": reqBody.Messages}, json.Deterministic(true))
	mcpRoundTrip(t, srv, http.MethodPost, fmt.Sprintf(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"headroom_compress","arguments":%s}}`, args))

	out := mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"headroom_stats"}}`)
	text := mcpFirstText(t, out)
	var stats map[string]any
	if err := json.Unmarshal([]byte(text), &stats); err != nil {
		t.Fatalf("stats are not JSON: %v", err)
	}
	if c := stats["compressions"]; c != float64(1) {
		t.Errorf("compressions = %v, want 1", c)
	}
	if saved, ok := stats["tokens_saved"].(float64); !ok || saved <= 0 {
		t.Errorf("tokens_saved = %v, want > 0", stats["tokens_saved"])
	}

	// The resource mirrors the tool.
	out = mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":9,"method":"resources/read","params":{"uri":"headroom://stats"}}`)
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %#v", out)
	}
	contents, ok := result["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("expected one content block, got %#v", result["contents"])
	}
	block := contents[0].(map[string]any)
	if block["uri"] != "headroom://stats" || block["mimeType"] != "application/json" {
		t.Errorf("unexpected resource contents: %#v", block)
	}
	var resourceStats map[string]any
	if err := json.Unmarshal([]byte(block["text"].(string)), &resourceStats); err != nil {
		t.Fatalf("resource text is not JSON: %v", err)
	}
	if resourceStats["compressions"] != float64(1) {
		t.Errorf("resource compressions = %v, want 1", resourceStats["compressions"])
	}
}

func TestMCPParseError(t *testing.T) {
	srv := NewMCPServer(NewBuiltinEngine())
	out := mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":10,"method":`)

	err := mcpError(t, out)
	if code, ok := err["code"].(float64); !ok || int(code) != -32700 {
		t.Errorf("error code = %v, want -32700", err["code"])
	}
	if msg := err["message"]; msg != "Parse error" {
		t.Errorf("error message = %v, want %q", msg, "Parse error")
	}
	if id, ok := out["id"]; !ok || id != nil {
		t.Errorf("parse-error id = %v, want null", id)
	}
}

func TestMCPErrors(t *testing.T) {
	srv := NewMCPServer(NewBuiltinEngine())

	// Unknown method.
	out := mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":11,"method":"bogus"}`)
	if code := mcpError(t, out)["code"]; code != float64(-32601) {
		t.Errorf("unknown method code = %v, want -32601", code)
	}

	// Unknown tool.
	out = mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"nope"}}`)
	if code := mcpError(t, out)["code"]; code != float64(-32602) {
		t.Errorf("unknown tool code = %v, want -32602", code)
	}

	// Missing tool name.
	out = mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{}}`)
	if code := mcpError(t, out)["code"]; code != float64(-32602) {
		t.Errorf("missing tool code = %v, want -32602", code)
	}

	// Compress without a messages array.
	out = mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"headroom_compress","arguments":{"session_id":"s1"}}}`)
	if code := mcpError(t, out)["code"]; code != float64(-32602) {
		t.Errorf("missing messages code = %v, want -32602", code)
	}

	// Unsupported JSON-RPC version.
	out = mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"1.0","id":16,"method":"tools/list"}`)
	if code := mcpError(t, out)["code"]; code != float64(-32600) {
		t.Errorf("bad version code = %v, want -32600", code)
	}

	// Non-POST is rejected.
	out = mcpRoundTrip(t, srv, http.MethodGet, `{"jsonrpc":"2.0","id":17,"method":"tools/list"}`)
	if code := mcpError(t, out)["code"]; code != float64(-32600) {
		t.Errorf("GET code = %v, want -32600", code)
	}

	// Unknown resource.
	out = mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":18,"method":"resources/read","params":{"uri":"headroom://nope"}}`)
	if code := mcpError(t, out)["code"]; code != float64(-32002) {
		t.Errorf("unknown resource code = %v, want -32002", code)
	}
}

func TestMCPInitialize(t *testing.T) {
	srv := NewMCPServer(NewBuiltinEngine())
	out := mcpRoundTrip(t, srv, http.MethodPost, `{"jsonrpc":"2.0","id":19,"method":"initialize","params":{}}`)
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %#v", out)
	}
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", result["protocolVersion"], mcpProtocolVersion)
	}
}
