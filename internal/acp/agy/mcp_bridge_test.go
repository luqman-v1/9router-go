package agy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestMCPBridge(t *testing.T) {
	tools := []MCPTool{{
		Name:        "get_weather",
		Description: "Get weather",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
	}}
	bridge, err := StartMCPBridge(context.Background(), tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	call := func(method string, params any) map[string]any {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		resp, err := http.Post(bridge.URL(), "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	init := call("initialize", map[string]any{})
	if init["result"] == nil {
		t.Fatalf("initialize failed: %v", init)
	}
	list := call("tools/list", map[string]any{})
	result, _ := list["result"].(map[string]any)
	listed, _ := result["tools"].([]any)
	if len(listed) != 1 {
		t.Fatalf("tools/list = %v", listed)
	}
	call("tools/call", map[string]any{"name": "get_weather", "arguments": map[string]any{"city": "Jakarta"}})

	select {
	case captured := <-bridge.Calls():
		if captured.Name != "get_weather" {
			t.Errorf("captured tool = %q", captured.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("tool call not captured")
	}
}

func TestToolsFromOpenAI(t *testing.T) {
	raw := json.RawMessage(`[{"type":"function","function":{"name":"lookup","description":"d","parameters":{"type":"object"}}}]`)
	tools := ToolsFromOpenAI(raw)
	if len(tools) != 1 || tools[0].Name != "lookup" {
		t.Fatalf("ToolsFromOpenAI = %+v", tools)
	}
}

func TestMCPBridgeContext(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	cancel()
	// Bridge without tools is not started by RunTurn; just verify the helpers
	// compile and are cancel-safe.
	if bridgeCalls(nil) != nil {
		t.Fatal("bridgeCalls(nil) should be nil")
	}
}

// A seeded tool result must not re-emit a tool_call: on a fresh session that
// would restart the round-trip forever instead of letting agy finish the turn.
func TestMCPBridgeSeededNoToolCall(t *testing.T) {
	bridge, err := StartMCPBridge(context.Background(), []MCPTool{{Name: "get_weather", InputSchema: map[string]any{}}}, map[string]string{"get_weather": "sunny"})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "get_weather", "arguments": map[string]any{}}})
	resp, err := http.Post(bridge.URL(), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	select {
	case <-bridge.Calls():
		t.Fatal("seeded tool call was re-emitted")
	case <-time.After(100 * time.Millisecond):
	}
}
