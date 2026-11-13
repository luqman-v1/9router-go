package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// A Gemini SDK posts contents[]/systemInstruction/generationConfig/tools. The
// gateway speaks Chat internally, so the request has to become one.
func TestGeminiClientToChatRequest(t *testing.T) {
	body := []byte(`{
		"systemInstruction": {"parts": [{"text": "be terse"}]},
		"contents": [
			{"role": "user", "parts": [{"text": "hello"}]},
			{"role": "model", "parts": [{"text": "hi there"}]},
			{"role": "user", "parts": [{"text": "and again"}]}
		],
		"generationConfig": {"maxOutputTokens": 128, "temperature": 0.4, "topP": 0.9}
	}`)
	out, err := GeminiClientToChatRequest(body, "deepseek/deepseek-chat", false)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var chat map[string]any
	if err := json.Unmarshal(out, &chat); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if chat["model"] != "deepseek/deepseek-chat" {
		t.Errorf("model = %v", chat["model"])
	}
	if chat["stream"] != false {
		t.Errorf("stream = %v, want false", chat["stream"])
	}
	msgs, _ := chat["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4 (system plus three turns)", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be terse" {
		t.Errorf("first message = %v", first)
	}
	// A Gemini `model` turn is an assistant turn in Chat.
	if role := msgs[2].(map[string]any)["role"]; role != "assistant" {
		t.Errorf("model turn became role %v, want assistant", role)
	}
	if chat["max_tokens"] != float64(128) {
		t.Errorf("max_tokens = %v, want 128", chat["max_tokens"])
	}
}

// Gemini has no native call id, so a call and its response derive the same one
// from the name. A pair that did not line up would be rejected upstream.
// A parallel-tool turn carries every functionResponse in one content block
// (upstream ef2af4def): all of them must survive as separate role:"tool"
// messages, ordered before any co-located follow-up text or calls, or the
// unanswered tool_call ids get the request rejected upstream.
func TestGeminiClientPreservesAllToolResults(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []map[string]any // role + optional tool_call_id/content/tool_calls, in order
	}{
		{
			name: "two parallel tool results become two tool messages",
			body: `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_w","response":{"result":"sunny"}}},{"functionResponse":{"name":"get_time","id":"call_t","response":{"result":"noon"}}}]}]}`,
			want: []map[string]any{
				{"role": "tool", "tool_call_id": "call_w", "content": `"sunny"`},
				{"role": "tool", "tool_call_id": "call_t", "content": `"noon"`},
			},
		},
		{
			name: "results without ids derive call_ from the name",
			body: `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"result":"sunny"}}}]}]}`,
			want: []map[string]any{
				{"role": "tool", "tool_call_id": "call_get_weather", "content": `"sunny"`},
			},
		},
		{
			name: "results precede co-located follow-up text",
			body: `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_w","response":{"result":"sunny"}}},{"text":"now plan my day"}]}]}`,
			want: []map[string]any{
				{"role": "tool", "tool_call_id": "call_w", "content": `"sunny"`},
				{"role": "user", "content": "now plan my day"},
			},
		},
		{
			name: "results precede co-located follow-up calls",
			body: `{"contents":[{"role":"model","parts":[{"functionResponse":{"name":"get_weather","id":"call_w","response":{"result":"sunny"}}},{"functionCall":{"name":"get_time","args":{}}}]}]}`,
			want: []map[string]any{
				{"role": "tool", "tool_call_id": "call_w", "content": `"sunny"`},
				{"role": "assistant", "tool_calls": "present"},
			},
		},
		{
			name: "object result stringifies as the object itself",
			body: `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"read_file","id":"call_f","response":{"result":{"path":"/x","size":12}}}}]}]}`,
			want: []map[string]any{
				{"role": "tool", "tool_call_id": "call_f", "content": `{"path":"/x","size":12}`},
			},
		},
		{
			name: "response without result falls back to the whole object",
			body: `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_w","response":{"report":"rain"}}}]}]}`,
			want: []map[string]any{
				{"role": "tool", "tool_call_id": "call_w", "content": `{"report":"rain"}`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := GeminiClientToChatRequest([]byte(tt.body), "m", false)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			var chat map[string]any
			if err := json.Unmarshal(out, &chat); err != nil {
				t.Fatalf("parse: %v", err)
			}
			msgs, _ := chat["messages"].([]any)
			if len(msgs) != len(tt.want) {
				t.Fatalf("messages = %d, want %d: %s", len(msgs), len(tt.want), string(out))
			}
			for i, want := range tt.want {
				got, _ := msgs[i].(map[string]any)
				if got["role"] != want["role"] {
					t.Errorf("msg[%d] role = %v, want %v", i, got["role"], want["role"])
				}
				if id, ok := want["tool_call_id"]; ok && got["tool_call_id"] != id {
					t.Errorf("msg[%d] tool_call_id = %v, want %v", i, got["tool_call_id"], id)
				}
				if content, ok := want["content"]; ok {
					// Compare structurally: a map round-trips through the
					// decoder, and key order inside a marshalled object is
					// not part of the contract.
					if !jsonEqual(want["content"], got["content"]) {
						t.Errorf("msg[%d] content = %v, want %v", i, got["content"], content)
					}
				}
				if _, ok := want["tool_calls"]; ok {
					if _, present := got["tool_calls"]; !present {
						t.Errorf("msg[%d] missing tool_calls", i)
					}
				}
			}
		})
	}
}

