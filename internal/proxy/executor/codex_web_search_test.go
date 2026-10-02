package executor

import (
	json "encoding/json/v2"
	"testing"
)

// buildCodexBody runs a client body through the Codex transform and hands back
// the forwarded Responses body, so a test asserts on what the backend receives.
func buildCodexBody(t *testing.T, body string) map[string]any {
	t.Helper()
	out, _, err := buildResponsesBody([]byte(body))
	if err != nil {
		t.Fatalf("buildResponsesBody: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return req
}

// inputTypes lists the type of every input item, which is where the Lite
// prefix shows up.
func inputTypes(req map[string]any) []string {
	items, _ := req["input"].([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		t, _ := m["type"].(string)
		out = append(out, t)
	}
	return out
}

func hasItemType(req map[string]any, want string) bool {
	for _, t := range inputTypes(req) {
		if t == want {
			return true
		}
	}
	return false
}

// toolList returns the forwarded top-level tools, or nil when the field is
// absent or not a list.
func toolList(req map[string]any) []any {
	items, _ := req["tools"].([]any)
	return items
}

// TestBuildResponsesBody_CodexLiteKeepsHostedWebSearch is upstream 7bf931781:
// Lite cannot execute a hosted search, so a body asking for one must stay on
// regular Responses instead of having the tool wrapped into the prefix and lost.
func TestBuildResponsesBody_CodexLiteKeepsHostedWebSearch(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			body := `{"model":"` + model + `","instructions":"be brief","tools":[{"type":"function","name":"run","parameters":{"type":"object","properties":{}}},{"type":"web_search"}],"tool_choice":"none","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}]}`
			req := buildCodexBody(t, body)

			if hasItemType(req, "additional_tools") {
				t.Errorf("hosted search must not travel in a Lite prefix: %v", inputTypes(req))
			}
			tools := toolList(req)
			if len(tools) != 2 {
				t.Fatalf("tools = %v, want both definitions on the top-level field", tools)
			}
			if got, _ := tools[1].(map[string]any)["type"].(string); got != "web_search" {
				t.Errorf("tools[1].type = %q, want web_search", got)
			}
			if got, _ := req["tool_choice"].(string); got != "none" {
				t.Errorf("tool_choice = %q, want the caller's choice to stand", got)
			}
			if got, _ := req["instructions"].(string); got != "be brief" {
				t.Errorf("instructions = %q, want them kept top-level off Lite", got)
			}
			if _, has := req["parallel_tool_calls"]; has {
				t.Error("parallel_tool_calls is a Lite-only field and must be absent")
			}
			if codexResponsesLiteBody(mustMarshal(t, req)) {
				t.Error("a body asking for hosted search must not claim the Lite transport")
			}
		})
	}
}

// TestBuildResponsesBody_CodexLiteLiftsHostedSearchOutOfPrefix: a body that
// already arrived in Lite shape carries the tool inside the prefix. It has to be
// lifted out, because that is the one place a search can no longer run.
func TestBuildResponsesBody_CodexLiteLiftsHostedSearchOutOfPrefix(t *testing.T) {
	body := `{"model":"gpt-6-sol","tools":null,"tool_choice":"none","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"run","parameters":{"type":"object","properties":{}}},{"type":"web_search"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}]}`
	req := buildCodexBody(t, body)

	if hasItemType(req, "additional_tools") {
		t.Errorf("the prefix must be dropped: %v", inputTypes(req))
	}
	tools := toolList(req)
	if len(tools) != 2 {
		t.Fatalf("tools = %v, want the prefixed definitions lifted to the top level", tools)
	}
	if got, _ := tools[1].(map[string]any)["type"].(string); got != "web_search" {
		t.Errorf("tools[1].type = %q, want web_search", got)
	}
}

