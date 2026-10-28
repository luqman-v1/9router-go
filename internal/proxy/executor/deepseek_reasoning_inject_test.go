package executor

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
)

// TestInjectReasoningContent_DeepSeekApiKeyLane covers the API-key deepseek
// provider lane: upstream injects a reasoning_content placeholder into every
// assistant turn that lacks one (utils/reasoningContentInjector.js, scope
// "all"), because DeepSeek's thinking mode 400s a request declaring tools
// without it. Real reasoning the client replayed must survive untouched.
func TestInjectReasoningContent_DeepSeekApiKeyLane(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"file contents"},
		{"role":"assistant","content":"done","reasoning_content":"I reasoned about it"},
		{"role":"user","content":"continue"}
	]}`)

	out := InjectReasoningContent(body, "deepseek")

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := m["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if rc, _ := asst["reasoning_content"].(string); rc != " " {
		t.Errorf("assistant tool-call turn reasoning_content = %q, want the placeholder", rc)
	}
	real := msgs[3].(map[string]any)
	if rc, _ := real["reasoning_content"].(string); rc != "I reasoned about it" {
		t.Errorf("replayed reasoning_content = %q, want it preserved", rc)
	}
	if _, ok := msgs[0].(map[string]any)["reasoning_content"]; ok {
		t.Error("a user turn must not gain reasoning_content")
	}
}

// TestInjectReasoningContent_DeepSeekToolHistoryEndingOnTool pins the exact
// shape that reproduces the live 400: a long tool-call history ending on a
// `tool` message (no trailing user turn) with every assistant turn missing
// `reasoning_content`. DeepSeek answers that shape with "The reasoning_content
// in the thinking mode must be passed back to the API"; the injector must put
// the placeholder on every assistant turn.
func TestInjectReasoningContent_DeepSeekToolHistoryEndingOnTool(t *testing.T) {
	turns := 6
	body := []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"start"}`)
	for i := 1; i <= turns; i++ {
		id := fmt.Sprintf("call_%d", i)
		body = append(body, []byte(fmt.Sprintf(
			`,{"role":"assistant","content":"","tool_calls":[{"id":%q,"type":"function","function":{"name":"read","arguments":"{}"}}]},`+
				`{"role":"tool","tool_call_id":%q,"content":"x"}`,
			id, id))...)
	}
	body = append(body, []byte(`]}`)...)

	out := InjectReasoningContent(body, "deepseek")

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, msg := range m["messages"].([]any) {
		mm := msg.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		if rc, _ := mm["reasoning_content"].(string); rc != " " {
			t.Errorf("assistant at index %d reasoning_content = %q, want placeholder", i, rc)
		}
	}
}

// TestInjectReasoningContent_DeepSeekV4ProAlias mirrors upstream
// DEEPSEEK_V4_PRO_ALIASES: the -max/-none catalog ids are not wire ids, the
// request must go out as the base model with the thinking toggle at top level
// (`thinking.type`) and the effort in `reasoning_effort`. The SDK `extra_body`
// wrapper must not be emitted — it never reaches the wire, so `-none` would
// otherwise leave thinking enabled.
func TestInjectReasoningContent_DeepSeekV4ProAlias(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		wantModel    string
		wantType     string
		wantEffort   string
		wantEffortOn bool
	}{
		{"max alias", "deepseek-v4-pro-max", "deepseek-v4-pro", "enabled", "max", true},
		{"none alias", "deepseek-v4-pro-none", "deepseek-v4-pro", "disabled", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tt.model + `","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
			out := InjectReasoningContent(body, "deepseek")

			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got, _ := m["model"].(string); got != tt.wantModel {
				t.Errorf("model = %q, want %q", got, tt.wantModel)
			}
			thinking, _ := m["thinking"].(map[string]any)
			if got, _ := thinking["type"].(string); got != tt.wantType {
				t.Errorf("thinking.type = %q, want %q", got, tt.wantType)
			}
			if _, ok := m["extra_body"]; ok {
				t.Error("the SDK extra_body wrapper must not be emitted; thinking lives at top level")
			}
			effort, ok := m["reasoning_effort"].(string)
			if ok != tt.wantEffortOn {
				t.Errorf("reasoning_effort present = %v, want %v", ok, tt.wantEffortOn)
			}
			if tt.wantEffortOn && effort != tt.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q", effort, tt.wantEffort)
			}
		})
	}
}

// TestInjectReasoningContent_DeepSeekAliasNotForOtherProviders: the alias
// rewrite is scoped to the deepseek provider; the same model id on another
// provider must pass through untouched.
func TestInjectReasoningContent_DeepSeekAliasNotForOtherProviders(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro-max","messages":[{"role":"user","content":"hi"}]}`)
	out := InjectReasoningContent(body, "openrouter")
	if !strings.Contains(string(out), `"model":"deepseek-v4-pro-max"`) {
		t.Errorf("non-deepseek provider must not rewrite the alias: %s", out)
	}
}
