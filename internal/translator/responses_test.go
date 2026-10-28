package translator

import (
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

func respState() *ResponsesState {
	return InitResponsesState("test-model", true)
}

func eventNames(events []ResponsesEvent) []string {
	names := make([]string, len(events))
	for i, ev := range events {
		names[i] = ev.Event
	}
	return names
}

func findEvent(t *testing.T, events []ResponsesEvent, name string) map[string]any {
	t.Helper()
	for _, ev := range events {
		if ev.Event == name {
			return ev.Data
		}
	}
	t.Fatalf("event %q not found; got %v", name, eventNames(events))
	return nil
}

func hasEvent(events []ResponsesEvent, name string) bool {
	for _, ev := range events {
		if ev.Event == name {
			return true
		}
	}
	return false
}

func intPtr(v int) *int { return &v }

func strPtr(v string) *string { return &v }

// --- text stream ---

func TestTranslateOpenAIToResponses_TextStream(t *testing.T) {
	s := respState()

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		ID: "chatcmpl-1",
		Choices: []OpenAIChoice{{
			Index: 0,
			Delta: OpenAIDelta{Role: "assistant", Content: "Hello"},
		}},
	}, s)

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
	}
	if got := eventNames(events); !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	created := findEvent(t, events, "response.created")
	if created["type"] != "response.created" {
		t.Errorf("created.type = %v", created["type"])
	}
	if id := created["response"].(map[string]any)["id"]; id != "resp_chatcmpl-1" {
		t.Errorf("response id = %v, want resp_chatcmpl-1 (derived from chunk id)", id)
	}

	delta := findEvent(t, events, "response.output_text.delta")
	if delta["delta"] != "Hello" {
		t.Errorf("delta = %v, want Hello", delta["delta"])
	}
	if delta["item_id"] != s.messageItemID(0) {
		t.Errorf("item_id = %v, want %v", delta["item_id"], s.messageItemID(0))
	}
}

func TestTranslateOpenAIToResponses_SequenceNumbersAreMonotonic(t *testing.T) {
	s := respState()

	var all []ResponsesEvent
	all = append(all, TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "a"}}},
	}, s)...)
	all = append(all, TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "b"}}},
	}, s)...)
	all = append(all, TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("stop")}},
	}, s)...)

	for i, ev := range all {
		seq, ok := ev.Data["sequence_number"].(int64)
		if !ok {
			t.Fatalf("event %d (%s) has sequence_number %T, want int64", i, ev.Event, ev.Data["sequence_number"])
		}
		if want := int64(i + 1); seq != want {
			t.Errorf("event %d (%s) sequence_number = %d, want %d", i, ev.Event, seq, want)
		}
	}
}

func TestTranslateOpenAIToResponses_MessageItemOpenedOnce(t *testing.T) {
	s := respState()

	// Only the first chunk may announce the message item; every later chunk of
	// the same message must reuse it.
	for i, text := range []string{"a", "b", "c"} {
		events := TranslateOpenAIToResponses(&OpenAIChunk{
			Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: text}}},
		}, s)
		announced := hasEvent(events, "response.output_item.added")
		if want := i == 0; announced != want {
			t.Fatalf("chunk %q announced=%v, want %v", text, announced, want)
		}
		if !hasEvent(events, "response.output_text.delta") {
			t.Fatalf("chunk %q produced no text delta", text)
		}
	}

	final := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("stop")}},
	}, s)

	done := findEvent(t, final, "response.output_text.done")
	if done["text"] != "abc" {
		t.Errorf("buffered text = %v, want abc", done["text"])
	}
}

// --- reasoning ---

