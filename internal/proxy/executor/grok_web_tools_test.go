package executor

import (
	"strings"
	"testing"

	json "encoding/json/v2"
)

func grokToolsBody(t *testing.T, messages string, tools string) map[string]any {
	t.Helper()
	body := map[string]any{}
	_ = json.Unmarshal([]byte(`{"messages":`+messages+`,"tools":`+tools+`}`), &body)
	return body
}

const grokSearchAndBashTools = `[
	{"type":"function","function":{"name":"web_search","description":"search the web","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}},
	{"type":"function","function":{"name":"bash","description":"run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}
]`

func TestGrokBuildToolRegistry(t *testing.T) {
	t.Run("no tools", func(t *testing.T) {
		registry := grokBuildToolRegistry(map[string]any{"messages": []any{}})
		if registry.enabled {
			t.Error("registry must be disabled without tools")
		}
	})
	t.Run("tool_choice none", func(t *testing.T) {
		body := grokToolsBody(t, `[{"role":"user","content":"hi"}]`, grokSearchAndBashTools)
		body["tool_choice"] = "none"
		if grokBuildToolRegistry(body).enabled {
			t.Error("tool_choice none must disable the bridge")
		}
	})
	t.Run("forced choice keeps only the named tool", func(t *testing.T) {
		body := grokToolsBody(t, `[{"role":"user","content":"hi"}]`, grokSearchAndBashTools)
		body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "bash"}}
		registry := grokBuildToolRegistry(body)
		if !registry.enabled || len(registry.toolsByName) != 1 {
			t.Fatalf("registry = %+v", registry)
		}
		if _, ok := registry.toolsByName["web_search"]; ok {
			t.Error("forced choice must drop the other tools")
		}
	})
	t.Run("executed calls are keyed", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "list files"},
				map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id":       "call-1",
					"function": map[string]any{"name": "bash", "arguments": `{"command":"ls"}`},
				}}},
			},
			"tools": []any{},
		}
		registry := grokBuildToolRegistry(body)
		if !registry.executedToolKeys["bash(command=ls)"] {
			t.Errorf("executed keys = %v", registry.executedToolKeys)
		}
		if len(registry.completedToolCalls) != 1 || registry.completedToolCalls[0] != "bash(ls)" {
			t.Errorf("completed calls = %v", registry.completedToolCalls)
		}
	})
}

func TestGrokBuildClientToolManifest(t *testing.T) {
	body := grokToolsBody(t, `[{"role":"user","content":"hi"}]`, grokSearchAndBashTools)
	manifest := grokBuildClientToolManifest(grokBuildToolRegistry(body), nil)
	for _, want := range []string{
		"CLIENT_TOOLS:",
		`tool_choice="auto"`,
		"tools (priority order for this request):",
		"1. name: web_search (args=query; required=query)",
		"2. name: bash (args=command; required=command)",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q:\n%s", want, manifest)
		}
	}
}

func TestGrokBuildMessageFoldsToolResults(t *testing.T) {
	// The tool result is the current turn's tail: the last user message is
	// the one that asked for it, so both the assistant calls and the result
	// follow it and are kept.
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "search go"},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c1", "function": map[string]any{"name": "web_search", "arguments": "{\"query\":\"go\"}"}}}},
			map[string]any{"role": "tool", "name": "web_search", "tool_call_id": "c1", "content": "results here"},
		},
		"tools": []any{},
	}
	registry := grokBuildToolRegistry(body)
	registry.enabled = true
	registry.toolsByName = map[string]grokClientTool{"web_search": {name: "web_search"}}
	message := grokBuildMessage(body["messages"].([]any), registry, nil)
	for _, want := range []string{
		"search go",
		"Previous assistant tool calls:",
		"CLIENT TOOL RESULT from caller runtime for web_search (c1)",
		"results here",
		"CLIENT_TOOLS:",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message missing %q:\n%s", want, message)
		}
	}

	// A tool result that precedes the last user message belongs to an earlier
	// turn and is dropped, the way upstream drops it.
	stale := []any{
		map[string]any{"role": "user", "content": "search go"},
		map[string]any{"role": "tool", "name": "web_search", "tool_call_id": "c0", "content": "stale result"},
		map[string]any{"role": "user", "content": "summarize"},
	}
	if message := grokBuildMessage(stale, registry, nil); strings.Contains(message, "stale result") {
		t.Errorf("stale tool result kept:\n%s", message)
	}
}

