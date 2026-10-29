package executor

import (
	json "encoding/json/v2"
	"reflect"
	"testing"
)

// commandcodeParamsForTest runs buildCommandcodeBody and returns payload["params"]
// as a map. It fails the test when the wrapper or params object is missing.
func commandcodeParamsForTest(t *testing.T, body string) map[string]any {
	t.Helper()
	out, err := buildCommandcodeBody([]byte(body), "")
	if err != nil {
		t.Fatalf("buildCommandcodeBody() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	params, ok := payload["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected params object, got %T", payload["params"])
	}
	return params
}

func TestCommandcodeConvertMessages(t *testing.T) {
	tests := []struct {
		name         string
		messages     string
		wantMessages []any
		wantSystem   string
	}{
		{
			name: "tool result becomes a role:tool tool-result block",
			messages: `[
				{"role":"user","content":"weather?"},
				{"role":"assistant","tool_calls":[
					{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Nairobi\"}"}}
				]},
				{"role":"tool","tool_call_id":"call_1","name":"get_weather","content":"24C sunny"}
			]`,
			wantMessages: []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "weather?"},
				}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "reasoning", "text": " "},
					map[string]any{"type": "tool-call", "toolCallId": "call_1", "toolName": "get_weather", "input": map[string]any{"city": "Nairobi"}},
				}},
				map[string]any{"role": "tool", "content": []any{
					map[string]any{"type": "tool-result", "toolCallId": "call_1", "toolName": "get_weather", "output": map[string]any{"type": "text", "value": "24C sunny"}},
				}},
			},
		},
		{
			name: "consecutive tool turns keep role tool and string ids",
			messages: `[
				{"role":"user","content":"go"},
				{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"ls","arguments":"{\"path\":\".\"}"}}]},
				{"role":"tool","tool_call_id":"call_1","name":"ls","content":"a.txt"},
				{"role":"tool","content":"{\"output\":\"README.md\"}"}
			]`,
			wantMessages: []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "go"},
				}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "reasoning", "text": " "},
					map[string]any{"type": "tool-call", "toolCallId": "call_1", "toolName": "ls", "input": map[string]any{"path": "."}},
				}},
				map[string]any{"role": "tool", "content": []any{
					map[string]any{"type": "tool-result", "toolCallId": "call_1", "toolName": "ls", "output": map[string]any{"type": "text", "value": "a.txt"}},
				}},
				map[string]any{"role": "tool", "content": []any{
					map[string]any{"type": "tool-result", "toolCallId": "", "toolName": "", "output": map[string]any{"type": "text", "value": "{\"output\":\"README.md\"}"}},
				}},
			},
		},
		{
			name: "assistant reasoning text wins over placeholder",
			messages: `[
				{"role":"user","content":"hi"},
				{"role":"assistant","reasoning_content":"thinking","content":"answer"}
			]`,
			wantMessages: []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "hi"},
				}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "reasoning", "text": "thinking"},
					map[string]any{"type": "text", "text": "answer"},
				}},
			},
		},
		{
			name: "system content array flattens to one string",
			messages: `[
				{"role":"system","content":[{"type":"text","text":"be terse"},{"type":"text","text":"no filler"}]},
				{"role":"system","content":"second block"},
				{"role":"user","content":"go"}
			]`,
			wantSystem: "be terse\nno filler\n\nsecond block",
			wantMessages: []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "go"},
				}},
			},
		},
		{
			name:     "user nil content becomes empty text block",
			messages: `[{"role":"user","content":null}]`,
			wantMessages: []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": ""},
				}},
			},
		},
		{
			name:     "unknown role routes to user",
			messages: `[{"role":"nobody","content":"orphan"}]`,
			wantMessages: []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "orphan"},
				}},
			},
		},
		{
			name:     "empty assistant emits one empty text block",
			messages: `[{"role":"assistant","content":null}]`,
			wantMessages: []any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": ""},
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := commandcodeParamsForTest(t, `{"model":"z-ai/glm-5.3-flash","messages":`+tt.messages+`}`)
			// marshalStable, not json.Marshal: default json/v2 randomizes map
			// member order per call, so a byte comparison would flake.
			got, _ := marshalStable(params["messages"])
			want, _ := marshalStable(tt.wantMessages)
			if string(got) != string(want) {
				t.Errorf("messages mismatch\n got: %s\nwant: %s", got, want)
			}
			if system, _ := params["system"].(string); system != tt.wantSystem {
				t.Errorf("system = %q, want %q", system, tt.wantSystem)
			}
		})
	}
}