func TestTranslateOpenAIToResponses_ReasoningContent(t *testing.T) {
	s := respState()

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ReasoningContent: "thinking"}}},
	}, s)

	if !hasEvent(events, "response.output_item.added") {
		t.Error("reasoning did not open an output item")
	}
	if !hasEvent(events, "response.reasoning_summary_part.added") {
		t.Error("reasoning did not open a summary part")
	}
	rd := findEvent(t, events, "response.reasoning_summary_text.delta")
	if rd["delta"] != "thinking" {
		t.Errorf("reasoning delta = %v, want thinking", rd["delta"])
	}

	// The answer text closes reasoning before the message starts.
	events = TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "answer"}}},
	}, s)
	if !hasEvent(events, "response.reasoning_summary_text.done") {
		t.Error("reasoning was not closed when the answer started")
	}
	if !hasEvent(events, "response.output_item.done") {
		t.Error("reasoning output_item.done not emitted on close")
	}
}

func TestTranslateOpenAIToResponses_ReasoningDetails(t *testing.T) {
	s := respState()

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ReasoningDetails: []OpenAIReasoningDetail{
			{Text: "step one "},
			{Content: "step two"},
		}}}},
	}, s)

	rd := findEvent(t, events, "response.reasoning_summary_text.delta")
	if rd["delta"] != "step one step two" {
		t.Errorf("reasoning delta = %v, want \"step one step two\"", rd["delta"])
	}
}

func TestTranslateOpenAIToResponses_ThinkBlockSplit(t *testing.T) {
	s := respState()

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "<think>reasoning</think>answer"}}},
	}, s)

	rd := findEvent(t, events, "response.reasoning_summary_text.delta")
	if rd["delta"] != "reasoning" {
		t.Errorf("reasoning delta = %v, want reasoning", rd["delta"])
	}
	if hasEvent(events, "response.reasoning_summary_text.done") == false {
		t.Error("reasoning was not closed at </think>")
	}
	td := findEvent(t, events, "response.output_text.delta")
	if td["delta"] != "answer" {
		t.Errorf("text delta = %v, want answer (thinking text must not leak into the answer)", td["delta"])
	}
}

func TestTranslateOpenAIToResponses_ThinkBlockAcrossChunks(t *testing.T) {
	s := respState()

	// Content before </think> must stay reasoning, not become answer text.
	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "<think>more "}}},
	}, s)
	if hasEvent(events, "response.output_text.delta") {
		t.Error("text inside <think> was emitted as answer text")
	}

	events = TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "thought</think>final"}}},
	}, s)
	td := findEvent(t, events, "response.output_text.delta")
	if td["delta"] != "final" {
		t.Errorf("text delta = %v, want final", td["delta"])
	}
}

func TestTranslateOpenAIToResponses_CloseReasoningIsIdempotent(t *testing.T) {
	s := respState()

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ReasoningContent: "t"}}},
	}, s)

	first := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "a"}}},
	}, s)
	second := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "b"}}},
	}, s)

	if hasEvent(first, "response.reasoning_summary_text.done") != true {
		t.Error("reasoning should close when the answer starts")
	}
	if hasEvent(second, "response.reasoning_summary_text.done") {
		t.Error("reasoning closed twice")
	}
}

// --- tool calls ---

func TestTranslateOpenAIToResponses_FunctionCall(t *testing.T) {
	s := respState()

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "let me check"}}},
	}, s)

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
			Index:    intPtr(0),
			ID:       "call_1",
			Function: &OpenAIFunctionStream{Name: "get_weather", Arguments: `{"city":`},
		}}}}},
	}, s)

	added := findEvent(t, events, "response.output_item.added")
	item := added["item"].(map[string]any)
	if item["id"] != "fc_call_1" {
		t.Errorf("item id = %v, want fc_call_1", item["id"])
	}
	if item["type"] != "function_call" {
		t.Errorf("item type = %v, want function_call", item["type"])
	}
	if _, ok := item["arguments"]; !ok {
		t.Error("function_call item must carry arguments")
	}
	if !hasEvent(events, "response.output_text.done") {
		t.Error("text message was not closed before the tool call")
	}

	// Second fragment continues the same call.
	events = TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
			Index:    intPtr(0),
			Function: &OpenAIFunctionStream{Arguments: `"Jakarta"}`},
		}}}}},
	}, s)
	if hasEvent(events, "response.output_item.added") {
		t.Error("tool item was announced twice")
	}

	// Finish closes it with the full argument buffer.
	events = TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("tool_calls")}},
	}, s)

	done := findEvent(t, events, "response.function_call_arguments.done")
	if done["arguments"] != `{"city":"Jakarta"}` {
		t.Errorf("arguments = %v, want the joined buffer", done["arguments"])
	}
	itemDone := findEvent(t, events, "response.output_item.done")
	got := itemDone["item"].(map[string]any)
	if got["name"] != "get_weather" || got["call_id"] != "call_1" {
		t.Errorf("closed item = %v", got)
	}
}

