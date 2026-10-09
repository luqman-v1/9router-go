package translator

import (
	json "encoding/json/v2"
	"testing"
)

// A tool_call id is opaque: Gemini 3.x supplies its own (functionCall.id), and
// even a generated id carries no name a parser can rely on. The tool a result
// answers must come from what this gateway recorded when it emitted the call,
// never from reading the id. Before that, an unknown id was "recovered" by
// stripping call_<name>_<n>, which mangles an opaque token into a name that
// looks like a tool.
func TestGeminiOpaqueToolCallIDRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		geminiID   string
		wantTool   string
		wantHasIdx bool // generated ids end in _<index>
	}{
		{name: "gemini supplied id is used verbatim", geminiID: "call_abc123", wantTool: "read_file"},
		{name: "absent id falls back to a generated one", geminiID: "", wantTool: "read_file", wantHasIdx: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ClearGeminiToolCallNames()

			state := &GeminiStreamState{MessageId: "msg", Model: "gemini-test"}
			part := `{"functionCall":{"name":"read_file","args":{"path":"a.go"}`
			if tt.geminiID != "" {
				part += `,"id":"` + tt.geminiID + `"`
			}
			part += `}}`
			chunk := `{"candidates":[{"content":{"parts":[` + part + `]},"finishReason":"STOP"}]}`

			id := streamedToolCallID(t, chunk, state)

			if tt.geminiID != "" && id != tt.geminiID {
				t.Fatalf("id = %q, want the id Gemini sent (%q)", id, tt.geminiID)
			}
			if tt.geminiID == "" && !endsWithIndex(id, state.ToolCallCount-1) {
				t.Errorf("generated id %q must end in the call index %d", id, state.ToolCallCount-1)
			}

			// The client echoes only the result, as many do.
			body := `{"model":"gemini-test","messages":[
				{"role":"tool","tool_call_id":"` + id + `","content":"contents of a.go"}
			]}`
			out, err := TranslateOpenAIToGemini([]byte(body))
			if err != nil {
				t.Fatalf("TranslateOpenAIToGemini: %v", err)
			}

			resp := firstFunctionResponse(t, out)
			if resp.Name != tt.wantTool {
				t.Errorf("functionResponse name = %q, want %q (id %q)", resp.Name, tt.wantTool, id)
			}
			if resp.ID != id {
				t.Errorf("functionResponse id = %q, want %q", resp.ID, id)
			}
		})
	}
}

// Two calls in one chunk must stay distinguishable: they are emitted inside a
// single clock tick, so a UnixNano-only id gives both the same value and the
// client can no longer tell which result belongs to which call.
func TestGeminiParallelToolCallIDsAreDistinct(t *testing.T) {
	ClearGeminiToolCallNames()

	state := &GeminiStreamState{MessageId: "msg", Model: "gemini-test"}
	chunk := `{"candidates":[{"content":{"parts":[
		{"functionCall":{"name":"read_file","args":{"path":"a.go"}}},
		{"functionCall":{"name":"write_file","args":{"path":"b.go"}}},
		{"functionCall":{"name":"read_file","args":{"path":"c.go"}}}
	]},"finishReason":"STOP"}]}`

	ids := streamedToolCallIDs(t, chunk, state)
	if len(ids) != 3 {
		t.Fatalf("want 3 tool_call ids, got %d: %v", len(ids), ids)
	}

	seen := map[string]int{}
	for i, id := range ids {
		if j, dup := seen[id]; dup {
			t.Errorf("ids[%d] and ids[%d] are both %q — parallel calls must not share an id", j, i, id)
		}
		seen[id] = i
	}

	// Each name still answers its own id.
	want := []string{"read_file", "write_file", "read_file"}
	for i, id := range ids {
		if got := GetGeminiToolCallName(id, "msg"); got != want[i] {
			t.Errorf("name for %q = %q, want %q", id, got, want[i])
		}
	}
}

// The store must resolve an id through the "__ts__" transport suffix and the
// session namespace, the same way the thought signature store does, or an
// Antigravity thinking turn cannot recover the tool it minted.
func TestGeminiToolCallNameStoreResolvesTransportSuffix(t *testing.T) {
	ClearGeminiToolCallNames()

	StoreGeminiToolCallName("call_read_file_1_0", "read_file", "msg")

	if got := GetGeminiToolCallName("call_read_file_1_0__ts__SIG", "msg"); got != "read_file" {
		t.Errorf("name via __ts__ suffix = %q, want read_file", got)
	}
	if got := GetGeminiToolCallName("call_read_file_1_0", ""); got != "read_file" {
		t.Errorf("name via global lookup = %q, want read_file", got)
	}
	if got := GetGeminiToolCallName("call_unknown_2_0", "msg"); got != "" {
		t.Errorf("unknown id = %q, want empty", got)
	}
}

// A result for an id this gateway never minted must not be given an invented
// tool name. Gemini matches responses by name, so a wrong name is a silent
// mis-pair; the id is the honest answer.
func TestGeminiUnknownToolCallIDIsNotParsedIntoAName(t *testing.T) {
	ClearGeminiToolCallNames()

	body := `{"model":"gemini-test","messages":[
		{"role":"tool","tool_call_id":"call_read_file_1791515128_0","content":"x"}
	]}`
	out, err := TranslateOpenAIToGemini([]byte(body))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	if got := firstFunctionResponse(t, out).Name; got != "call_read_file_1791515128_0" {
		t.Errorf("name = %q, want the id verbatim — an unknown id has no tool name", got)
	}
}

// ─── helpers ───

func streamedToolCallIDs(t *testing.T, chunk string, state *GeminiStreamState) []string {
	t.Helper()
	var ids []string
	for _, chunkJSON := range decodeSSEChunks(t, chunk, state) {
		choices, _ := chunkJSON["choices"].([]any)
		for _, c := range choices {
			choice, _ := c.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, tc := range calls {
				call, _ := tc.(map[string]any)
				id, _ := call["id"].(string)
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func streamedToolCallID(t *testing.T, chunk string, state *GeminiStreamState) string {
	t.Helper()
	ids := streamedToolCallIDs(t, chunk, state)
	if len(ids) != 1 {
		t.Fatalf("want 1 tool_call id, got %d", len(ids))
	}
	return ids[0]
}

func decodeSSEChunks(t *testing.T, chunk string, state *GeminiStreamState) []map[string]any {
	t.Helper()
	out, err := TranslateGeminiChunkToOpenAI([]byte(chunk), state)
	if err != nil {
		t.Fatalf("TranslateGeminiChunkToOpenAI: %v", err)
	}
	var chunks []map[string]any
	for _, line := range decodeOpenAISSE(t, out) {
		chunks = append(chunks, line)
	}
	return chunks
}

func endsWithIndex(id string, index int) bool {
	want := "_" + itoa(index)
	return len(id) > len(want) && id[len(id)-len(want):] == want
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func firstFunctionResponse(t *testing.T, geminiBody []byte) GeminiFunctionResp {
	t.Helper()
	var req GeminiRequest
	if err := json.Unmarshal(geminiBody, &req); err != nil {
		t.Fatalf("parse translated request: %v", err)
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				return *p.FunctionResponse
			}
		}
	}
	t.Fatalf("no functionResponse part in %s", string(geminiBody))
	return GeminiFunctionResp{}
}
