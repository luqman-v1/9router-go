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
	m = fillJSON(t, f, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2}]}}]}`)
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