func TestTranslateOpenAIToResponses_ToolCallWaitsForNameAndID(t *testing.T) {
	s := respState()

	// The name arrives on its own chunk, with no call id yet.
	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
			Index:    intPtr(0),
			Function: &OpenAIFunctionStream{Name: "exec"},
		}}}}},
	}, s)
	if hasEvent(events, "response.output_item.added") {
		t.Error("tool item announced before the call id arrived")
	}

	events = TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
			Index: intPtr(0),
			ID:    "call_9",
		}}}}},
	}, s)
	if !hasEvent(events, "response.output_item.added") {
		t.Error("tool item not announced once both id and name were known")
	}
}

func TestTranslateOpenAIToResponses_CustomToolCall(t *testing.T) {
	s := respState()
	s.SetCustomToolNames([]string{"exec"})

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
			Index:    intPtr(0),
			ID:       "call_2",
			Function: &OpenAIFunctionStream{Name: "exec", Arguments: `{"input":"ls -la"}`},
		}}}}},
	}, s)

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("tool_calls")}},
	}, s)

	if hasEvent(events, "response.function_call_arguments.done") {
		t.Error("custom tool must not emit function_call_arguments.done")
	}
	input := findEvent(t, events, "response.custom_tool_call_input.done")
	if input["input"] != "ls -la" {
		t.Errorf("input = %v, want the unwrapped freeform program \"ls -la\"", input["input"])
	}
	item := findEvent(t, events, "response.output_item.done")["item"].(map[string]any)
	if item["id"] != "ctc_call_2" || item["type"] != "custom_tool_call" {
		t.Errorf("closed item = %v, want a ctc_ prefixed custom_tool_call", item)
	}
}

func TestExtractCustomToolInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"unwraps input envelope", `{"input":"print(1)"}`, "print(1)"},
		{"passes through plain object", `{"cmd":"ls"}`, `{"cmd":"ls"}`},
		{"passes through invalid json", `{not json`, `{not json`},
		{"passes through bare string", `ls -la`, `ls -la`},
		{"empty object has no input", `{}`, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractCustomToolInput(tt.in); got != tt.want {
				t.Errorf("extractCustomToolInput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- usage ---

func TestTranslateOpenAIToResponses_UsageFromEmptyChoicesChunk(t *testing.T) {
	s := respState()

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("stop")}},
	}, s)

	// OpenAI reports usage on a trailing chunk with no choices at all. It must
	// not be dropped by the choices guard.
	trailing := TranslateOpenAIToResponses(&OpenAIChunk{
		Usage: &OpenAIUsage{
			PromptTokens:            10,
			CompletionTokens:        4,
			PromptTokensDetails:     &PromptTokensDetails{CachedTokens: 3},
			CompletionTokensDetails: &CompletionTokensDetails{ReasoningTokens: 2},
		},
	}, s)

	if len(trailing) != 0 {
		t.Errorf("empty-choices chunk produced %v, want no events", eventNames(trailing))
	}

	completed := TranslateOpenAIToResponses(nil, s)
	payload := findEvent(t, completed, "response.completed")["response"].(map[string]any)
	usage := payload["usage"].(*ResponsesUsage)
	if usage.InputTokens != 10 || usage.OutputTokens != 4 || usage.TotalTokens != 14 {
		t.Errorf("usage = %+v, want 10/4/14", usage)
	}
	if usage.InputTokensDetails == nil || usage.InputTokensDetails.CachedTokens != 3 {
		t.Errorf("input details = %+v, want cached_tokens 3", usage.InputTokensDetails)
	}
	if usage.OutputTokensDetails == nil || usage.OutputTokensDetails.ReasoningTokens != 2 {
		t.Errorf("output details = %+v, want reasoning_tokens 2", usage.OutputTokensDetails)
	}
}

