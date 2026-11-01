package proxy

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func fillJSON(t *testing.T, f *StreamToolCallIDFill, payload string) map[string]any {
	t.Helper()
	out, _ := f.Fill([]byte(payload))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("patched payload is not JSON: %v\n%s", err, out)
	}
	return m
}

func firstToolCall(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	choices, _ := m["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	calls, _ := delta["tool_calls"].([]any)
	tc, _ := calls[0].(map[string]any)
	return tc
}

// A fragment opening a new index without an id must reach the client with a
// stable id, reused by later fragments of the same index.
func TestStreamToolCallIDFill_MintsAndReusesPerIndex(t *testing.T) {
	f := NewStreamToolCallIDFill()

	m := fillJSON(t, f, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"name":"read","arguments":"{\"p\""}}]}}]}`)
	tc := firstToolCall(t, m)
	id, ok := tc["id"].(string)
	if !ok || id == "" {
		t.Fatalf("minted id missing: %v", tc)
	}
	if !strings.HasPrefix(id, "call_1_") {
		t.Errorf("minted id %q does not carry the index", id)
	}

	// A later fragment for the same index reuses the minted id.
	m = fillJSON(t, f, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"path\":\"x\"}"}}]}}]}`)
	if got := firstToolCall(t, m)["id"]; got != id {
		t.Errorf("id not stable for index: got %v, want %v", got, id)
	}

	// An id the provider does send is remembered and passed through.
	m = fillJSON(t, f, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"call_abc","function":{"name":"ls"}}]}}]}`)
	if got := firstToolCall(t, m)["id"]; got != "call_abc" {
		t.Errorf("explicit id altered: %v", got)
	}
	// Empty placeholder is dropped
	out, changed := f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2}]}}]}`))
	if !changed {
		t.Error("expected change for empty placeholder")
	}
	var m2 map[string]any
	json.Unmarshal(out, &m2)
	if _, ok := m2["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"]; ok {
		t.Error("empty index-only placeholder not dropped")
	}

	// Subsequent non-empty fragment for index 2 reuses the seen id
	m = fillJSON(t, f, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"function":{"arguments":"arg"}}]}}]}`)
	if got := firstToolCall(t, m)["id"]; got != "call_abc" {
		t.Errorf("fragment for index 2 lost the seen id: %v", got)
	}
}

func TestStreamToolCallIDFill_LeavesCleanChunksUntouched(t *testing.T) {
	f := NewStreamToolCallIDFill()
	plain := `{"choices":[{"index":0,"delta":{"content":"hello \"tool_calls\" world"}}]}`
	out, changed := f.Fill([]byte(plain))
	if changed {
		t.Errorf("content chunk modified: %s", out)
	}
	// The escaped needle must not trip the fast path into a parse.
	if !strings.Contains(string(out), "\\\"tool_calls\\\"") {
		t.Errorf("escaped content corrupted: %s", out)
	}

	if _, changed := f.Fill([]byte(`not json`)); changed {
		t.Error("unparseable payload modified")
	}
	if _, changed := f.Fill([]byte(`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`)); changed {
		t.Error("content-only chunk modified")
	}
}

// The SSECopy relay patches a data line in place and leaves everything else
// byte-identical.
func TestSSECopy_FillsToolCallIDs(t *testing.T) {
	rec := &mockResponseWriter{}
	upstream := strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"read\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n")
	if err := SSECopy(rec, upstream, rec, nil); err != nil {
		t.Fatalf("SSECopy: %v", err)
	}
	out := rec.String()
	if !strings.Contains(out, `"id":"call_0_`) {
		t.Errorf("minted id not in relay: %s", out)
	}
	if strings.Count(out, `"id":"call_0_`) != 2 {
		t.Errorf("second fragment did not reuse the minted id: %s", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("terminal frame lost: %s", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Errorf("[DONE] lost: %s", out)
	}
}

func TestStreamToolCallIDFill_DropsEmptyPlaceholders(t *testing.T) {
	// 1. All-null payload
	f := NewStreamToolCallIDFill()
	out, changed := f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":null,"function":{"name":null,"arguments":null}}]}}]}`))
	if !changed {
		t.Error("expected change for all-null tool call placeholder")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	choices := m["choices"].([]any)
	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	if _, ok := delta["tool_calls"]; ok {
		t.Errorf("tool_calls not removed: %s", out)
	}
	if _, known := f.seen[0]; known {
		t.Error("id allocated for empty placeholder")
	}

	// 2. Index-only fragment
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0}]}}]}`))
	json.Unmarshal(out, &m)
	if _, ok := m["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"]; ok {
		t.Error("tool_calls not removed for index-only fragment")
	}

	// 3. Empty function fields
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","id":"","function":{"name":"","arguments":""}}]}}]}`))
	json.Unmarshal(out, &m)
	if _, ok := m["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"]; ok {
		t.Error("tool_calls not removed for empty function fields")
	}

	// 4. Does not leave empty tool_calls array beside reasoning_content
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking","tool_calls":[{"index":0}]}}]}`))
	json.Unmarshal(out, &m)
	delta = m["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if _, ok := delta["tool_calls"]; ok {
		t.Error("empty tool_calls array left beside reasoning_content")
	}
	if delta["reasoning_content"] != "thinking" {
		t.Errorf("reasoning_content lost: %v", delta)
	}

	// 5. Real start with empty arguments is KEPT and recorded in f.seen
	f = NewStreamToolCallIDFill()
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real","type":"function","function":{"name":"read","arguments":""}}]}}]}`))
	if !strings.Contains(string(out), `"id":"call_real"`) {
		t.Errorf("real start not kept: %s", out)
	}
	if f.seen[0] != "call_real" {
		t.Errorf("real start id not recorded in seen: %v", f.seen)
	}

	// 6. Argument fragments (including whitespace like " ") are KEPT and reuse ID
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" "}}]}}]}`))
	if !strings.Contains(string(out), `"id":"call_real"`) {
		t.Errorf("whitespace argument fragment dropped or id lost: %s", out)
	}

	// 7. Id-only and name-only fragments are KEPT
	f = NewStreamToolCallIDFill()
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_id_only"}}]}}]}`))
	if !strings.Contains(string(out), `"id":"call_id_only"`) {
		t.Errorf("id-only fragment dropped: %s", out)
	}

	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"function":{"name":"ls"}}]}}]}`))
	if !strings.Contains(string(out), `"name":"ls"`) {
		t.Errorf("name-only fragment dropped: %s", out)
	}

	// 8. Custom tool types ("custom") are KEPT
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":3,"type":"custom"}}]}}]}`))
	if !strings.Contains(string(out), `"type":"custom"`) {
		t.Errorf("custom tool type dropped: %s", out)
	}

	// 9. Provider extensions (extra_content, provider_metadata) are KEPT
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":4,"provider_metadata":true}]}}]}`))
	if !strings.Contains(string(out), `provider_metadata`) {
		t.Errorf("provider extension dropped: %s", out)
	}

	// 10. Preserves sibling content, finish_reason, and usage
	out, _ = f.Fill([]byte(`{"choices":[{"index":0,"delta":{"content":"abc","tool_calls":[{"index":0}],"finish_reason":"stop"}}],"usage":{"prompt_tokens":10}}`))
	json.Unmarshal(out, &m)
	delta = m["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "abc" || delta["finish_reason"] != "stop" {
		t.Errorf("sibling fields lost: %v", delta)
	}
	if _, ok := delta["tool_calls"]; ok {
		t.Error("empty tool_calls not removed")
	}
}