// TestCommandcodeNullFieldsAreAbsent pins the nullish semantics upstream's
// openaiToCommandCodeRequest has: a null value is an absent value, so the
// default applies and no key ever carries a JSON null.
func TestCommandcodeNullFieldsAreAbsent(t *testing.T) {
	params := commandcodeParamsForTest(t, `{"model":"m","messages":[],"max_tokens":null,"temperature":null,"top_p":null,"reasoning_effort":null}`)
	if got := params["max_tokens"]; got != float64(1024) {
		t.Errorf("max_tokens = %v, want the 1024 default", got)
	}
	if got := params["temperature"]; got != 0.3 {
		t.Errorf("temperature = %v, want the 0.3 default", got)
	}
	if _, has := params["top_p"]; has {
		t.Errorf("top_p must be absent, got %v", params["top_p"])
	}
	if _, has := params["reasoning_effort"]; has {
		t.Errorf("reasoning_effort must be absent, got %v", params["reasoning_effort"])
	}
}

// TestCommandcodeDeveloperRoleFeedsTheSystemPrompt pins the deliberate local
// divergence: upstream downgrades a developer turn to a user turn
// (open-sse/translator/request/openai-to-commandcode.js:106-160), while this
// port feeds the system prompt.
func TestCommandcodeDeveloperRoleFeedsTheSystemPrompt(t *testing.T) {
	params := commandcodeParamsForTest(t, `{"model":"m","messages":[{"role":"developer","content":"be terse"}]}`)
	if got := params["system"]; got != "be terse" {
		t.Errorf("system = %v, want the developer turn in the system prompt", got)
	}
}

func TestBuildCommandcodeBody_ConvertsToolsToAnthropicShape(t *testing.T) {
	params := commandcodeParamsForTest(t, `{
		"model":"z-ai/glm-5.3-flash",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"type":"function",
			"function":{"name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}
		}]
	}`)
	tools, ok := params["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d (%v)", len(tools), params["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if _, stillOpenAI := tool["type"]; stillOpenAI {
		t.Errorf("tool still OpenAI shaped: %v", tool)
	}
	if tool["name"] != "get_weather" {
		t.Errorf("tool name = %v, want get_weather", tool["name"])
	}
	if !reflect.DeepEqual(tool["input_schema"], map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}) {
		t.Errorf("input_schema = %v, want the request parameters object", tool["input_schema"])
	}
}

func TestBuildCommandcodeBody_ParamsWhitelist(t *testing.T) {
	params := commandcodeParamsForTest(t, `{
		"model":"z-ai/glm-5.3-flash",
		"messages":[{"role":"user","content":"hi"}],
		"stream_options":{"include_usage":true},
		"store":true,
		"metadata":{"k":"v"},
		"tool_choice":"auto",
		"max_output_tokens":2048,
		"reasoning_effort":"medium",
		"top_p":0.9
	}`)
	for _, leak := range []string{"stream_options", "store", "metadata", "tool_choice", "max_output_tokens"} {
		if v, ok := params[leak]; ok {
			t.Errorf("params leaked OpenAI-only key %q = %v", leak, v)
		}
	}
	if _, ok := params["reasoning_effort"]; !ok {
		t.Error("params.reasoning_effort missing — the thinking pipeline depends on it")
	}
	if params["top_p"] != 0.9 {
		t.Error("params.top_p missing")
	}
	if params["max_tokens"] != 2048.0 {
		t.Errorf("params.max_tokens = %v, want 2048 from max_output_tokens", params["max_tokens"])
	}
	if params["temperature"] != 0.3 {
		t.Errorf("params.temperature = %v, want 0.3 default", params["temperature"])
	}
	if params["stream"] != true {
		t.Error("params.stream missing")
	}
}

