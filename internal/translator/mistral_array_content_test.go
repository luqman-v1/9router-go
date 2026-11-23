package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// sseEvents parses a concatenated Claude SSE stream into its typed events.
// [DONE] is a control frame, not a JSON event.
func sseEvents(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, frame := range strings.Split(string(out), "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" || strings.Contains(frame, "data: [DONE]") {
			continue
		}
		idx := strings.Index(frame, "data: ")
		if idx < 0 {
			t.Fatalf("unexpected SSE frame %q", frame)
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(frame[idx+len("data: "):]), &ev); err != nil {
			t.Fatalf("bad SSE payload %q: %v", frame, err)
		}
		events = append(events, ev)
	}
	return events
}

// deltasOfType returns every content_block_delta of the requested kind.
func deltasOfType(events []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if ev["type"] != "content_block_delta" {
			continue
		}
		d, ok := ev["delta"].(map[string]any)
		if ok && d["type"] == kind {
			out = append(out, d)
		}
	}
	return out
}

func eventsOfType(events []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if ev["type"] == kind {
			out = append(out, ev)
		}
	}
	return out
}

func mistralChunk(id, content, finish string) string {
	finishJSON := "null"
	if finish != "" {
		finishJSON = `"` + finish + `"`
	}
	return `{"id":"` + id + `","model":"mistral-large-latest","choices":[{"index":0,"delta":{` + content + `},"finish_reason":` + finishJSON + `}]}`
}

const mistralUsageFinishSuffix = `],"usage":{"prompt_tokens":10,"completion_tokens":2}}`

// A Mistral thinking part inside a content array must become exactly one
// thinking_delta and must never leak into the visible text stream.
func TestMistralArrayContent_ThinkingIsReasoningOnly(t *testing.T) {
	const key = "mistral-array-thinking"
	defer ClearStreamState(key)

	out := streamClaude(t, key,
		mistralChunk(key, `"content":[{"type":"thinking","thinking":[{"type":"text","text":"Check the fixture."}],"closed":true}]`, ""),
		`{"id":"`+key+`","model":"mistral-large-latest","choices":[{"index":0,"delta":{},"finish_reason":"stop"}`+mistralUsageFinishSuffix,
	)
	s := string(out)
	events := sseEvents(t, out)

	thinking := deltasOfType(events, "thinking_delta")
	if len(thinking) != 1 {
		t.Fatalf("thinking_delta count = %d, want 1: %s", len(thinking), s)
	}
	if thinking[0]["thinking"] != "Check the fixture." {
		t.Errorf("thinking = %v, want %q", thinking[0]["thinking"], "Check the fixture.")
	}
	if text := deltasOfType(events, "text_delta"); len(text) != 0 {
		t.Errorf("text_delta count = %d, want 0 (thinking leaked into content): %s", len(text), s)
	}
	if strings.Count(s, "Check the fixture.") != 1 {
		t.Errorf("thinking text must appear exactly once and only as thinking_delta: %s", s)
	}
	if blocks := eventsOfType(events, "content_block_start"); len(blocks) != 1 {
		t.Errorf("content_block_start count = %d, want 1 (thinking block only): %s", len(blocks), s)
	} else {
		p, _ := blocks[0]["content_block"].(map[string]any)
		if p["type"] != "thinking" {
			t.Errorf("block type = %v, want thinking", p["type"])
		}
	}
}

// Text parts are joined in arrival order; non-text parts are ignored and one
// text_delta carries the whole visible string.
func TestMistralArrayContent_TextPartsFlattenToOneDelta(t *testing.T) {
	const key = "mistral-array-text"
	defer ClearStreamState(key)

	out := streamClaude(t, key,
		mistralChunk(key, `"content":[{"type":"text","text":"DO"},{"type":"text","text":"NE"},{"type":"image_url","image_url":{"url":"https://example.test/hidden.png"}}]`, ""),
		`{"id":"`+key+`","model":"mistral-large-latest","choices":[{"index":0,"delta":{},"finish_reason":"stop"}`+mistralUsageFinishSuffix,
	)
	s := string(out)
	events := sseEvents(t, out)

	text := deltasOfType(events, "text_delta")
	if len(text) != 1 {
		t.Fatalf("text_delta count = %d, want 1: %s", len(text), s)
	}
	if text[0]["text"] != "DONE" {
		t.Errorf("text = %v, want %q", text[0]["text"], "DONE")
	}
	if thinking := deltasOfType(events, "thinking_delta"); len(thinking) != 0 {
		t.Errorf("thinking_delta count = %d, want 0: %s", len(thinking), s)
	}
	if strings.Contains(s, "hidden.png") {
		t.Errorf("image part leaked into visible SSE: %s", s)
	}
}

