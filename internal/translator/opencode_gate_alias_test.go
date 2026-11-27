package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestAliasOpenCodeGateTools(t *testing.T) {
	body := []byte(`{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"hi"}],"tools":[
{"type":"function","function":{"name":"terminal","description":"run a command","parameters":{"type":"object","properties":{"command":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["command","old_string","new_string"]}}},
{"type":"function","function":{"name":"run_code","description":"run code","parameters":{"type":"object","properties":{"code":{"type":"string"}}}}},
{"type":"function","function":{"name":"ReportFindings","description":"report","parameters":{"type":"object","properties":{}}}},
{"type":"function","function":{"name":"ScheduleWakeup","description":"wake","parameters":{"type":"object","properties":{}}}},
{"type":"function","function":{"name":"patch","description":"patch","parameters":{"type":"object","properties":{"old_text":{"type":"string"},"new_string":{"type":"string"}}}}}
],"tool_choice":{"type":"function","function":{"name":"terminal"}}}`)

	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	aliases := AliasOpenCodeGateTools(request, false, false)
	tools, _ := request["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("tools = %d, want 5", len(tools))
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		fn, _ := tool.(map[string]any)["function"].(map[string]any)
		names = append(names, fn["name"].(string))
		if s, _ := fn["name"].(string); s == "patch" {
			props, _ := fn["parameters"].(map[string]any)["properties"].(map[string]any)
			if _, bad := props["new_string"]; bad {
				t.Errorf("colliding param key new_string left in place: %v", props)
			}
			if alias, ok := aliases.ParamKeys["new_text2"]; !ok || alias != "new_string" {
				t.Errorf("colliding param alias not recorded: %v", aliases.ParamKeys)
			}
		}
	}
	joined := strings.Join(names, ",")
	for _, gated := range []string{"terminal", "run_code", "ReportFindings"} {
		if strings.Contains(strings.ToLower(joined), strings.ToLower(gated)) {
			t.Errorf("gated name %q survived: %s", gated, joined)
		}
	}
	for _, alias := range []string{"run_command", "run_program", "report_notes"} {
		if !strings.Contains(joined, alias) {
			t.Errorf("alias %q missing: %s", alias, joined)
		}
	}
	if got := aliases.ToolNames["run_command"]; got != "terminal" {
		t.Errorf("run_command maps to %q, want terminal", got)
	}
	choice, _ := request["tool_choice"].(map[string]any)
	fn, _ := choice["function"].(map[string]any)
	if fn["name"] != "run_command" {
		t.Errorf("tool_choice not retargeted: %v", choice)
	}
	out, err := json.Marshal(request, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	var check map[string]any
	if json.Unmarshal(out, &check) != nil || len(check) == 0 {
		t.Errorf("shaped body not valid: %s", out)
	}
}

func TestRestoreOpenCodeGateAliases(t *testing.T) {
	aliases := OpenCodeGateAliases{
		ToolNames: map[string]string{"run_command": "terminal", "report_notes": "ReportFindings"},
		ParamKeys: map[string]string{"old_text": "old_string", "new_text": "new_string"},
	}
	t.Run("json", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{\"old_text\":\"a\",\"new_text\":\"b\"}"}}]}}]}`)
		want := string(RestoreOpenCodeGateAliases(in, aliases))
		if !strings.Contains(want, `"name":"terminal"`) {
			t.Errorf("tool name not restored: %s", want)
		}
		if !strings.Contains(want, `\"old_string\":\"a\"`) || !strings.Contains(want, `\"new_string\":\"b\"`) {
			t.Errorf("argument keys not restored: %s", want)
		}
	})
	t.Run("sse", func(t *testing.T) {
		in := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"report_notes\",\"arguments\":\"{\\\"old_text\\\":\\\"z\\\"}\"}}]}}]}\n\ndata: [DONE]\n\n")
		out := string(RestoreOpenCodeGateAliases(in, aliases))
		if !strings.Contains(out, `"name":"ReportFindings"`) {
			t.Errorf("SSE tool name not restored: %s", out)
		}
		if !strings.Contains(out, `\"old_string\":\"z\"`) {
			t.Errorf("SSE argument key not restored: %s", out)
		}
		if !strings.Contains(out, "data: [DONE]") {
			t.Errorf("SSE sentinel lost: %s", out)
		}
	})
}