// A response missing its result key degrades to an empty JSON object, matching
// upstream's `|| {}` fallback, rather than marshalling a null.
func TestGeminiClientToolResultWithoutResponse(t *testing.T) {
	out, err := GeminiClientToChatRequest([]byte(
		`{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_w"}}]}]}`), "m", false)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(string(out), `"content":"{}"`) {
		t.Errorf("content = %s, want empty JSON object", string(out))
	}
}

func TestGeminiClientToolCallAndResponsePair(t *testing.T) {
	body := []byte(`{"contents":[
		{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"/x"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"read_file","response":{"result":"ok"}}}]}
	]}`)
	out, err := GeminiClientToChatRequest(body, "m", false)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var chat map[string]any
	json.Unmarshal(out, &chat)
	msgs := chat["messages"].([]any)

	assistant := msgs[0].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %d", len(calls))
	}
	callID := calls[0].(map[string]any)["id"].(string)
	if callID != "call_read_file" {
		t.Errorf("call id = %q, want call_read_file", callID)
	}

	// The functionResponse is its own turn, paired by id.
	toolMsg := msgs[1].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Errorf("response turn role = %v, want tool", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != callID {
		t.Errorf("tool_call_id = %v, want %q", toolMsg["tool_call_id"], callID)
	}
}

// An inlineData part becomes an image_url block, because the Chat pipeline's
// vision path reads that shape.
func TestGeminiClientInlineDataBecomesAnImageBlock(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[
		{"text":"what is this"},
		{"inlineData":{"mimeType":"image/png","data":"AAAA"}}
	]}]}`)
	out, err := GeminiClientToChatRequest(body, "m", false)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(string(out), "data:image/png;base64,AAAA") {
		t.Errorf("image block missing: %s", out)
	}
	if !strings.Contains(string(out), "image_url") {
		t.Errorf("image_url block missing: %s", out)
	}
}

// A tool declaration keeps its schema; dropping it would send the model a tool
// with no parameters.
func TestGeminiClientToolDeclarationKeepsItsSchema(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"tools":[{"functionDeclarations":[{"name":"read_file","description":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}]}`)
	out, err := GeminiClientToChatRequest(body, "m", false)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(string(out), `"path"`) {
		t.Errorf("parameter schema was dropped: %s", out)
	}
	if !strings.Contains(string(out), `"read_file"`) {
		t.Errorf("tool name missing: %s", out)
	}
}

