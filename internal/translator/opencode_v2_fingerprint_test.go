package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func opencodeGenuineToolDescription(name string) any {
	for _, def := range openCodeGenuineTools {
		if fn, _ := def["function"].(map[string]any); fn["name"] == name {
			return fn["description"]
		}
	}
	return nil
}

func decodeShaped(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode shaped body: %v", err)
	}
	return m
}

func shapedToolNames(m map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, tool := range m["tools"].([]any) {
		tm := tool.(map[string]any)
		var name string
		if fn, ok := tm["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		} else {
			name, _ = tm["name"].(string)
		}
		out[name] = tm
	}
	return out
}

// TestConcealOpenCodeV2InstallsInertBuiltins covers the gate contract
// measured live (opencode_free_harness.go): every shaped request declares an
// entry for every built-in name (the gate 429s without them), and with no
// caller tools those entries are inert placeholders.
func TestConcealOpenCodeV2InstallsInertBuiltins(t *testing.T) {
	body := []byte(`{"model":"glm-5.3-flash-free","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out, rename := ConcealOpenCodeV2FingerprintTools(body, false, false)
	if len(rename) != 0 {
		t.Fatalf("no caller name may be rewritten, got %v", rename)
	}
	m := decodeShaped(t, out)
	tools := shapedToolNames(m)
	if len(tools) != len(openCodeGenuineToolNames) {
		t.Fatalf("placeholder set not installed: %d tools", len(tools))
	}
	for _, name := range openCodeGenuineToolNames {
		tm, ok := tools[name]
		if !ok {
			t.Fatalf("builtin %q missing", name)
		}
		fn := tm["function"].(map[string]any)
		// The gate validates the built-in definitions against the CLI 2.x
		// capture (a stub answers 403 FreeTierError), so the declarations are
		// verbatim and the prompt does the steering instead.
		if fn["description"] != opencodeGenuineToolDescription(name) {
			t.Fatalf("builtin %q is not the verbatim CLI definition", name)
		}
	}
	messages := m["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("system message not inserted, first role %v", first["role"])
	}
	content := first["content"].(string)
	if !strings.HasPrefix(content, "You are an AI agent running in OpenCode") {
		t.Fatalf("system does not start with the harness preamble: %.80s", content)
	}
	if strings.Contains(content, "# Code Mode") {
		t.Fatal("Code Mode section must be stripped: it routes tool use through the inert built-ins")
	}
	if !strings.Contains(content, opencodeDirectCallNote) {
		t.Fatal("direct-call note missing")
	}
}

// TestConcealOpenCodeV2KeepsCallerToolsDeclared verifies the caller's own
// tools stay declared natively (so the model emits their real names) next to
// the inert builtin placeholders, and history tool_call names are untouched.
func TestConcealOpenCodeV2KeepsCallerToolsDeclared(t *testing.T) {
	body := []byte(`{"model":"glm-5.3-flash-free","stream":true,"tool_choice":{"type":"function","function":{"name":"weather"}},"messages":[{"role":"system","content":"Be terse."},{"role":"user","content":"weather in jakarta"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"jakarta\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"31C"}],"tools":[` +
		`{"type":"function","function":{"name":"weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`)
	out, rename := ConcealOpenCodeV2FingerprintTools(body, false, false)
	if len(rename) != 0 {
		t.Fatalf("history names must not be rewritten, got %v", rename)
	}
	m := decodeShaped(t, out)
	tools := shapedToolNames(m)
	weather, ok := tools["weather"]
	if !ok {
		t.Fatal("caller tool no longer declared")
	}
	if weather["function"].(map[string]any)["description"] != "Get weather" {
		t.Fatal("caller tool declaration was replaced")
	}
	if len(tools) != len(openCodeGenuineToolNames)+1 {
		t.Fatalf("want caller tool plus %d placeholders, got %d tools", len(openCodeGenuineToolNames), len(tools))
	}
	messages := m["messages"].([]any)
	system := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, opencodeDirectCallNote) {
		t.Fatal("direct-call note missing")
	}
	if !strings.HasPrefix(system, "You are an AI agent running in OpenCode") || !strings.Contains(system, "Be terse.") {
		t.Fatal("caller system text not preserved after the preamble")
	}
	assistant := messages[2].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	if call["function"].(map[string]any)["name"] != "weather" {
		t.Fatal("history tool_call renamed")
	}
	// A forced choice naming a caller tool cannot travel as-is; intent
	// survives as "required".
	if m["tool_choice"] != "required" {
		t.Fatalf("forced caller choice should become required, got %v", m["tool_choice"])
	}
}

// TestConcealOpenCodeV2ShapesClaudeAndResponsesLanes checks the other two wire
// formats keep their own shapes: caller tools in lane form, placeholders
// appended in the same form.
func TestConcealOpenCodeV2ShapesClaudeAndResponsesLanes(t *testing.T) {
	t.Run("claude messages", func(t *testing.T) {
		body := []byte(`{"model":"qwen3.8-max","max_tokens":64,"stream":true,"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","description":"Look up","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, false, false)
		m := decodeShaped(t, out)
		tools := shapedToolNames(m)
		if _, ok := tools["lookup"]; !ok {
			t.Fatal("caller claude tool not declared")
		}
		for _, name := range openCodeGenuineToolNames {
			tm, ok := tools[name]
			if !ok {
				t.Fatalf("builtin %q missing", name)
			}
			if _, ok := tm["input_schema"]; !ok {
				t.Fatalf("claude placeholder lost input_schema: %v", tm)
			}
		}
		system, _ := m["system"].(string)
		if !strings.HasPrefix(system, "You are an AI agent running in OpenCode") {
			t.Fatalf("claude system not prefixed: %.60s", system)
		}
		if !strings.Contains(system, "You are a helpful assistant.") || !strings.Contains(system, opencodeDirectCallNote) {
			t.Fatal("caller system text or direct-call note missing")
		}
	})

	t.Run("responses", func(t *testing.T) {
		body := []byte(`{"model":"muse-spark-1.3","stream":true,"instructions":"Be brief.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"function","name":"probe","description":"Probe","parameters":{"type":"object","properties":{}}}]}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, true, false)
		m := decodeShaped(t, out)
		tools := shapedToolNames(m)
		if _, ok := tools["probe"]; !ok {
			t.Fatal("caller responses tool not declared")
		}
		for _, name := range openCodeGenuineToolNames {
			tm, ok := tools[name]
			if !ok {
				t.Fatalf("builtin %q missing", name)
			}
			if tm["strict"] != false {
				t.Fatalf("flat placeholder must keep strict:false, got %v", tm["strict"])
			}
		}
		instructions, _ := m["instructions"].(string)
		if !strings.HasPrefix(instructions, "You are an AI agent running in OpenCode") {
			t.Fatalf("instructions not prefixed: %.60s", instructions)
		}
		if !strings.Contains(instructions, "Be brief.") || !strings.Contains(instructions, opencodeDirectCallNote) {
			t.Fatal("caller instructions or direct-call note missing")
		}
		if m["tool_choice"] != "auto" {
			t.Fatalf("responses lane should default to auto, got %v", m["tool_choice"])
		}
	})
}

// TestConcealOpenCodeV2ToolChoiceRules covers the choice rewriting matrix.
func TestConcealOpenCodeV2ToolChoiceRules(t *testing.T) {
	t.Run("none dropped", func(t *testing.T) {
		out, _ := ConcealOpenCodeV2FingerprintTools([]byte(`{"messages":[{"role":"user","content":"hi"}],"tool_choice":"none"}`), false, false)
		m := decodeShaped(t, out)
		if _, present := m["tool_choice"]; present {
			t.Fatalf("none choice must be dropped, got %v", m["tool_choice"])
		}
	})
	t.Run("genuine forced kept", func(t *testing.T) {
		out, _ := ConcealOpenCodeV2FingerprintTools([]byte(`{"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"read"}}}`), false, false)
		m := decodeShaped(t, out)
		fn := m["tool_choice"].(map[string]any)["function"].(map[string]any)
		if fn["name"] != "read" {
			t.Fatalf("genuine forced choice must survive, got %v", m["tool_choice"])
		}
	})
	t.Run("responses forceAuto", func(t *testing.T) {
		out, _ := ConcealOpenCodeV2FingerprintTools([]byte(`{"instructions":"x","input":[],"tool_choice":{"type":"function","name":"probe"}}`), true, true)
		m := decodeShaped(t, out)
		if m["tool_choice"] != "auto" {
			t.Fatalf("auto-only model must force auto, got %v", m["tool_choice"])
		}
	})
}

// TestConcealOpenCodeV2LeavesUnparseableBodyAlone guards the passthrough.
func TestConcealOpenCodeV2LeavesUnparseableBodyAlone(t *testing.T) {
	out, rename := ConcealOpenCodeV2FingerprintTools([]byte("not json"), false, false)
	if string(out) != "not json" || len(rename) != 0 {
		t.Fatal("unparseable body must pass through untouched")
	}
}

// TestConcealOpenCodeV2ModelIdentity verifies the harness preamble's static
// "Your Model" block is corrected by a suffix that follows the client's
// selected model, without editing the fingerprinted preamble bytes.
func TestConcealOpenCodeV2ModelIdentity(t *testing.T) {
	const model = "mimo-v2.6-flash-free"
	tests := []struct {
		name string
		flat bool
		body string
		read func(t *testing.T, m map[string]any) string
	}{
		{
			name: "chat completions",
			body: `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`,
			read: func(t *testing.T, m map[string]any) string {
				first := m["messages"].([]any)[0].(map[string]any)
				return first["content"].(string)
			},
		},
		{
			name: "responses",
			flat: true,
			body: `{"model":"` + model + `","instructions":"Be terse.","input":[]}`,
			read: func(t *testing.T, m map[string]any) string {
				return m["instructions"].(string)
			},
		},
		{
			name: "claude messages",
			body: `{"model":"` + model + `","system":"Be terse.","messages":[{"role":"user","content":"hi"}]}`,
			read: func(t *testing.T, m map[string]any) string {
				return m["system"].(string)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := ConcealOpenCodeV2FingerprintTools([]byte(tt.body), tt.flat, false)
			m := decodeShaped(t, out)
			system := tt.read(t, m)
			if !strings.HasPrefix(system, openCodeHarnessPrefix()) {
				t.Fatal("harness preamble must stay byte-exact at the start")
			}
			if !strings.HasSuffix(system, "Your actual model for this session is: "+model) {
				t.Fatalf("identity suffix missing or wrong: %.200s", system[len(system)-min(len(system), 200):])
			}
			if strings.Index(system, "Your actual model for this session") <= strings.Index(system, openCodeHarnessPrefix()) {
				t.Fatal("identity suffix must follow the preamble, not replace it")
			}
		})
	}
}

// TestConcealOpenCodeV2SendsCallerSystemTextOnce guards a regression the review
// caught on an earlier head: the stripped path carries the caller's text in the
// suffix, and the injector prepended the original again, so every shaped
// request sent the caller system prompt twice — doubled tokens and latency on
// the large Claude Code prompts #1024 is about.
func TestConcealOpenCodeV2SendsCallerSystemTextOnce(t *testing.T) {
	const sentinel = "SENTINEL-XYZZY"
	cases := []struct {
		name string
		flat bool
		body string
	}{
		{"chat string", false, `{"model":"m","messages":[{"role":"system","content":"` + sentinel + `"},{"role":"user","content":"hi"}]}`},
		{"chat array", false, `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"` + sentinel + `"}]},{"role":"user","content":"hi"}]}`},
		{"claude string", false, `{"model":"m","system":"` + sentinel + `","messages":[{"role":"user","content":"hi"}]}`},
		{"claude array", false, `{"model":"m","system":[{"type":"text","text":"` + sentinel + `"}],"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", true, `{"model":"m","instructions":"` + sentinel + `","input":[]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := ConcealOpenCodeV2FingerprintTools([]byte(tt.body), tt.flat, false)
			if n := strings.Count(string(out), sentinel); n != 1 {
				t.Fatalf("caller system text appears %d times, want exactly 1", n)
			}
		})
	}
}

// TestConcealOpenCodeV2BuiltinNamedCallerToolIsNotDuplicated covers the review's
// collision case: two declarations sharing a name answer a server error on the
// gate, so a caller tool that reuses a built-in name replaces it — each built-in
// name stays declared exactly once.
func TestConcealOpenCodeV2BuiltinNamedCallerToolIsNotDuplicated(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","description":"caller read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`)
	out, _ := ConcealOpenCodeV2FingerprintTools(body, false, false)
	m := decodeShaped(t, out)
	tools, _ := m["tools"].([]any)
	seen := map[string]int{}
	var readDesc string
	for _, tool := range tools {
		tm := tool.(map[string]any)
		fn := tm["function"].(map[string]any)
		name := fn["name"].(string)
		seen[name]++
		if name == "read" {
			readDesc, _ = fn["description"].(string)
		}
	}
	if seen["read"] != 1 {
		t.Fatalf("built-in name read declared %d times, want exactly 1", seen["read"])
	}
	if readDesc != "caller read" {
		t.Fatalf("the caller's own read tool must win the name, got %q", readDesc)
	}
	if len(tools) != len(openCodeGenuineToolNames) {
		t.Fatalf("total declarations = %d, want %d (caller read folded into the built-in slot)", len(tools), len(openCodeGenuineToolNames))
	}
	for _, name := range openCodeGenuineToolNames {
		if seen[name] != 1 {
			t.Fatalf("built-in %q declared %d times, want exactly 1", name, seen[name])
		}
	}
}