// TestBuildResponsesBody_CodexLiteKeepsDeveloperMessageWhenLifting: upstream
// drops the whole prefix but keeps the developer message that travelled beside
// it, because that message is the caller's own instructions. Losing it would
// answer the request under different instructions than the client asked for.
func TestBuildResponsesBody_CodexLiteKeepsDeveloperMessageWhenLifting(t *testing.T) {
	body := `{"model":"gpt-6-sol","instructions":"","tools":null,"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"web_search"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"Only answer in French"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}]}`
	req := buildCodexBody(t, body)

	items, _ := req["input"].([]any)
	found := false
	for _, item := range items {
		m, _ := item.(map[string]any)
		if t, _ := m["type"].(string); t != "message" {
			continue
		}
		if text := ExtractSimpleText(mustMarshal(t, m["content"])); text == "Only answer in French" {
			found = true
		}
	}
	if !found {
		t.Errorf("the developer's own instructions were dropped with the prefix: %v", inputTypes(req))
	}
	if got, _ := req["instructions"].(string); got != "" {
		t.Errorf("instructions = %q, want empty (they already live in the input)", got)
	}
}

// TestBuildResponsesBody_CodexLiteDoesNotDuplicateTools: a replayed transcript
// can carry the same definition both top-level and inside the prefix, and a
// duplicate definition is what the Codex backend rejects.
func TestBuildResponsesBody_CodexLiteDoesNotDuplicateTools(t *testing.T) {
	tool := `{"type":"function","name":"run","parameters":{"type":"object","properties":{}}}`
	body := `{"model":"gpt-6-sol","tools":[` + tool + `,{"type":"web_search"}],"input":[{"type":"additional_tools","role":"developer","tools":[` + tool + `,{"type":"web_search"}]},{"type":"additional_tools","role":"developer","tools":[` + tool + `]},{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}]}`
	req := buildCodexBody(t, body)

	tools := toolList(req)
	if len(tools) != 2 {
		t.Fatalf("tools = %v, want the two distinct definitions only", tools)
	}
	names := map[string]int{}
	for _, item := range tools {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		kind, _ := m["type"].(string)
		names[kind+":"+name]++
	}
	if names["function:run"] != 1 || names["web_search:"] != 1 {
		t.Errorf("tool definitions = %v, want each declared once", names)
	}
}

// TestBuildResponsesBody_CodexLiteAutoWebSearchFlag: the marker is ours, so it
// is consumed — the tool is registered when it is missing and the field never
// reaches the backend, which would reject it as unknown.
func TestBuildResponsesBody_CodexLiteAutoWebSearchFlag(t *testing.T) {
	t.Run("registers search and drops the marker", func(t *testing.T) {
		req := buildCodexBody(t, `{"model":"gpt-6-sol","_autoCodexWebSearch":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}]}`)

		if _, has := req["_autoCodexWebSearch"]; has {
			t.Error("the marker must never be forwarded to the Codex backend")
		}
		tools := toolList(req)
		if len(tools) != 1 {
			t.Fatalf("tools = %v, want the hosted search tool", tools)
		}
		if got, _ := tools[0].(map[string]any)["type"].(string); got != "web_search" {
			t.Errorf("tools[0].type = %q, want web_search", got)
		}
		if hasItemType(req, "additional_tools") {
			t.Errorf("hosted search must not be wrapped into a Lite prefix: %v", inputTypes(req))
		}
	})

	t.Run("does not register search twice", func(t *testing.T) {
		req := buildCodexBody(t, `{"model":"gpt-6-sol","_autoCodexWebSearch":true,"tools":[{"type":"web_search"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}]}`)

		tools := toolList(req)
		if len(tools) != 1 {
			t.Errorf("tools = %v, want the one the caller already declared", tools)
		}
		if _, has := req["_autoCodexWebSearch"]; has {
			t.Error("the marker must never be forwarded to the Codex backend")
		}
	})

	t.Run("marker on a classic model is still consumed", func(t *testing.T) {
		// The marker is ours wherever it lands, and a backend that does not know
		// the field rejects the request over it.
		req := buildCodexBody(t, `{"model":"gpt-5.6-sol","_autoCodexWebSearch":true,"messages":[{"role":"user","content":"hi"}]}`)
		if _, has := req["_autoCodexWebSearch"]; has {
			t.Error("the marker must never be forwarded, whatever the model")
		}
	})
}