// The answer has to come back in the SDK's shape: candidates[].content.parts[].
func TestChatResponseToGemini(t *testing.T) {
	chat := []byte(`{"id":"c1","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	out, err := ChatResponseToGemini(chat, "deepseek/deepseek-chat")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cands, _ := resp["candidates"].([]any)
	if len(cands) != 1 {
		t.Fatalf("candidates = %d", len(cands))
	}
	cand := cands[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason = %v", cand["finishReason"])
	}
	content := cand["content"].(map[string]any)
	parts := content["parts"].([]any)
	if got := parts[0].(map[string]any)["text"]; got != "Hello there" {
		t.Errorf("text = %v", got)
	}
	usage, _ := resp["usageMetadata"].(map[string]any)
	if usage["totalTokenCount"] != float64(8) {
		t.Errorf("totalTokenCount = %v", usage["totalTokenCount"])
	}
}

// `length` is MAX_TOKENS in Gemini's vocabulary, and a client switches on it.
func TestChatResponseToGeminiMapsFinishReasons(t *testing.T) {
	cases := map[string]string{
		"stop":           "STOP",
		"length":         "MAX_TOKENS",
		"tool_calls":     "STOP",
		"content_filter": "SAFETY",
	}
	for in, want := range cases {
		chat := []byte(`{"choices":[{"message":{"content":"x"},"finish_reason":"` + in + `"}]}`)
		out, err := ChatResponseToGemini(chat, "m")
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		var resp map[string]any
		json.Unmarshal(out, &resp)
		got := resp["candidates"].([]any)[0].(map[string]any)["finishReason"]
		if got != want {
			t.Errorf("finish %q mapped to %v, want %q", in, got, want)
		}
	}
}

// A streaming chunk that carries only a role announcement has nothing a Gemini
// client can display, and must not be emitted.
func TestChatChunkToGeminiDropsEmptyDeltas(t *testing.T) {
	roleOnly := []byte(`{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
	if _, ok := ChatChunkToGemini(roleOnly, "m"); ok {
		t.Error("a role-only delta was emitted")
	}

	content := []byte(`{"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`)
	frame, ok := ChatChunkToGemini(content, "m")
	if !ok {
		t.Fatal("a content delta was dropped")
	}
	if !strings.Contains(string(frame), `"Hi"`) {
		t.Errorf("frame lost the text: %s", frame)
	}

	// Reasoning arrives as a thought part, which the SDK renders separately.
	reasoning := []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}`)
	frame, ok = ChatChunkToGemini(reasoning, "m")
	if !ok {
		t.Fatal("a reasoning delta was dropped")
	}
	if !strings.Contains(string(frame), `"thought":true`) {
		t.Errorf("frame does not mark the part as a thought: %s", frame)
	}
}

// The final chunk carries the finish reason and the usage block; a client reads
// both from the same frame.
func TestChatChunkToGeminiFinalChunkCarriesUsage(t *testing.T) {
	final := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	frame, ok := ChatChunkToGemini(final, "m")
	if !ok {
		t.Fatal("the final chunk was dropped")
	}
	var parsed map[string]any
	if err := json.Unmarshal(frame, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cand := parsed["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason = %v", cand["finishReason"])
	}
	usage, _ := parsed["usageMetadata"].(map[string]any)
	if usage["totalTokenCount"] != float64(8) {
		t.Errorf("usageMetadata missing or wrong: %v", parsed["usageMetadata"])
	}
}

// The tool-result payload is what the provider parses, so it must be
// byte-stable: json/v2 re-marshals decoded maps in unstable member order, and
// a payload that shifts between runs would make provider-side caching and
// fixture comparisons non-deterministic. The converter therefore re-emits the
// verbatim result bytes the client sent (upstream's JSON.stringify also
// preserves member order).
func TestGeminiClientToolResultPayloadByteStable(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"read_file","id":"call_f","response":{"result":{"path":"/x","size":12,"alpha":1,"beta":2,"gamma":3}}}}]}]}`)
	seen := map[string]bool{}
	for range 300 {
		out, err := GeminiClientToChatRequest(body, "m", false)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		var chat struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(out, &chat); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(chat.Messages) != 1 {
			t.Fatalf("messages = %d, want 1", len(chat.Messages))
		}
		seen[chat.Messages[0].Content] = true
	}
	if len(seen) != 1 {
		for s := range seen {
			t.Errorf("distinct payload: %s", s)
		}
		t.Fatalf("payload unstable: %d distinct bodies across 300 runs", len(seen))
	}
}