func TestBuildCommandcodeBody_NoToolsKeyWhenAbsent(t *testing.T) {
	params := commandcodeParamsForTest(t, `{"model":"z-ai/glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)
	if v, ok := params["tools"]; ok {
		t.Errorf("params.tools must be absent when the request has no tools, got %v", v)
	}
}

// TestBuildCommandcodeBody_WhitelistsAPreWrappedParams pins the native
// envelope: a client that sends {params:{...}} gets the same treatment as an
// OpenAI body. Without the whitelist the client could set the model the
// provider is called with, and pass any parameter the provider accepts — on
// the operator's account.
func TestBuildCommandcodeBody_WhitelistsAPreWrappedParams(t *testing.T) {
	out, err := buildCommandcodeBody([]byte(`{
		"model":"requested",
		"params":{
			"model":"attacker-model",
			"max_tokens":4096,
			"system":"leak",
			"extra":"not-a-param",
			"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
		}
	}`), "resolved-model")
	if err != nil {
		t.Fatalf("buildCommandcodeBody() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	params, ok := payload["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected params object, got %T", payload["params"])
	}
	if params["model"] != "resolved-model" {
		t.Errorf("params.model = %v, want the resolved model", params["model"])
	}
	if _, has := params["extra"]; has {
		t.Errorf("params carried a key outside the whitelist: %v", params)
	}
	if params["max_tokens"] != float64(4096) {
		t.Errorf("params.max_tokens = %v, want the client's own budget", params["max_tokens"])
	}
	if params["system"] != "leak" {
		t.Errorf("params.system = %v, want the client's system prompt", params["system"])
	}
	if _, has := params["messages"]; !has {
		t.Errorf("params.messages must survive the whitelist: %v", params)
	}
}

// TestBuildCommandcodeBody_RejectsAPreWrappedParamsThatIsNotAnObject pins the
// shape: a params value that is not an object is a client error, not a body
// to forward.
func TestBuildCommandcodeBody_RejectsAPreWrappedParamsThatIsNotAnObject(t *testing.T) {
	if _, err := buildCommandcodeBody([]byte(`{"model":"m","params":"nope"}`), "m"); err == nil {
		t.Fatal("a non-object params must be refused")
	}
}

// TestCommandcodeMaxTokensResolution pins which client field supplies the
// output budget. max_completion_tokens is the last source, not the first: a
// body that still carries max_tokens or max_output_tokens keeps the value it
// carried before this branch existed.
func TestCommandcodeMaxTokensResolution(t *testing.T) {
	tests := []struct {
		name string
		body string
		want any
	}{
		{
			name: "max_tokens wins over max_completion_tokens",
			body: `{"max_tokens":4096,"max_completion_tokens":2048}`,
			want: 4096.0,
		},
		{
			name: "max_output_tokens wins over max_completion_tokens",
			body: `{"max_output_tokens":2048,"max_completion_tokens":4096}`,
			want: 2048.0,
		},
		{
			name: "max_tokens alone",
			body: `{"max_tokens":4096}`,
			want: 4096.0,
		},
		{
			// The shape normalizeChatTokenFields leaves behind for a client
			// that sent both: max_tokens is gone, so this is the only field
			// left that names the budget. Reading it is what keeps the
			// client's value instead of the 1024 default.
			name: "max_completion_tokens alone, the post-normalizer shape",
			body: `{"max_completion_tokens":4096}`,
			want: 4096.0,
		},
		{
			name: "no field falls back to the default",
			body: `{}`,
			// The default is a Go int literal, not a decoded JSON number;
			// both serialise as 1024 on the wire.
			want: 1024,
		},
		{
			name: "a null value is an absent value",
			body: `{"max_tokens":null,"max_completion_tokens":3000}`,
			want: 3000.0,
		},
		{
			name: "all three null falls back to the default",
			body: `{"max_tokens":null,"max_output_tokens":null,"max_completion_tokens":null}`,
			want: 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal([]byte(tt.body), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if got := commandcodeMaxTokens(body); got != tt.want {
				t.Errorf("commandcodeMaxTokens() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBuildCommandcodeBody_SingleBudgetFieldOnWire pins that resolving the
// budget never puts a second spelling on the wire: the provider reads
// params.max_tokens only, and a body carrying both max_tokens and
// max_completion_tokens is exactly the shape that gets the client's budget
// discarded when only the first is read.
func TestBuildCommandcodeBody_SingleBudgetFieldOnWire(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantToks float64
	}{
		{
			name:     "client sent both, max_tokens is kept",
			body:     `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"max_completion_tokens":2048}`,
			wantToks: 4096,
		},
		{
			name:     "client sent max_completion_tokens only",
			body:     `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":2048}`,
			wantToks: 2048,
		},
		{
			name:     "client sent max_tokens only",
			body:     `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":2048}`,
			wantToks: 2048,
		},
		{
			name:     "client sent no budget",
			body:     `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			wantToks: 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := buildCommandcodeBody([]byte(tt.body), "m")
			if err != nil {
				t.Fatalf("buildCommandcodeBody() error = %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}

			// The provider reads params.max_tokens; a top-level copy is dead
			// weight the schema does not define.
			for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
				if v, has := payload[key]; has {
					t.Errorf("envelope carries %q at the top level = %v", key, v)
				}
			}

			params, ok := payload["params"].(map[string]any)
			if !ok {
				t.Fatalf("expected params object, got %T", payload["params"])
			}
			for _, key := range []string{"max_completion_tokens", "max_output_tokens"} {
				if v, has := params[key]; has {
					t.Errorf("params carries a second budget spelling %q = %v", key, v)
				}
			}
			if got := params["max_tokens"]; got != tt.wantToks {
				t.Errorf("params.max_tokens = %v, want %v", got, tt.wantToks)
			}
		})
	}
}
