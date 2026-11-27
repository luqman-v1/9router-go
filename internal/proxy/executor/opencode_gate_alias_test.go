package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"9router/proxy/internal/translator"
)

// The gate filter measured on the free tier must be applied by the shaping
// helper the lanes actually call, not only by the translator in isolation.
func TestShapeOpenCodeFreeBody_GateAliases(t *testing.T) {
	const body = `{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"edit a file"}],"tools":[{"type":"function","function":{"name":"terminal","description":"run","parameters":{"type":"object","properties":{"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["old_string","new_string"]}}}]}`
	var aliases translator.OpenCodeGateAliases
	shaped, names := shapeOpenCodeFreeBody([]byte(body), false, "glm-5.3-flash-free", &aliases)
	if len(names) != 0 {
		t.Errorf("v2 lane must not rename built-ins: %v", names)
	}
	if aliases.ToolNames["run_command"] != "terminal" {
		t.Errorf("aliases missing: %+v", aliases.ToolNames)
	}
	if aliases.ParamKeys["old_text"] != "old_string" || aliases.ParamKeys["new_text"] != "new_string" {
		t.Errorf("param aliases missing: %+v", aliases.ParamKeys)
	}
	var request map[string]any
	if err := json.Unmarshal(shaped, &request); err != nil {
		t.Fatal(err)
	}
	tools, _ := request["tools"].([]any)
	found := false
	for _, tool := range tools {
		fn, _ := tool.(map[string]any)["function"].(map[string]any)
		if name, _ := fn["name"].(string); name == "run_command" {
			found = true
			params, _ := fn["parameters"].(map[string]any)
			props, _ := params["properties"].(map[string]any)
			if _, exists := props["old_string"]; exists {
				t.Errorf("gated param key survived shaping: %v", props)
			}
		}
	}
	if !found {
		t.Errorf("aliased tool declaration not found: %s", shaped)
	}
	restored := string(translator.RestoreOpenCodeGateAliases(shaped, aliases))
	if !strings.Contains(restored, `"name":"terminal"`) {
		t.Errorf("response restore failed: %s", restored)
	}
}
