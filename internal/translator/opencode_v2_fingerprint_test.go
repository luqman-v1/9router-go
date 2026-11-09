package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func decodeShaped(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode shaped body: %v", err)
	}
	return m
}

// TestConcealOpenCodeV2InjectsHarnessAndGenuineTools covers the gate contract
// measured live (opencode_free_harness.go): every shaped request starts with
// the harness preamble and declares only the genuine built-ins.
func TestConcealOpenCodeV2InjectsHarnessAndGenuineTools(t *testing.T) {
	body := []byte(`{"model":"glm-5.3-flash-free","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out, rename := ConcealOpenCodeV2FingerprintTools(body, false, false)
	if len(rename) != 0 {
		t.Fatalf("no caller name may be rewritten, got %v", rename)
	}
	m := decodeShaped(t, out)

	tools, _ := m["tools"].([]any)
	if len(tools) != len(openCodeGenuineToolNames) {
		t.Fatalf("genuine tool set not installed: %d tools", len(tools))
	}
	for _, tool := range tools {
		tm := tool.(map[string]any)
		fn := tm["function"].(map[string]any)
		if !openCodeGenuineToolSet[fn["name"].(string)] {
			t.Fatalf("non-genuine declaration %v", fn["name"])
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
	if len(content) < len(openCodeHarnessSystem) {
		t.Fatalf("system shorter than the preamble: %d", len(content))
	}
	if !strings.Contains(content, openCodeBuiltinPlaceholderNote) {
		t.Fatal("placeholder note missing")
	}
}

// TestConcealOpenCodeV2MovesCallerToolsToPrompt verifies caller tools are
// described in the system text and never declared, keeping their names in
// history untouched.
func TestConcealOpenCodeV2MovesCallerToolsToPrompt(t *testing.T) {
	body := []byte(`{"model":"glm-5.3-flash-free","stream":true,"tool_choice":{"type":"function","function":{"name":"weather"}},"messages":[{"role":"system","content":"Be terse."},{"role":"user","content":"weather in jakarta"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"jakarta\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"31C"}],"tools":[` +
		`{"type":"function","function":{"name":"weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`)
	out, rename := ConcealOpenCodeV2FingerprintTools(body, false, false)
	if len(rename) != 0 {
		t.Fatalf("history names must not be rewritten, got %v", rename)
	}
	m := decodeShaped(t, out)

	for _, tool := range m["tools"].([]any) {
		fn := tool.(map[string]any)["function"].(map[string]any)
		if fn["name"] == "weather" {
			t.Fatal("caller tool still declared")
		}
	}

	messages := m["messages"].([]any)
	system := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "Session tool protocol") || !strings.Contains(system, `"name":"weather"`) {
		t.Fatal("caller tool not described under the session tool protocol")
	}
	if !strings.HasPrefix(system, "You are an AI agent running in OpenCode") || !strings.Contains(system, "Be terse.") {
		t.Fatal("caller system text not preserved after the preamble")
	}

	assistant := messages[2].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	if call["function"].(map[string]any)["name"] != "weather" {
		t.Fatal("history tool_call renamed")
	}

	// A forced choice naming a caller tool cannot travel; intent survives as
	// "required".
	if m["tool_choice"] != "required" {
		t.Fatalf("forced caller choice should become required, got %v", m["tool_choice"])
	}
}

// TestConcealOpenCodeV2ShapesClaudeAndResponsesLanes checks the other two wire
// formats keep their own shapes.
func TestConcealOpenCodeV2ShapesClaudeAndResponsesLanes(t *testing.T) {
	t.Run("claude messages", func(t *testing.T) {
		body := []byte(`{"model":"qwen3.8-max","max_tokens":64,"stream":true,"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","description":"Look up","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, false, false)
		m := decodeShaped(t, out)
		for _, tool := range m["tools"].([]any) {
			tm := tool.(map[string]any)
			if _, ok := tm["input_schema"]; !ok {
				t.Fatalf("claude tool lost input_schema: %v", tm)
			}
			if !openCodeGenuineToolSet[tm["name"].(string)] {
				t.Fatalf("non-genuine claude declaration %v", tm["name"])
			}
		}
		system, _ := m["system"].(string)
		if !strings.HasPrefix(system, "You are an AI agent running in OpenCode") {
			t.Fatalf("claude system not prefixed: %.60s", system)
		}
		if !strings.Contains(system, `"name":"lookup"`) {
			t.Fatal("caller claude tool not described")
		}
	})

	t.Run("responses", func(t *testing.T) {
		body := []byte(`{"model":"muse-spark-1.3","stream":true,"instructions":"Be brief.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"function","name":"probe","description":"Probe","parameters":{"type":"object","properties":{}}}]}`)
		out, _ := ConcealOpenCodeV2FingerprintTools(body, true, false)
		m := decodeShaped(t, out)
		for _, tool := range m["tools"].([]any) {
			tm := tool.(map[string]any)
			if !openCodeGenuineToolSet[tm["name"].(string)] {
				t.Fatalf("non-genuine flat declaration %v", tm["name"])
			}
			if tm["strict"] != false {
				t.Fatalf("flat tool must keep strict:false, got %v", tm["strict"])
			}
		}
		instructions, _ := m["instructions"].(string)
		if !strings.HasPrefix(instructions, "You are an AI agent running in OpenCode") {
			t.Fatalf("instructions not prefixed: %.60s", instructions)
		}
		if !strings.Contains(instructions, `"name":"probe"`) {
			t.Fatal("caller responses tool not described")
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