func TestTranslateOpenAIToResponses_NoUsageOmitsUsageField(t *testing.T) {
	s := respState()

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "hi"}}},
	}, s)
	completed := TranslateOpenAIToResponses(nil, s)

	payload := findEvent(t, completed, "response.completed")["response"].(map[string]any)
	if _, ok := payload["usage"]; ok {
		t.Error("usage must be omitted when the upstream never reported any")
	}
}

// --- completion deferral ---

func TestTranslateOpenAIToResponses_DefersCompletionToFlush(t *testing.T) {
	s := respState()

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("stop")}},
	}, s)
	if hasEvent(events, "response.completed") {
		t.Error("completed was emitted before the trailing usage chunk arrived")
	}

	flushed := TranslateOpenAIToResponses(nil, s)
	if !hasEvent(flushed, "response.completed") {
		t.Error("flush did not emit the deferred completion")
	}
}

func TestTranslateOpenAIToResponses_CompletesImmediatelyOnPivotHop(t *testing.T) {
	// When the terminal chunk never reaches the translator, deferring would
	// swallow response.completed entirely.
	s := InitResponsesState("test-model", false)

	events := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("stop")}},
	}, s)
	if !hasEvent(events, "response.completed") {
		t.Error("pivot hop must complete on the finish frame, not defer")
	}
}

func TestFlushResponses_NoopAfterCompleted(t *testing.T) {
	s := respState()

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("stop")}},
	}, s)
	if got := TranslateOpenAIToResponses(nil, s); !hasEvent(got, "response.completed") {
		t.Fatal("first flush did not complete")
	}
	if got := TranslateOpenAIToResponses(nil, s); len(got) != 0 {
		t.Errorf("second flush produced %v, want nothing", eventNames(got))
	}
}

func TestTranslateOpenAIToResponses_EmptyChunkIsNoop(t *testing.T) {
	s := respState()

	if got := TranslateOpenAIToResponses(&OpenAIChunk{}, s); len(got) != 0 {
		t.Errorf("empty chunk produced %v, want nothing", eventNames(got))
	}
	if s.Started {
		t.Error("an empty chunk must not open the stream")
	}
}

// --- issue #4307: response.completed must carry the output ---

func TestResponsesCompletedCarriesOutput(t *testing.T) {
	s := respState()

	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Delta: OpenAIDelta{Content: "the answer"}}},
	}, s)
	// No usage has been seen yet, so the completion is deferred to the flush.
	all := TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{FinishReason: strPtr("stop")}},
	}, s)
	all = append(all, TranslateOpenAIToResponses(nil, s)...)
	payload := findEvent(t, all, "response.completed")["response"].(map[string]any)
	output, ok := payload["output"].([]map[string]any)
	if !ok {
		t.Fatalf("output = %T, want []map[string]any", payload["output"])
	}
	if len(output) != 1 {
		t.Fatalf("output has %d items, want 1", len(output))
	}
	if output[0]["type"] != "message" {
		t.Errorf("output item type = %v, want message", output[0]["type"])
	}

	// The recorded item must be the one output_item.done delivered, and it must
	// carry the full answer text rather than an empty placeholder.
	var doneItem map[string]any
	for _, ev := range all {
		if ev.Event == "response.output_item.done" {
			doneItem = ev.Data["item"].(map[string]any)
		}
	}
	if doneItem == nil {
		t.Fatal("no output_item.done was emitted")
	}
	if !reflect.DeepEqual(doneItem, output[0]) {
		t.Error("completed.output does not match the item delivered by output_item.done")
	}
	if got := output[0]["content"].([]any)[0].(map[string]any)["text"]; got != "the answer" {
		t.Errorf("completed item text = %v, want the streamed answer", got)
	}
}