// TestBuildResponsesBody_CodexLiteUnchangedWithoutWebSearch is the guard the
// whole port hangs on: a body that never mentions hosted search must leave the
// transform byte-for-byte as it was before this change. It compares a
// deterministic re-encoding against a golden captured from the pre-fix code,
// so a field added, dropped or reordered by the web-search handling shows up
// here rather than as a surprise 400 from the Codex backend.
func TestBuildResponsesBody_CodexLiteUnchangedWithoutWebSearch(t *testing.T) {
	inputs := map[string]string{
		"codexLiteChatCompletions":  `{"model":"gpt-6-sol","instructions":"be brief","tools":[{"type":"function","function":{"name":"get_weather","description":"w","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"messages":[{"role":"user","content":"hi"}]}`,
		"codexLiteAlreadyPrefixed":   `{"model":"gpt-6-sol","input":[{"type":"additional_tools","role":"developer","tools":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"instructions":"be brief"}`,
		"codexLiteEffortFromClient":  `{"model":"gpt-6-luna","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
		"codexLiteResponsesPassthru": `{"model":"gpt-6-sol(high)","instructions":"be brief","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
		"classicCodex":               `{"model":"gpt-5.6-sol","instructions":"be brief","tools":[{"type":"function","function":{"name":"get_weather","description":"w","parameters":{"type":"object"}}}],"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
	}
	golden := map[string]string{
		"classicCodex":              `{"input":[{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"instructions":"be brief","model":"gpt-5.6-sol","reasoning":{"effort":"high","summary":"auto"},"store":false,"stream":true,"tools":[{"description":"w","name":"get_weather","parameters":{"properties":{},"type":"object"},"type":"function"}]}`,
		"codexLiteChatCompletions":  `{"input":[{"role":"developer","tools":[{"description":"w","name":"get_weather","parameters":{"properties":{"city":{"type":"string"}},"type":"object"},"type":"function"}],"type":"additional_tools"},{"content":[{"text":"be brief","type":"input_text"}],"role":"developer","type":"message"},{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"instructions":"","model":"gpt-6-sol","parallel_tool_calls":false,"reasoning":{"context":"all_turns","effort":"medium"},"store":false,"stream":true,"tool_choice":"auto","tools":null}`,
		"codexLiteAlreadyPrefixed":  `{"input":[{"role":"developer","tools":[],"type":"additional_tools"},{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"instructions":"be brief","model":"gpt-6-sol","reasoning":{"context":"all_turns","effort":"medium"},"store":false,"stream":true}`,
		"codexLiteEffortFromClient": `{"input":[{"role":"developer","tools":[],"type":"additional_tools"},{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"instructions":"","model":"gpt-6-luna","parallel_tool_calls":false,"reasoning":{"context":"all_turns","effort":"high"},"store":false,"stream":true,"tool_choice":"auto","tools":null}`,
		"codexLiteResponsesPassthru": `{"input":[{"role":"developer","tools":[],"type":"additional_tools"},{"content":[{"text":"be brief","type":"input_text"}],"role":"developer","type":"message"},{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"instructions":"","model":"gpt-6-sol","parallel_tool_calls":false,"reasoning":{"context":"all_turns","effort":"medium"},"store":false,"stream":true,"tool_choice":"auto","tools":null}`,
	}

	for name, want := range golden {
		t.Run(name, func(t *testing.T) {
			out, _, err := buildResponsesBody([]byte(inputs[name]))
			if err != nil {
				t.Fatalf("buildResponsesBody: %v", err)
			}
			got := deterministicJSON(t, out)
			if got != want {
				t.Errorf("body changed for a request without hosted search.\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// deterministicJSON re-encodes a body with sorted keys so a golden stays stable
// across Go versions, whose map key order is not.
func deterministicJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	out, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

// TestCodexResponsesLiteBody covers the decision that drives the Lite header:
// Lite models claim it, hosted search and every other model do not.
func TestCodexResponsesLiteBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "lite model claims lite", body: `{"model":"gpt-6-sol","input":[]}`, want: true},
		{name: "lite override suffix still claims lite", body: `{"model":"gpt-6-luna(high)","input":[]}`, want: true},
		{name: "hosted search drops lite", body: `{"model":"gpt-6-sol","tools":[{"type":"web_search"}],"input":[]}`, want: false},
		{name: "classic model never claims lite", body: `{"model":"gpt-5.6-sol","input":[]}`, want: false},
		{name: "unreadable body never claims lite", body: `not json`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexResponsesLiteBody([]byte(tt.body)); got != tt.want {
				t.Errorf("codexResponsesLiteBody(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
// mustMarshal encodes a decoded fragment back to JSON for a helper that only
// reads text, so a test does not hand-roll the same cast twice.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return out
}
