package executor

import (
	"strings"
	"testing"
)

// joinEvents renders processed events back to SSE wire form (as callers do).
func joinEvents(events []SSEEvent) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Type != "" {
			b.WriteString("event: " + ev.Type + "\ndata: ")
		} else {
			b.WriteString("data: ")
		}
		b.Write(ev.Payload)
		b.WriteString("\n\n")
	}
	return b.String()
}

// ScanStream strips the "data:" prefix before invoking callbacks, while the
// raw SSE path keeps it. ClaudeStreamDecloaker must handle both forms.
func TestClaudeStreamDecloaker_BareAndSSEForms(t *testing.T) {
	m := map[string]string{"bash_ide": "bash"}
	event := `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_x","name":"bash_ide"}}`

	d1 := NewClaudeStreamDecloaker(m)
	sse := joinEvents(d1.Events([]byte("data: " + event + "\n\n")))
	if !strings.Contains(sse, `"name":"bash"`) || strings.Contains(sse, "bash_ide") {
		t.Fatalf("SSE form not decloaked: %s", sse)
	}

	d2 := NewClaudeStreamDecloaker(m)
	bare := joinEvents(d2.Events([]byte(event)))
	if strings.Contains(bare, "data: data:") {
		t.Fatalf("bare form must not gain a second data: prefix: %q", bare)
	}
	if !strings.Contains(bare, `"name":"bash"`) || strings.Contains(bare, "bash_ide") {
		t.Fatalf("bare form not decloaked: %s", bare)
	}

	// Unknown / untracked names pass through untouched.
	d3 := NewClaudeStreamDecloaker(m)
	unknown := joinEvents(d3.Events([]byte(`{"type":"content_block_start","content_block":{"type":"tool_use","name":"other_ide","id":"i"}}`)))
	if !strings.Contains(unknown, `"name":"other_ide"`) {
		t.Fatalf("unknown name should pass through: %s", unknown)
	}
}

// The [DONE] sentinel must come back bare — a second "data:" prefix would
// break the caller's "[DONE]" check and double the sentinel.
func TestClaudeStreamDecloaker_DoneNoDoublePrefix(t *testing.T) {
	d := NewClaudeStreamDecloaker(map[string]string{"bash_ide": "bash"})
	events := d.Events([]byte("data: [DONE]\n\n"))
	if len(events) != 1 {
		t.Fatalf("expected exactly one event, got %d", len(events))
	}
	if string(events[0].Payload) != "[DONE]" {
		t.Fatalf("expected bare [DONE], got %q", string(events[0].Payload))
	}
	if strings.Contains(joinEvents(events), "data: data:") {
		t.Fatalf("double data: prefix on [DONE]")
	}
}

// extractEventType must read the top-level "type", not a nested one.
func TestExtractEventType_RootOnly(t *testing.T) {
	payload := `{"content_block":{"type":"tool_use","name":"Task"},"type":"content_block_start"}`
	if got := extractEventType(payload); got != "content_block_start" {
		t.Fatalf("expected content_block_start, got %q", got)
	}
	if got := extractEventType(`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`); got != "message_delta" {
		t.Fatalf("expected message_delta, got %q", got)
	}
	if got := extractEventType(`not json`); got != "" {
		t.Fatalf("expected empty for non-JSON, got %q", got)
	}
}

func TestClaudeStreamDecloaker_ConvertsDecoyStream(t *testing.T) {
	m := map[string]string{"calc_ide": "calc"}
	d := NewClaudeStreamDecloaker(m)

	// 1. content_block_start with decoy Task: converted to text, plus a
	// synthetic text_delta so the notice reaches OpenAI-format clients.
	chunk1 := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Task"}}`)
	events := d.Events(chunk1)
	if len(events) != 2 {
		t.Fatalf("expected converted start + synthetic text_delta, got %d events", len(events))
	}
	if !strings.Contains(string(events[0].Payload), `"type":"text"`) {
		t.Fatalf("expected decoy block start converted to text: %s", string(events[0].Payload))
	}
	if events[1].Type != "content_block_delta" ||
		!strings.Contains(string(events[1].Payload), `"type":"text_delta"`) ||
		!strings.Contains(string(events[1].Payload), "[Tool Task is unavailable]") {
		t.Fatalf("expected synthetic text_delta with notice: %s", string(events[1].Payload))
	}

	// 2. content_block_delta for decoy Task should be suppressed.
	chunk2 := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`)
	if events := d.Events(chunk2); len(events) != 0 {
		t.Fatalf("expected decoy delta to be suppressed, got %v", events)
	}
	// 3. content_block_stop should clear decoy block index.
	chunkStop := []byte(`{"type":"content_block_stop","index":0}`)
	if events := d.Events(chunkStop); len(events) == 0 {
		t.Fatalf("expected content_block_stop not suppressed")
	}
	if len(d.decoyBlocks) != 0 {
		t.Fatalf("decoyBlocks should be cleared on content_block_stop, got %v", d.decoyBlocks)
	}

	// 4. message_delta with stop_reason tool_use should be rewritten to end_turn.
	chunk3 := []byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`)
	out3 := joinEvents(d.Events(chunk3))
	if !strings.Contains(out3, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected stop_reason rewritten to end_turn: %s", out3)
	}
}

func TestDecloakClaudeResponseBody_DecloaksTools(t *testing.T) {
	m := map[string]string{"my_calc_ide": "my_calc"}
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "text", "text": "calling tools"},
			{"type": "tool_use", "id": "toolu_1", "name": "my_calc_ide", "input": {"a": 1}},
			{"type": "tool_use", "id": "toolu_2", "name": "untracked_tool", "input": {"x": 2}}
		]
	}`)

	out := DecloakClaudeResponseBody(body, m)
	s := string(out)
	if !strings.Contains(s, `"name":"my_calc"`) {
		t.Fatalf("my_calc_ide should be decloaked to my_calc: %s", s)
	}
	if strings.Contains(s, "my_calc_ide") {
		t.Fatalf("my_calc_ide should not be present: %s", s)
	}
	if !strings.Contains(s, `"name":"untracked_tool"`) {
		t.Fatalf("untracked_tool should pass through: %s", s)
	}
}

func TestDecloakClaudeResponseBody_ConvertsDecoysAndFixesStopReason(t *testing.T) {
	m := map[string]string{"calc_ide": "calc"}
	body := []byte(`{
		"id": "msg_1",
		"type": "message",
		"role": "assistant",
		"stop_reason": "tool_use",
		"content": [
			{"type": "tool_use", "id": "t1", "name": "Task", "input": {}}
		]
	}`)

	out := string(DecloakClaudeResponseBody(body, m))
	if !strings.Contains(out, `"type":"text"`) || !strings.Contains(out, "[Tool Task is unavailable]") {
		t.Fatalf("expected decoy converted to text notice: %s", out)
	}
	if strings.Contains(out, `"tool_use"`) || strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected decoy tool_use and stop_reason gone: %s", out)
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected stop_reason end_turn: %s", out)
	}
}