// The full upstream sequence: thinking array, text array, then a tool call.
// The tool call must close the message once with stop_reason tool_use.
func TestMistralArrayContent_ThenToolCallFinishesOnce(t *testing.T) {
	const key = "mistral-array-tool"
	defer ClearStreamState(key)

	out := streamClaude(t, key,
		mistralChunk(key, `"content":[{"type":"thinking","thinking":[{"type":"text","text":"Check the fixture."}],"closed":true}]`, ""),
		mistralChunk(key, `"content":[{"type":"text","text":"DO"},{"type":"text","text":"NE"},{"type":"image_url","image_url":{"url":"https://example.test/hidden.png"}}]`, ""),
		mistralChunk(key, `"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]`, "tool_calls"),
		"[DONE]",
	)
	s := string(out)
	events := sseEvents(t, out)

	thinking := deltasOfType(events, "thinking_delta")
	if len(thinking) != 1 || thinking[0]["thinking"] != "Check the fixture." {
		t.Fatalf("thinking deltas = %v, want one %q: %s", thinking, "Check the fixture.", s)
	}
	text := deltasOfType(events, "text_delta")
	if len(text) != 1 || text[0]["text"] != "DONE" {
		t.Fatalf("text deltas = %v, want one %q: %s", text, "DONE", s)
	}
	jsonDeltas := deltasOfType(events, "input_json_delta")
	if len(jsonDeltas) != 1 {
		t.Fatalf("input_json_delta count = %d, want 1: %s", len(jsonDeltas), s)
	}
	if part, _ := jsonDeltas[0]["partial_json"].(string); !strings.Contains(part, "Paris") {
		t.Errorf("partial_json = %q, want the tool arguments", part)
	}
	msgDelta := eventsOfType(events, "message_delta")
	if len(msgDelta) != 1 {
		t.Fatalf("message_delta count = %d, want 1: %s", len(msgDelta), s)
	}
	delta, _ := msgDelta[0]["delta"].(map[string]any)
	if delta["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", delta["stop_reason"])
	}
	if stops := eventsOfType(events, "message_stop"); len(stops) != 1 {
		t.Errorf("message_stop count = %d, want 1: %s", len(stops), s)
	}
}

// Plain string content must keep its existing behaviour exactly.
func TestMistralPlainStringContentUnchanged(t *testing.T) {
	const key = "mistral-string-content"
	defer ClearStreamState(key)

	out := streamClaude(t, key,
		mistralChunk(key, `"content":"plain answer"`, ""),
		`{"id":"`+key+`","model":"mistral-large-latest","choices":[{"index":0,"delta":{},"finish_reason":"stop"}`+mistralUsageFinishSuffix,
	)
	s := string(out)
	text := deltasOfType(sseEvents(t, out), "text_delta")
	if len(text) != 1 || text[0]["text"] != "plain answer" {
		t.Fatalf("text deltas = %v, want one %q: %s", text, "plain answer", s)
	}
}

// Lower-level coverage for the two flattening helpers, including the nested
// thinking shape and the ignored part types.
func TestExtractContentHelpers(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantText  string
		wantThink string
	}{
		{"plain string", `"hello"`, "hello", ""},
		{"null content", `null`, "", ""},
		{"text parts with image", `[{"type":"text","text":"DO"},{"type":"text","text":"NE"},{"type":"image_url","image_url":{"url":"x"}}]`, "DONE", ""},
		{"thinking as array of parts", `[{"type":"thinking","thinking":[{"type":"text","text":"Check the fixture."}],"closed":true}]`, "", "Check the fixture."},
		{"thinking as plain string", `[{"type":"thinking","thinking":"plain thought","closed":true}]`, "", "plain thought"},
		{"mixed thinking and text", `[{"type":"thinking","thinking":"why"},{"type":"text","text":"answer"}]`, "answer", "why"},
		{"non-text part with text field", `[{"type":"image_url","text":"not visible"}]`, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var delta OpenAIDelta
			if err := json.Unmarshal([]byte(`{"content":`+tc.content+`,"role":"assistant"}`), &delta); err != nil {
				t.Fatalf("unmarshal delta: %v", err)
			}
			if delta.Content != tc.wantText {
				t.Errorf("Content = %q, want %q", delta.Content, tc.wantText)
			}
			if got := extractReasoningText(delta); got != tc.wantThink {
				t.Errorf("extractReasoningText = %q, want %q", got, tc.wantThink)
			}
		})
	}
}

// Decoding an array content delta must not disturb any of the other fields.
func TestOpenAIDeltaArrayContentKeepsOtherFields(t *testing.T) {
	const payload = `{"role":"assistant","content":[{"type":"text","text":"hi"}],"reasoning_content":"legacy","reason_result":"result","reasoning":"reason","reasoning_details":[{"type":"text","text":"detail"}],"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}`
	var delta OpenAIDelta
	if err := json.Unmarshal([]byte(payload), &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta.Role != "assistant" || delta.Content != "hi" {
		t.Errorf("role/content = %q/%q, want assistant/hi", delta.Role, delta.Content)
	}
	if delta.ReasoningContent != "legacy" || delta.ReasonResult != "result" || delta.Reasoning != "reason" {
		t.Errorf("reasoning fields = %q/%q/%q", delta.ReasoningContent, delta.ReasonResult, delta.Reasoning)
	}
	if len(delta.ReasoningDetails) != 1 || delta.ReasoningDetails[0].Text != "detail" {
		t.Errorf("reasoning_details = %+v", delta.ReasoningDetails)
	}
	if len(delta.ToolCalls) != 1 || delta.ToolCalls[0].ID != "call_1" ||
		delta.ToolCalls[0].Function == nil || delta.ToolCalls[0].Function.Name != "Read" {
		t.Errorf("tool_calls = %+v", delta.ToolCalls)
	}
}
