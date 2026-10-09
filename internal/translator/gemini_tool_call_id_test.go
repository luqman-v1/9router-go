package translator

import (
	"encoding/json"
	"strconv"
	"strings"
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
		name     string
		geminiID string
	}{
		{name: "gemini supplied id is used verbatim", geminiID: "call_abc123"},
		{name: "absent id falls back to a generated one", geminiID: ""},
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

			if tt.geminiID != "" {
				if id != tt.geminiID {
					t.Fatalf("id = %q, want the id Gemini sent (%q)", id, tt.geminiID)
				}
			} else if !strings.HasSuffix(id, "_"+strconv.Itoa(0)) {
				// The first call in a stream is index 0, whatever the counter
				// has been incremented to by the time the assertion runs.
				t.Errorf("generated id %q must end in the first call's index _0", id)
			}

			// The client echoes only the result, as many do.
			resp := firstFunctionResponse(t, translateToolOnlyResult(t, id))
			if resp.Name != "read_file" {
				t.Errorf("functionResponse name = %q, want read_file (id %q)", resp.Name, id)
			}
			if resp.ID != id {
				t.Errorf("functionResponse id = %q, want %q", resp.ID, id)
			}
		})
	}
}

// The non-stream translator mints ids too, and the reverse direction has to
// resolve them: a client that answers a streamed-then-buffered turn echoes only
// role:"tool", so without this the fallback degrades to using the id as the
// tool name — the exact defect the store exists to prevent.
func TestGeminiNonStreamToolCallIDRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		geminiID string
	}{
		{name: "gemini supplied id", geminiID: "call_abc123"},
		{name: "generated id", geminiID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ClearGeminiToolCallNames()

			part := `{"functionCall":{"name":"read_file","args":{"path":"a.go"}`
			if tt.geminiID != "" {
				part += `,"id":"` + tt.geminiID + `"`
			}
			part += `}}`
			body := `{"candidates":[{"content":{"parts":[` + part + `]},"finishReason":"STOP"}]}`

			out, _, err := TranslateGeminiResponseToOpenAI([]byte(body))
			if err != nil {
				t.Fatalf("TranslateGeminiResponseToOpenAI: %v", err)
			}
			id := firstToolCallID(t, out)
			if tt.geminiID != "" && id != tt.geminiID {
				t.Errorf("id = %q, want %q", id, tt.geminiID)
			}

			resp := firstFunctionResponse(t, translateToolOnlyResult(t, id))
			if resp.Name != "read_file" {
				t.Errorf("functionResponse name = %q, want read_file (id %q)", resp.Name, id)
			}
			if resp.ID != id {
				t.Errorf("functionResponse id = %q, want %q", resp.ID, id)
			}
		})
	}
}

// An id leaving the gateway carries a "__ts__<sig>" suffix whenever the call had
// a thought signature, and that is the id the client echoes back. The name must
// still resolve through the suffix — this is the Antigravity thinking turn, and
// the one path where a session-scoped store entry is the only record of the
// tool that was called.
func TestGeminiToolCallNameResolvesThroughThoughtSignatureSuffix(t *testing.T) {
	ClearGeminiToolCallNames()

	state := &GeminiStreamState{MessageId: "msg", Model: "gemini-test"}
	chunk := `{"candidates":[{"content":{"parts":[
		{"thoughtSignature":"SIG123","functionCall":{"name":"read_file","args":{"path":"a.go"}}}
	]},"finishReason":"STOP"}]}`

	id := streamedToolCallID(t, chunk, state)
	if !strings.Contains(id, "__ts__") {
		t.Fatalf("id %q should carry a __ts__ transport suffix for a signed call", id)
	}

	resp := firstFunctionResponse(t, translateToolOnlyResult(t, id))
	if resp.Name != "read_file" {
		t.Errorf("functionResponse name = %q, want read_file (suffixed id %q)", resp.Name, id)
	}
	// The suffix is a 9router-go transport encoding and must not reach Gemini,
	// so the response carries the id with it stripped — the same id the client
	// sees on the functionCall half of the pair.
	if resp.ID != geminiCleanToolCallID(id) {
		t.Errorf("functionResponse id = %q, want %q (the __ts__ suffix stripped)", resp.ID, geminiCleanToolCallID(id))
	}
}

