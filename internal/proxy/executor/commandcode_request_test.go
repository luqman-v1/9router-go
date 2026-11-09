package executor

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"9router/proxy/internal/providers"
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
		{
			name: "budget above the provider cap is clamped",
			body: `{"max_tokens":384000}`,
			want: 200000.0,
		},
		{
			name: "budget exactly at the provider cap passes through",
			body: `{"max_tokens":200000}`,
			want: 200000.0,
		},
		{
			name: "max_completion_tokens above the cap is clamped",
			body: `{"max_completion_tokens":500000}`,
			want: 200000.0,
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

func TestCommandcodeSlugifyAndSessionUUID(t *testing.T) {
	if got := commandcodeSlugify("/home/u/My Project"); got != "home-u-my-project" {
		t.Errorf("slug = %q", got)
	}
	if got := commandcodeSlugify("/srv/fooBar/Ünï"); got != "srv-foo-bar-ueni" {
		t.Errorf("slug = %q", got)
	}
	if commandcodeSlugify("/") != "" || commandcodeProjectSlug == "" {
		t.Errorf("empty slug must fall back, got %q", commandcodeProjectSlug)
	}
	a, b := commandcodeSessionUUID("s1"), commandcodeSessionUUID("s1")
	if a != b || a == commandcodeSessionUUID("s2") {
		t.Errorf("session uuid not stable/distinct: %s %s", a, b)
	}
	if _, err := uuid.Parse(a); err != nil {
		t.Errorf("not a uuid: %v", err)
	}
	a1, a2 := commandcodeSessionUUID(""), commandcodeSessionUUID("")
	if a1 == a2 {
		t.Errorf("empty session must be random, got %s twice", a1)
	}
}

func TestCommandcodeBodySession(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no session", `{"model":"m","messages":[]}`, ""},
		{
			"claude _session_ suffix",
			`{"metadata":{"user_id":"user_abc_account__session_9f0c1a2b-3d4e-5f60-7a8b-9c0d1e2f3a4b"}}`,
			"claude\0009f0c1a2b-3d4e-5f60-7a8b-9c0d1e2f3a4b",
		},
		{
			"claude JSON user_id",
			`{"metadata":{"user_id":"{\"account_id\":\"acc\",\"session_id\":\"abc123\"}"}}`,
			"claude\000abc123",
		},
		{"raw claude user id", `{"metadata":{"user_id":"user_abc"}}`, "claude\000user_abc"},
		{"prompt_cache_key", `{"prompt_cache_key":"conv-1"}`, "conv-1"},
		{"session_id", `{"session_id":"conv-2"}`, "conv-2"},
		{"conversation_id", `{"conversation_id":"conv-3"}`, "conv-3"},
		{"prompt_cache_key wins over session_id", `{"prompt_cache_key":"a","session_id":"b"}`, "a"},
		{"metadata.user_id wins over prompt_cache_key", `{"metadata":{"user_id":"u1"},"prompt_cache_key":"a"}`, "claude\000u1"},
		{"blank values ignored", `{"prompt_cache_key":"  "}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tt := range tests {
		if got := commandcodeBodySession([]byte(tt.body)); got != tt.want {
			t.Errorf("%s: commandcodeBodySession = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCommandcodeSessionKeyStablePerConversation(t *testing.T) {
	system := []byte(`{"model":"m","system":"You are helpful.","messages":[{"role":"user","content":"first turn"}]}`)

	// A later turn appends messages; the prefix key must not move.
	turn1 := commandcodeSessionKey(system, "", "conn-1")
	turn2 := []byte(`{"model":"m","system":"You are helpful.","messages":[{"role":"user","content":"first turn"},{"role":"assistant","content":"ok"},{"role":"user","content":"second turn"}]}`)
	if got := commandcodeSessionKey(turn2, "", "conn-1"); got != turn1 {
		t.Errorf("prefix key moved across turns: %q vs %q", turn1, got)
	}

	// A different first turn is a different conversation.
	other := []byte(`{"model":"m","system":"You are helpful.","messages":[{"role":"user","content":"different"}]}`)
	if got := commandcodeSessionKey(other, "", "conn-1"); got == turn1 {
		t.Errorf("different first user turn must hash differently")
	}

	// Headers win, then body fields, then prefix, then connection.
	if got := commandcodeSessionKey(system, "hdr-1", "conn-1"); got != "hdr-1" {
		t.Errorf("header session must win, got %q", got)
	}
	if got := commandcodeSessionKey([]byte(`{"prompt_cache_key":"pc"}`), "", "conn-1"); got != "pc" {
		t.Errorf("body session must beat prefix/connection, got %q", got)
	}
	if got := commandcodeSessionKey(nil, "", "conn-1"); got != "conn\000conn-1" {
		t.Errorf("connection fallback = %q", got)
	}
	if got := commandcodeSessionKey(nil, "", ""); got != "" {
		t.Errorf("nothing to key on = %q", got)
	}

	// Whatever the layer, the derived threadId must stay a valid UUID.
	if _, err := uuid.Parse(commandcodeSessionUUID(turn1)); err != nil {
		t.Errorf("prefix-derived threadId not a uuid: %v", err)
	}
}

func TestForwardCommandcodeRetriesTransient(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("x-project-slug") == "" {
			t.Errorf("missing x-project-slug")
		}
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"type":"text-delta","text":"ok"}` + "\n" + `{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()
	rec := httptest.NewRecorder()
	err := ForwardCommandcode(rec, &Request{
		Config: &providers.ProviderConfig{BaseURL: srv.URL}, Client: srv.Client(),
		Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), SessionID: "s",
	})
	if err != nil || calls != 2 || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("err=%v calls=%d body=%s", err, calls, rec.Body.String())
	}
}

// Two requests with no client session must not mint a fresh threadId each:
// the upstream prompt cache is keyed on it, and a per-request random id
// re-cached the whole prefix on every turn (decolua/9router#5006).
func TestForwardCommandcodeThreadIDStableWithoutSession(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("x-session-id"))
		_, _ = w.Write([]byte(`{"type":"text-delta","text":"ok"}` + "\n" + `{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()
	send := func(connID, firstUser string) {
		seen = nil
		rec := httptest.NewRecorder()
		err := ForwardCommandcode(rec, &Request{
			Config: &providers.ProviderConfig{BaseURL: srv.URL}, Client: srv.Client(),
			Body:         []byte(`{"model":"m","messages":[{"role":"user","content":"` + firstUser + `"}]}`),
			ConnectionID: connID,
		})
		if err != nil {
			t.Fatalf("ForwardCommandcode: %v", err)
		}
	}
	// Same prefix (system + first user turn) → same threadId even across
	// connections: identical prefixes are cache-compatible upstream.
	send("conn-1", "hi")
	first := seen[0]
	send("conn-1", "hi")
	if seen[0] != first {
		t.Errorf("same conversation minted a second threadId: %q then %q", first, seen[0])
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Errorf("threadId not a uuid: %v", err)
	}
	send("conn-1", "a different first turn")
	if seen[0] == first {
		t.Errorf("different first user turn must get a different threadId")
	}
}

func TestCommandcodeFinishReasonMappedToOpenAI(t *testing.T) {
	for in, want := range map[string]string{
		"tool-calls": "tool_calls", "content-filter": "content_filter",
		"stop": "stop", "length": "length", "error": "stop",
	} {
		state := &CommandcodeStreamState{ToolIndexByID: map[string]int{}}
		ProcessCommandcodeEvent(map[string]any{"type": "finish-step", "finishReason": in}, "finish-step", state)
		out := ProcessCommandcodeEvent(map[string]any{"type": "finish"}, "finish", state)
		if len(out) == 0 || !strings.Contains(out[0], `"finish_reason":"`+want+`"`) {
			t.Errorf("%s: want finish_reason %s, got %v", in, want, out)
		}
	}
}

func TestCommandcodeFinishCarriesCacheUsage(t *testing.T) {
	state := &CommandcodeStreamState{ToolIndexByID: map[string]int{}}
	out := ProcessCommandcodeEvent(map[string]any{
		"type": "finish", "finishReason": "stop",
		"totalUsage": map[string]any{
			"inputTokens": 3033.0, "outputTokens": 20.0,
			"cachedInputTokens": 2944.0, "reasoningTokens": 7.0,
		},
	}, "finish", state)
	if len(out) == 0 {
		t.Fatal("no chunk")
	}
	var chunk struct {
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(out[0]), &chunk); err != nil {
		t.Fatal(err)
	}
	if chunk.Usage.PromptTokens != 3033 || chunk.Usage.PromptTokensDetails.CachedTokens != 2944 || chunk.Usage.CompletionTokensDetails.ReasoningTokens != 7 {
		t.Errorf("usage = %+v", chunk.Usage)
	}
}

func TestCommandcodeUsageFallsBackToFinishStepAndTotals(t *testing.T) {
	state := &CommandcodeStreamState{ToolIndexByID: map[string]int{}}
	ProcessCommandcodeEvent(map[string]any{"type": "finish-step", "finishReason": "stop",
		"usage": map[string]any{"inputTokens": 100.0, "outputTokens": 5.0, "cachedInputTokens": 64.0},
	}, "finish-step", state)
	out := ProcessCommandcodeEvent(map[string]any{"type": "finish"}, "finish", state)
	if len(out) == 0 || !strings.Contains(out[0], `"total_tokens":105`) || !strings.Contains(out[0], `"cached_tokens":64`) {
		t.Fatalf("chunk = %v", out)
	}
	u := commandcodeUsage(state)
	if u.PromptTokens != 100 || u.CompletionTokens != 5 || u.CachedTokens != 64 {
		t.Errorf("gateway usage = %+v", u)
	}
	est := commandcodeUsage(&CommandcodeStreamState{OutputLength: 40})
	if est.CompletionTokens != 10 || est.PromptTokens != 0 {
		t.Errorf("estimate fallback = %+v", est)
	}
}

func TestBuildCommandcodeBody_ReasoningBlock(t *testing.T) {
	input := []byte(`{
		"model": "zai-org/GLM-5.2",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "hi", "reasoning_content": " "}
		]
	}`)

	resBytes, err := buildCommandcodeBody(input, "zai-org/GLM-5.2")
	if err != nil {
		t.Fatalf("failed to build commandcode body: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(resBytes, &payload); err != nil {
		t.Fatalf("failed to parse payload: %v", err)
	}

	params, ok := payload["params"].(map[string]any)
	if !ok {
		t.Fatalf("missing params in payload: %#v", payload)
	}

	msgs, ok := params["messages"].([]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("missing messages in params: %#v", params)
	}

	asstMsg := msgs[1].(map[string]any)
	contentBlocks, ok := asstMsg["content"].([]any)
	if !ok {
		t.Fatalf("missing content blocks in assistant message: %#v", asstMsg)
	}

	foundReasoning := false
	for _, block := range contentBlocks {
		bMap, ok := block.(map[string]any)
		if ok && bMap["type"] == "reasoning" && bMap["text"] == " " {
			foundReasoning = true
			break
		}
	}

	if !foundReasoning {
		t.Errorf("expected content blocks to contain reasoning block with text ' ', got %#v", contentBlocks)
	}
}
