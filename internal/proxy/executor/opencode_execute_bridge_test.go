package executor

import (
	json "encoding/json/v2"
	"net/http/httptest"
	"strings"
	"testing"
)

// chunkLine serializes one SSE data frame from a decoded value.
func chunkLine(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(encoded) + "\n\n"
}

// callDelta builds a tool_calls delta for chunkLine.
func callDelta(frags ...map[string]any) map[string]any {
	anyFrags := make([]any, len(frags))
	for i, f := range frags {
		anyFrags[i] = f
	}
	return map[string]any{"index": 0, "delta": map[string]any{"tool_calls": anyFrags}}
}

func choiceWrap(choice map[string]any) map[string]any {
	return map[string]any{"choices": []any{choice}}
}

func fullCallToolArgs() string {
	// {"code":"CALL_TOOL get_weather {\"city\":\"Jakarta\"}"} — a strict
	// model's execute-wrapped call, as one arguments string.
	return "{\"code\":\"CALL_TOOL get_weather {\\\"city\\\":\\\"Jakarta\\\"}\"}"
}

// TestBridgeRewritesStreamingExecuteCall feeds a fragmented execute/CALL_TOOL
// stream through the bridge writer and expects one complete rewritten call to
// the caller's tool name, with everything else preserved.
func TestBridgeRewritesStreamingExecuteCall(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewOpenCodeExecuteBridgeWriter(rec)
	args := fullCallToolArgs()
	mid := len(args) / 2

	startFrag := map[string]any{"index": 0, "id": "call_1", "function": map[string]any{"name": "execute", "arguments": args[:mid]}}
	endFrag := map[string]any{"index": 0, "function": map[string]any{"arguments": args[mid:]}}

	roleChunk := choiceWrap(map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}})
	startChunk := choiceWrap(callDelta(startFrag))
	endChunk := choiceWrap(callDelta(endFrag))
	finishChunk := choiceWrap(map[string]any{"index": 0, "finish_reason": "tool_calls", "delta": map[string]any{}})

	for _, l := range []string{chunkLine(t, roleChunk), chunkLine(t, startChunk), chunkLine(t, endChunk), chunkLine(t, finishChunk), "data: [DONE]\n\n"} {
		if _, err := w.Write([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"name":"get_weather"`) {
		t.Fatalf("rewritten call missing, got: %s", out)
	}
	if strings.Contains(out, `"name":"execute"`) {
		t.Fatalf("execute name leaked to the client: %s", out)
	}
	if !strings.Contains(out, "Jakarta") {
		t.Fatalf("rewritten arguments missing, got: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatal("finish line dropped")
	}
	if !strings.Contains(out, "[DONE]") {
		t.Fatal("[DONE] dropped")
	}
	if strings.Index(out, `"name":"get_weather"`) > strings.Index(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("finish line precedes the rewritten call: %s", out)
	}
}

// TestBridgeLeavesOrdinaryCallsAndContentAlone checks passthrough fidelity for
// non-execute traffic.
func TestBridgeLeavesOrdinaryCallsAndContentAlone(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewOpenCodeExecuteBridgeWriter(rec)
	ordinaryFrag := map[string]any{"index": 0, "id": "c", "function": map[string]any{"name": "get_weather", "arguments": "{\"city\":\"X\"}"}}
	in := chunkLine(t, choiceWrap(callDelta(ordinaryFrag))) +
		chunkLine(t, choiceWrap(map[string]any{"index": 0, "delta": map[string]any{"content": "hi"}})) +
		"data: [DONE]\n\n"
	if _, err := w.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != in {
		t.Fatalf("passthrough altered the stream:\n got: %s\nwant: %s", rec.Body.String(), in)
	}
}

// TestBridgeReplaysNonConventionExecute verifies a genuine Code Mode execute
// call (code is not CALL_TOOL) reaches the client verbatim.
func TestBridgeReplaysNonConventionExecute(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewOpenCodeExecuteBridgeWriter(rec)
	frag := map[string]any{"index": 0, "id": "c", "function": map[string]any{"name": "execute", "arguments": "{\"code\":\"print(1)\"}"}}
	in := chunkLine(t, choiceWrap(callDelta(frag))) + "data: [DONE]\n\n"
	if _, err := w.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), `"name":"execute"`) || !strings.Contains(rec.Body.String(), "print(1)") {
		t.Fatalf("non-convention execute altered: %s", rec.Body.String())
	}
}

// TestBridgeJSON rewrites the non-streaming shape.
func TestBridgeJSON(t *testing.T) {
	in := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"execute","arguments":"{\"code\":\"CALL_TOOL get_weather {\\\"city\\\":\\\"Jakarta\\\"}\"}"}}]}}]}`)
	out := bridgeOpenCodeExecuteToolCallsJSON(in)
	var obj struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	call := obj.Choices[0].Message.ToolCalls[0].Function
	if call.Name != "get_weather" || call.Arguments != `{"city":"Jakarta"}` {
		t.Fatalf("rewrite wrong: %+v", call)
	}
}
