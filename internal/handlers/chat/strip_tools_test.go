package chat

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestStripToolsForNonToolModel(t *testing.T) {
	body := []byte(`{"model":"@cf/meta/llama-3.1-8b-instruct-fp8-fast","tools":[{"type":"function","function":{"name":"recall"}}],"tool_choice":"auto","messages":[{"role":"system","content":"s"},{"role":"user","content":"q"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"recall","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"result"}]}`)

	got := stripToolsForNonToolModel(body)
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("bad output JSON: %v", err)
	}
	for _, key := range []string{"tools", "tool_choice", "functions"} {
		if _, ok := obj[key]; ok {
			t.Fatalf("unexpected top-level %q after strip: %s", key, got)
		}
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) != 4 {
		t.Fatalf("unexpected messages: %s", got)
	}
	assistant, _ := msgs[2].(map[string]any)
	if _, ok := assistant["tool_calls"]; ok {
		t.Fatalf("assistant tool_calls survived: %s", got)
	}
	content, _ := assistant["content"].(string)
	if !strings.Contains(content, "recall") {
		t.Fatalf("tool call not flattened to prose: %s", got)
	}
	toolResult, _ := msgs[3].(map[string]any)
	resultContent, _ := toolResult["content"].(string)
	if toolResult["role"] != "user" || !strings.Contains(resultContent, "result") {
		t.Fatalf("tool result not flattened: %s", got)
	}
}

func TestStripToolsForNonToolModel_InvalidBodyUnchanged(t *testing.T) {
	in := []byte(`not json`)
	if got := stripToolsForNonToolModel(in); string(got) != string(in) {
		t.Fatalf("invalid body changed: %q", got)
	}
}

func TestStripToolsForNonToolModel_NoToolsNoChange(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	got := stripToolsForNonToolModel(in)
	var before, after map[string]any
	_ = json.Unmarshal(in, &before)
	_ = json.Unmarshal(got, &after)
	if len(before) != len(after) {
		t.Fatalf("shape changed without tools: %s", got)
	}
}