func TestResponsesCompletedOutputIsOrderedByIndex(t *testing.T) {
	s := respState()

	// Reasoning lands on index 0, the message on 1, the tool call on 2.
	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Index: 0, Delta: OpenAIDelta{ReasoningContent: "think"}}},
	}, s)
	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Index: 1, Delta: OpenAIDelta{Content: "answer"}}},
	}, s)
	TranslateOpenAIToResponses(&OpenAIChunk{
		Choices: []OpenAIChoice{{Index: 2, Delta: OpenAIDelta{ToolCalls: []OpenAIToolCallStream{{
			Index:    intPtr(2),
			ID:       "call_x",
			Function: &OpenAIFunctionStream{Name: "lookup", Arguments: "{}"},
		}}}}},
	}, s)

	completed := TranslateOpenAIToResponses(nil, s)
	output := findEvent(t, completed, "response.completed")["response"].(map[string]any)["output"].([]map[string]any)

	wantTypes := []string{"reasoning", "message", "function_call"}
	if len(output) != len(wantTypes) {
		t.Fatalf("output has %d items, want %d", len(output), len(wantTypes))
	}
	for i, want := range wantTypes {
		if output[i]["type"] != want {
			t.Errorf("output[%d].type = %v, want %v", i, output[i]["type"], want)
		}
	}
}

func TestRecordCompletedOutputItem_OverwritesOnRepeatedClose(t *testing.T) {
	s := respState()

	s.recordCompletedOutputItem(0, map[string]any{"type": "message", "text": "first"})
	s.recordCompletedOutputItem(0, map[string]any{"type": "message", "text": "second"})

	output := s.collectCompletedOutputItems()
	if len(output) != 1 {
		t.Fatalf("output has %d items, want 1 (a repeated close must overwrite)", len(output))
	}
	if output[0]["text"] != "second" {
		t.Errorf("output[0].text = %v, want second", output[0]["text"])
	}
}

func TestCollectCompletedOutputItems_EmptyIsArrayNotNull(t *testing.T) {
	s := respState()

	payload, err := json.Marshal(s.collectCompletedOutputItems())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(payload) != "[]" {
		t.Errorf("empty output marshalled to %s, want [] (null breaks clients)", payload)
	}
}

// --- SSE rendering ---

func TestFormatResponsesSSE(t *testing.T) {
	frame := FormatResponsesSSE(ResponsesEvent{
		Event: "response.created",
		Data:  map[string]any{"type": "response.created", "id": "resp_1"},
	})

	if !strings.HasPrefix(frame, "event: response.created\ndata: ") {
		t.Fatalf("frame = %q, want an event:/data: SSE frame", frame)
	}
	if !strings.HasSuffix(frame, "\n\n") {
		t.Errorf("frame = %q, want a trailing blank line", frame)
	}
	if !strings.Contains(frame, `"type":"response.created"`) {
		t.Errorf("frame = %q, missing the type discriminator", frame)
	}
}

func TestFormatResponsesFailedSSE(t *testing.T) {
	frame := FormatResponsesFailedSSE(map[string]any{
		"code":    "context_length_exceeded",
		"message": "Too long",
		"type":    "invalid_request_error",
	})

	if !strings.HasPrefix(frame, "event: response.failed\ndata: ") {
		t.Fatalf("frame = %q, want response.failed event", frame)
	}
	if !strings.HasSuffix(frame, "\n\n") {
		t.Errorf("frame = %q, want trailing blank line", frame)
	}
	if !strings.Contains(frame, `"code":"context_length_exceeded"`) {
		t.Errorf("frame = %q, missing error code", frame)
	}
	if !strings.Contains(frame, `"status":"failed"`) {
		t.Errorf("frame = %q, missing status failed", frame)
	}
}

// --- helpers ---

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// equalJSON compares two decoded values structurally. Marshaling to a string and
// comparing bytes is not usable here: encoding/json/v2 does not promise a stable
// key order, so the same map can render differently in two contexts.
func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	return reflect.DeepEqual(a, b)
}
