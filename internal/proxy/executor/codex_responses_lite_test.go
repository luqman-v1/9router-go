package executor

import (
	"context"
	json "encoding/json/v2"
	"reflect"
	"testing"
)

func TestCodexWireShape(t *testing.T) {
	tests := []struct {
		name, body string
		header     string
		lite       bool
	}{
		{"luna chat defaults to classic", `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"parallel_tool_calls":true,"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`, "", false},
		{"sol native defaults to classic", `{"model":"gpt-6.1-sol","input":"hi","parallel_tool_calls":true,"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`, "", false},
		{"native prefix selects lite", `{"model":"gpt-6-luna","input":[{"type":"additional_tools","role":"developer","tools":[]},{"role":"user","content":"hi"}]}`, "", true},
		{"explicit header selects lite", `{"model":"gpt-6.1-sol","input":"hi","tools":[{"type":"function","name":"read"}]}`, " true ", true},
		{"false header remains classic", `{"model":"gpt-6-luna","input":"hi"}`, "false", false},
		{"search overrides header", `{"model":"gpt-6-luna","input":"hi","tools":[{"type":"web_search"}]}`, "true", false},
		{"search overrides prefix", `{"model":"gpt-6-luna","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"web_search"}]}]}`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := buildResponsesBody([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithCodexLiteHeader(context.Background(), tt.header)
			body, lite, err := shapeCodexResponsesBody(body, codexLiteRequested(ctx))
			if err != nil {
				t.Fatal(err)
			}
			if lite != tt.lite {
				t.Fatalf("lite = %v, want %v: %s", lite, tt.lite, body)
			}
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			if hasCodexLitePrefix(req) != lite {
				t.Errorf("prefix disagrees with mode: %s", body)
			}
			if lite {
				if req["tools"] != nil || req["parallel_tool_calls"] != false {
					t.Errorf("mixed Lite shape: %s", body)
				}
				reasoning, _ := req["reasoning"].(map[string]any)
				if reasoning["context"] != "all_turns" || reasoning["summary"] != nil {
					t.Errorf("Lite reasoning: %s", body)
				}
			} else if tt.name == "luna chat defaults to classic" || tt.name == "sol native defaults to classic" {
				if req["parallel_tool_calls"] != true || len(toolList(req)) != 1 {
					t.Errorf("classic tools/parallel lost: %s", body)
				}
			}
		})
	}
}

func TestCodexLitePreservesExplicitToolItems(t *testing.T) {
	body := `{"model":"gpt-6-luna","input":[{"type":"additional_tools","id":"at_first","role":"developer","tools":[{"type":"function","name":"read"}]},{"type":"message","role":"user","content":"hi"},{"type":"additional_tools","id":"at_second","role":"developer","tools":[{"type":"function","name":"edit"}]}]}`
	transformed, _, err := buildResponsesBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if err := json.Unmarshal(transformed, &before); err != nil {
		t.Fatal(err)
	}
	out, lite, err := shapeCodexResponsesBody(transformed, false)
	if err != nil || !lite {
		t.Fatalf("lite=%v err=%v", lite, err)
	}
	var after map[string]any
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after["input"], before["input"]) {
		t.Errorf("Lite input items changed:\nbefore: %#v\nafter:  %#v", before["input"], after["input"])
	}
	if after["parallel_tool_calls"] != false {
		t.Errorf("Lite must disable parallel tools: %s", out)
	}
}

func TestCodexLiteMergesPrefixAndTopLevel(t *testing.T) {
	body := `{"model":"gpt-6-luna","instructions":"be brief","tool_choice":{"type":"function","name":"read"},"tools":[{"type":"function","name":"edit"},{"type":"function","name":"read"}],"input":[{"type":"additional_tools","id":"at_original","role":"developer","tools":[{"type":"function","name":"read"}]},{"type":"message","role":"user","content":"hi"}]}`
	transformed, _, err := buildResponsesBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out, lite, err := shapeCodexResponsesBody(transformed, false)
	if err != nil || !lite {
		t.Fatalf("lite=%v err=%v", lite, err)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	input := req["input"].([]any)
	newPrefix := input[0].(map[string]any)
	original := input[2].(map[string]any)
	if len(newPrefix["tools"].([]any)) != 1 || original["id"] != "at_original" || len(original["tools"].([]any)) != 1 || req["tools"] != nil || req["instructions"] != "" {
		t.Errorf("mixed or lost fields: %s", out)
	}
	choice, ok := req["tool_choice"].(map[string]any)
	if !ok || choice["name"] != "read" {
		t.Errorf("forced tool choice lost: %s", out)
	}
}

func TestCodexLitePreservesDistinctNamelessTools(t *testing.T) {
	body := `{"model":"gpt-6-luna","tools":[{"type":"mcp","server_label":"beta"}],"input":[{"type":"additional_tools","id":"at_original","role":"developer","tools":[{"type":"mcp","server_label":"alpha"}]}]}`
	transformed, _, err := buildResponsesBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out, lite, err := shapeCodexResponsesBody(transformed, false)
	if err != nil || !lite {
		t.Fatalf("lite=%v err=%v", lite, err)
	}
	var req struct {
		Input []struct {
			ID    string `json:"id"`
			Tools []struct {
				Label string `json:"server_label"`
			} `json:"tools"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 2 || len(req.Input[0].Tools) != 1 || len(req.Input[1].Tools) != 1 || req.Input[0].Tools[0].Label != "beta" || req.Input[1].Tools[0].Label != "alpha" || req.Input[1].ID != "at_original" {
		t.Errorf("distinct tools or original prefix lost: %s", out)
	}
}

func TestResponsesBuilderDoesNotApplyCodexShape(t *testing.T) {
	body := `{"model":"gpt-6-luna","input":[{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"function","name":"web_search_20250305"}]}`
	out, _, err := buildResponsesBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	tool := toolList(req)[0].(map[string]any)
	if hasCodexLitePrefix(req) || tool["name"] != "web_search_20250305" {
		t.Errorf("Codex rules leaked: %s", out)
	}
}