// The assistant turn in the request is the strongest evidence of which tool a
// result answers, so it outranks this gateway's own record of the id. Swapping
// the two would answer a result from process-wide state instead of from the turn
// the client actually sent, which matters when a client replays an id against a
// different tool.
func TestGeminiToolResultNamePrefersTheRequestOverTheStore(t *testing.T) {
	ClearGeminiToolCallNames()

	// The gateway once minted this id for read_file.
	StoreGeminiToolCallName("call_abc123", "read_file", "")

	// The client's own turn says the id is write_file now.
	body := `{"model":"gemini-test","messages":[
		{"role":"assistant","content":"","tool_calls":[
			{"id":"call_abc123","type":"function","function":{"name":"write_file","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"call_abc123","content":"written"}
	]}`
	out, err := TranslateOpenAIToGemini([]byte(body))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	if got := firstFunctionResponse(t, out).Name; got != "write_file" {
		t.Errorf("functionResponse name = %q, want write_file — the request outranks the stored name", got)
	}
}

// An id this gateway never minted, arriving while the request does carry
// tool_calls, must fall past the request pairing and the store to the id itself.
// The tool-name parsing that used to sit here turned call_nope into "nope",
// inventing a tool that was never declared; Gemini matches a response by name,
// so that is a silent mis-pair rather than an honest unknown.
func TestGeminiUnknownToolCallIDFallsThroughToTheID(t *testing.T) {
	ClearGeminiToolCallNames()

	body := `{"model":"gemini-test","messages":[
		{"role":"assistant","content":"","tool_calls":[
			{"id":"call_9_alpha","type":"function","function":{"name":"alpha","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"call_nope","content":"1"}
	]}`
	out, err := TranslateOpenAIToGemini([]byte(body))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	if got := firstFunctionResponse(t, out).Name; got != "call_nope" {
		t.Errorf("functionResponse name = %q, want the id verbatim — an unknown id has no tool name", got)
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

	// Each result must still reach its own tool.
	want := []string{"read_file", "write_file", "read_file"}
	for i, id := range ids {
		if got := firstFunctionResponse(t, translateToolOnlyResult(t, id)).Name; got != want[i] {
			t.Errorf("result for %q named %q, want %q", id, got, want[i])
		}
	}
}

// Expiring an entry must not leave its key in the FIFO slice. A key that is
// pruned from entries and later reused is appended again, so a rebuild gated
// only on capacity lets order grow without bound while traffic stays under the
// cap — the regime in which the trim never runs and the leak is invisible.
func TestGeminiToolCallNameStoreOrderNeverExceedsEntries(t *testing.T) {
	ClearGeminiToolCallNames()

	for i := range 50 {
		StoreGeminiToolCallName("call_"+strconv.Itoa(i), "read_file", "msg")
	}

	// Expire everything, then write the same ids again.
	globalToolNameStore.mu.Lock()
	for k, v := range globalToolNameStore.entries {
		v.expiresAt = v.expiresAt.Add(-2 * memoryTTL)
		globalToolNameStore.entries[k] = v
	}
	globalToolNameStore.mu.Unlock()

	for i := range 50 {
		StoreGeminiToolCallName("call_"+strconv.Itoa(i), "read_file", "msg")
	}

	globalToolNameStore.mu.RLock()
	entries, order := len(globalToolNameStore.entries), len(globalToolNameStore.order)
	globalToolNameStore.mu.RUnlock()

	if order > entries {
		t.Errorf("order holds %d keys for %d entries: expired keys leaked into the FIFO slice", order, entries)
	}
}

// ─── helpers ───

// translateToolOnlyResult sends the shape a client produces when it answers a
// tool call: the result alone, with no assistant turn repeating the call.
func translateToolOnlyResult(t *testing.T, toolCallID string) []byte {
	t.Helper()
	body := `{"model":"gemini-test","messages":[
		{"role":"tool","tool_call_id":"` + toolCallID + `","content":"result"}
	]}`
	out, err := TranslateOpenAIToGemini([]byte(body))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	return out
}

func streamedToolCallIDs(t *testing.T, chunk string, state *GeminiStreamState) []string {
	t.Helper()
	out, err := TranslateGeminiChunkToOpenAI([]byte(chunk), state)
	if err != nil {
		t.Fatalf("TranslateGeminiChunkToOpenAI: %v", err)
	}
	var ids []string
	for _, chunkJSON := range decodeOpenAISSE(t, out) {
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
		t.Fatalf("want 1 tool_call id, got %d: %v", len(ids), ids)
	}
	return ids[0]
}

func firstToolCallID(t *testing.T, openaiResponse []byte) string {
	t.Helper()
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct{ ID string } `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(openaiResponse, &resp); err != nil {
		t.Fatalf("parse translated response: %v", err)
	}
	if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("no tool_calls in translated response: %s", openaiResponse)
	}
	return resp.Choices[0].Message.ToolCalls[0].ID
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
