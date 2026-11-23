package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func openAIToolsByName(t *testing.T, body []byte) map[string]map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode converted body: %v", err)
	}
	out := map[string]map[string]any{}
	tools, _ := req["tools"].([]any)
	for _, tRaw := range tools {
		tool, _ := tRaw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name != "" {
			out[name] = fn
		}
	}
	return out
}

// A client that loads a tool mid-conversation (inline-tools) sends the real
// schema as a tool_addition block. A Chat-format upstream reads only `tools`,
// so without the replay the model sees Bash() with no parameters and answers
// with an empty call the client refuses.
func TestTranslateClaudeToOpenAI_ReplaysToolAddition(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":[{"type":"tool_addition","tool":{"type":"tool_definition","name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]},
		{"role":"user","content":"two"}
	],"tools":[{"name":"Bash","description":"stub","input_schema":{"type":"object","properties":{}}}]}`)

	out, err := TranslateClaudeToOpenAI(body, "", "")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	fn := openAIToolsByName(t, out)["Bash"]
	if fn == nil {
		t.Fatalf("Bash tool missing from the converted request: %s", out)
	}
	params, _ := fn["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["command"]; !ok {
		t.Errorf("the added schema must replace the stub, got %v", params)
	}
	if strings.Contains(string(out), "tool_addition") {
		t.Errorf("the tool_addition block must not reach a Chat upstream: %s", out)
	}
}

// The removal side of the beta: a tool the client dropped must not stay
// callable upstream.
func TestTranslateClaudeToOpenAI_AppliesToolRemoval(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":[{"type":"tool_removal","name":"Bash"}]},
		{"role":"user","content":"two"}
	],"tools":[{"name":"Bash","input_schema":{"type":"object","properties":{}}},{"name":"Read","input_schema":{"type":"object","properties":{}}}]}`)

	out, err := TranslateClaudeToOpenAI(body, "", "")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	tools := openAIToolsByName(t, out)
	if _, ok := tools["Bash"]; ok {
		t.Errorf("a removed tool must not stay in the converted request: %s", out)
	}
	if _, ok := tools["Read"]; !ok {
		t.Errorf("the untouched tool must survive: %s", out)
	}
}

// A body without inline-tool blocks is not re-encoded.
func TestApplyInlineToolEdits_NoChangeKeepsBody(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if out := applyInlineToolEdits(body); string(out) != string(body) {
		t.Fatalf("body should be untouched, got %s", out)
	}
}