func TestGrokMapNativeToolCard(t *testing.T) {
	body := grokToolsBody(t, `[{"role":"user","content":"list my files please"}]`, grokSearchAndBashTools)
	registry := grokBuildToolRegistry(body)

	call := grokMapNativeToolCard(map[string]any{
		"bash": map[string]any{"args": map[string]any{"command": "ls -la"}},
	}, "card-9", registry)
	if call == nil {
		t.Fatal("bash card must map to the declared bash tool")
	}
	if call.ID != "card-9" || call.Function.Name != "bash" {
		t.Fatalf("call = %+v", call)
	}
	var args map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args["command"] != "ls -la" {
		t.Errorf("arguments = %s", call.Function.Arguments)
	}

	// A card with empty args still maps — the bash intent is declared — and
	// an unsupplied required argument is simply left out, matching upstream's
	// adaptArguments.
	if got := grokMapNativeToolCard(map[string]any{"bash": map[string]any{"args": map[string]any{}}}, "card-empty", registry); got == nil || got.Function.Name != "bash" {
		t.Errorf("empty-args card = %+v", got)
	}

	// An already-executed call is not re-emitted.
	registry.executedToolKeys["bash(command=ls -la)"] = true
	if got := grokMapNativeToolCard(map[string]any{
		"bash": map[string]any{"args": map[string]any{"command": "ls -la"}},
	}, "card-10", registry); got != nil {
		t.Errorf("replayed card = %+v", got)
	}
}

func TestGrokParseClientToolCallMarkup(t *testing.T) {
	body := grokToolsBody(t, `[{"role":"user","content":"hi"}]`, grokSearchAndBashTools)
	registry := grokBuildToolRegistry(body)

	calls := grokParseClientToolCallMarkup(
		`prefix <tool_call>{"name":"web_search","arguments":{"query":"go gopher"}}</tool_call> suffix`, registry)
	if len(calls) != 1 || calls[0].Function.Name != "web_search" {
		t.Fatalf("calls = %+v", calls)
	}
	var args map[string]any
	if json.Unmarshal([]byte(calls[0].Function.Arguments), &args) != nil || args["query"] != "go gopher" {
		t.Errorf("arguments = %s", calls[0].Function.Arguments)
	}
	if !strings.HasPrefix(calls[0].ID, "call_") {
		t.Errorf("id = %q", calls[0].ID)
	}

	// Undeclared tools are dropped, not forwarded.
	calls = grokParseClientToolCallMarkup(`<tool_call>{"name":"not_declared","arguments":{}}</tool_call>`, registry)
	if calls != nil {
		t.Errorf("undeclared call forwarded: %+v", calls)
	}

	// No markup at all.
	if calls := grokParseClientToolCallMarkup("plain answer", registry); calls != nil {
		t.Errorf("plain answer produced calls: %+v", calls)
	}
}

func TestGrokHasOpenToolCallMarkup(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"plain text", false},
		{"open <tool", true},
		{"open <tool_call", true},
		{"has <tool_call> but no close", true},
		{"<tool_call>x</tool_call> closed", false},
	}
	for _, tt := range tests {
		if got := grokHasOpenToolCallMarkup(tt.text); got != tt.want {
			t.Errorf("hasOpenToolCallMarkup(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestGrokStripToolCallMarkup(t *testing.T) {
	got := grokStripToolCallMarkup(`before <tool_call>{"name":"x"}</tool_call> after`)
	if got != "before  after" {
		t.Errorf("strip = %q", got)
	}
}
