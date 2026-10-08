package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestFitToolNames_NoTools(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	got, toolMap := FitToolNames(body)
	if string(got) != string(body) {
		t.Errorf("expected body unchanged, got: %s", got)
	}
	if len(toolMap) != 0 {
		t.Errorf("expected nil/empty toolMap, got: %v", toolMap)
	}
}

func TestFitToolNames_ToolsUnder64(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "function": {"name": "get_weather", "description": "weather"}},
			{"type": "function", "function": {"name": "exactly_64_characters_tool_name_abcdefghijklmnopqrstuvwxyz01234", "description": "64 chars"}}
		]
	}`)
	got, toolMap := FitToolNames(body)
	if string(got) != string(body) {
		t.Errorf("expected body unchanged for <=64 chars, got: %s", got)
	}
	if len(toolMap) != 0 {
		t.Errorf("expected empty toolMap, got: %v", toolMap)
	}
}

func TestFitToolNames_SingleLongTool(t *testing.T) {
	longName := "mcp__github_server__create_or_update_file_contents_with_commit_details_action" // 77 chars
	body := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "commit file"}],
		"tools": [
			{"type": "function", "function": {"name": "` + longName + `", "description": "commit"}}
		]
	}`)

	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 mapping, got: %v", toolMap)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal fitted body failed: %v", err)
	}

	tools := parsed["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	fn := tool0["function"].(map[string]any)
	shortName := fn["name"].(string)

	if len(shortName) > MaxToolNameLength {
		t.Errorf("shortName length %d > %d: %q", len(shortName), MaxToolNameLength, shortName)
	}
	if !strings.HasSuffix(shortName, "_1") {
		t.Errorf("expected shortName to end with _1, got: %q", shortName)
	}
	if toolMap[shortName] != longName {
		t.Errorf("expected toolMap[%q] = %q, got: %q", shortName, longName, toolMap[shortName])
	}
}

func TestFitToolNames_MultipleCollidingLongTools(t *testing.T) {
	// Two tools sharing the exact same 65-character prefix
	sharedPrefix := "mcp__shared_long_server_name__perform_very_important_action_base_" // 65 chars
	nameA := sharedPrefix + "variant_alpha_one"
	nameB := sharedPrefix + "variant_beta_two"

	body := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "run both"}],
		"tools": [
			{"type": "function", "function": {"name": "` + nameA + `"}},
			{"type": "function", "function": {"name": "` + nameB + `"}}
		]
	}`)

	got, toolMap := FitToolNames(body)
	if len(toolMap) != 2 {
		t.Fatalf("expected 2 mappings, got: %v", toolMap)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal fitted body failed: %v", err)
	}

	tools := parsed["tools"].([]any)
	shortA := tools[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	shortB := tools[1].(map[string]any)["function"].(map[string]any)["name"].(string)

	if shortA == shortB {
		t.Fatalf("short names must not collide: %q vs %q", shortA, shortB)
	}
	if len(shortA) > MaxToolNameLength || len(shortB) > MaxToolNameLength {
		t.Errorf("short names exceed max length: %d, %d", len(shortA), len(shortB))
	}
	if toolMap[shortA] != nameA {
		t.Errorf("toolMap[shortA] mismatch: got %q, want %q", toolMap[shortA], nameA)
	}
	if toolMap[shortB] != nameB {
		t.Errorf("toolMap[shortB] mismatch: got %q, want %q", toolMap[shortB], nameB)
	}
}

func TestFitToolNames_MessagesAndToolChoice(t *testing.T) {
	longName := "mcp__postgres_database__execute_complex_analytical_query_with_transaction"
	body := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "query data"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "` + longName + `", "arguments": "{}"}}
			]},
			{"role": "tool", "name": "` + longName + `", "tool_call_id": "call_1", "content": "result"}
		],
		"tools": [
			{"type": "function", "function": {"name": "` + longName + `"}}
		],
		"tool_choice": {
			"type": "function",
			"function": {"name": "` + longName + `"}
		}
	}`)

	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 mapping, got %v", toolMap)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Verify tool in tools declaration
	tools := parsed["tools"].([]any)
	shortName := tools[0].(map[string]any)["function"].(map[string]any)["name"].(string)

	// Verify assistant tool_calls
	msgs := parsed["messages"].([]any)
	asst := msgs[1].(map[string]any)
	asstCalls := asst["tool_calls"].([]any)
	asstName := asstCalls[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	if asstName != shortName {
		t.Errorf("assistant tool_call name %q != tool declaration name %q", asstName, shortName)
	}

	// Verify role: tool message name
	toolMsg := msgs[2].(map[string]any)
	toolMsgName := toolMsg["name"].(string)
	if toolMsgName != shortName {
		t.Errorf("tool message name %q != %q", toolMsgName, shortName)
	}

	// Verify tool_choice
	tc := parsed["tool_choice"].(map[string]any)
	tcName := tc["function"].(map[string]any)["name"].(string)
	if tcName != shortName {
		t.Errorf("tool_choice name %q != %q", tcName, shortName)
	}
}

func TestFitToolNames_ClaudeFormat(t *testing.T) {
	longName := "mcp__filesystem_server__search_and_replace_text_across_directory_tree"
	body := []byte(`{
		"model": "claude-3-opus-20240229",
		"messages": [
			{"role": "user", "content": "find replace"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "tu_1", "name": "` + longName + `", "input": {}}
			]}
		],
		"tools": [
			{"name": "` + longName + `", "description": "replace across files"}
		],
		"tool_choice": {"type": "tool", "name": "` + longName + `"}
	}`)

	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 mapping, got %v", toolMap)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tools := parsed["tools"].([]any)
	shortName := tools[0].(map[string]any)["name"].(string)
	if len(shortName) > MaxToolNameLength {
		t.Errorf("shortName length %d > %d", len(shortName), MaxToolNameLength)
	}

	msgs := parsed["messages"].([]any)
	asst := msgs[1].(map[string]any)
	content := asst["content"].([]any)
	asstName := content[0].(map[string]any)["name"].(string)
	if asstName != shortName {
		t.Errorf("Claude tool_use name %q != %q", asstName, shortName)
	}

	tc := parsed["tool_choice"].(map[string]any)
	if tc["name"] != shortName {
		t.Errorf("Claude tool_choice name %q != %q", tc["name"], shortName)
	}
}

func TestRestoreToolNames_AllFormats(t *testing.T) {
	origName := "mcp__server_name__action_detail_something_very_long_indeed_action_12345"
	shortName := "mcp__server_name__action_detail_something_very_long_indeed_act_1"
	toolMap := map[string]string{shortName: origName}

	t.Run("OpenAI non-stream", func(t *testing.T) {
		body := []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"` + shortName + `","arguments":"{}"}}]}}]}`)
		restored := RestoreToolNamesInPayload(body, toolMap)
		if !strings.Contains(string(restored), origName) {
			t.Errorf("expected %q restored in OpenAI non-stream, got: %s", origName, restored)
		}
		if strings.Contains(string(restored), shortName) {
			t.Errorf("shortName should be replaced: %s", restored)
		}
	})

	t.Run("OpenAI streaming SSE", func(t *testing.T) {
		chunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"" + shortName + "\"}}]}}]}\n\n")
		restored := RestoreToolNamesInPayload(chunk, toolMap)
		if !strings.Contains(string(restored), origName) {
			t.Errorf("expected %q restored in OpenAI SSE, got: %s", origName, restored)
		}
		if !strings.HasPrefix(string(restored), "data: ") {
			t.Errorf("SSE frame must preserve data: prefix: %s", restored)
		}
	})

	t.Run("OpenAI legacy function_call", func(t *testing.T) {
		body := []byte(`{"choices":[{"message":{"function_call":{"name":"` + shortName + `"}}}]}`)
		restored := RestoreToolNamesInPayload(body, toolMap)
		if !strings.Contains(string(restored), origName) {
			t.Errorf("expected %q restored in function_call, got: %s", origName, restored)
		}
	})

	t.Run("Claude non-stream", func(t *testing.T) {
		body := []byte(`{"content":[{"type":"tool_use","id":"tu1","name":"` + shortName + `","input":{}}]}`)
		restored := RestoreToolNamesInPayload(body, toolMap)
		if !strings.Contains(string(restored), origName) {
			t.Errorf("expected %q restored in Claude non-stream, got: %s", origName, restored)
		}
	})

	t.Run("Claude streaming SSE", func(t *testing.T) {
		chunk := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"" + shortName + "\"}}\n\n")
		restored := RestoreToolNamesInPayload(chunk, toolMap)
		if !strings.Contains(string(restored), origName) {
			t.Errorf("expected %q restored in Claude SSE, got: %s", origName, restored)
		}
		if !strings.Contains(string(restored), "event: content_block_start\n") {
			t.Errorf("event line must be preserved: %s", restored)
		}
	})

	t.Run("Responses API streaming item", func(t *testing.T) {
		chunk := []byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"" + shortName + "\"}}\n\n")
		restored := RestoreToolNamesInPayload(chunk, toolMap)
		if !strings.Contains(string(restored), origName) {
			t.Errorf("expected %q restored in Responses item, got: %s", origName, restored)
		}
	})

	t.Run("Gemini native candidate", func(t *testing.T) {
		body := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + shortName + `"}}]}}]}`)
		restored := RestoreToolNamesInPayload(body, toolMap)
		if !strings.Contains(string(restored), origName) {
			t.Errorf("expected %q restored in Gemini native, got: %s", origName, restored)
		}
	})
}

// A /v1/responses request keeps its conversation history in `input`, not
// `messages`. Without a walker there the declaration was fitted but the history
// still called the tool by its original long name, so the upstream received a
// request that contradicted itself and the 64-char fix never actually applied
// on the Responses-native lane.
func TestFitToolNames_ResponsesInputStaysConsistent(t *testing.T) {
	longName := "mcp__server_name__action_detail_something_very_long_indeed_action_12345"
	body := []byte(`{
		"model": "gpt-5",
		"tools": [{"type": "function", "name": "` + longName + `", "parameters": {"type": "object"}}],
		"input": [
			{"type": "function_call", "call_id": "call_1", "name": "` + longName + `"},
			{"role": "user", "content": [{"type": "input_text", "text": "go on"}]}
		]
	}`)

	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected the long name to be fitted, got map %v", toolMap)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	declared := parsed["tools"].([]any)[0].(map[string]any)["name"].(string)
	inHistory := parsed["input"].([]any)[0].(map[string]any)["name"].(string)

	if len(declared) > MaxToolNameLength {
		t.Errorf("declared name %q is %d chars, over the %d limit", declared, len(declared), MaxToolNameLength)
	}
	if declared != inHistory {
		t.Errorf("request contradicts itself: declared %q but input history says %q", declared, inHistory)
	}
	if inHistory == longName {
		t.Error("the long name still travelled in input; the 64-char fix did not apply")
	}

	// The upstream answers with the fitted name; the client must receive the
	// name it actually declared, so the map has to reverse it on output[].
	resp := []byte(`{"output":[{"type":"function_call","call_id":"call_1","name":"` + declared + `"}]}`)
	restored := RestoreToolNamesInPayload(resp, toolMap)
	if !strings.Contains(string(restored), longName) {
		t.Errorf("expected %q restored for the client, got: %s", longName, restored)
	}
}

// The upstream echoes the fitted name in output[]; the client must receive the
// name it actually declared. The Responses passthrough relays the body itself,
// so this is where the map has to be applied.
func TestRestoreToolNames_ResponsesOutputItem(t *testing.T) {
	longName := "mcp__server_name__action_detail_something_very_long_indeed_action_12345"
	shortName := "mcp__server_name__action_detail_something_very_long_indeed_act_1"
	toolMap := map[string]string{shortName: longName}

	body := []byte(`{"output":[{"type":"function_call","call_id":"call_1","name":"` + shortName + `"}]}`)
	restored := RestoreToolNamesInPayload(body, toolMap)
	if !strings.Contains(string(restored), longName) {
		t.Errorf("expected %q restored in Responses output[], got: %s", longName, restored)
	}
}

// The fit boundary is "<= 64 is left alone, 65 is fitted". The existing
// fixtures were 63 and 65, so nothing pinned the exact boundary — which is the
// line this whole feature turns on.
func TestFitToolNames_ExactBoundary(t *testing.T) {
	exactly64 := "mcp__boundary_tool_name_that_is_exactly_sixty_four_characters_xy"
	if len(exactly64) != MaxToolNameLength {
		t.Fatalf("fixture is %d chars; it must be exactly %d for this test to mean anything",
			len(exactly64), MaxToolNameLength)
	}
	exactly65 := exactly64 + "x"

	for _, tc := range []struct {
		name    string
		fixture string
		wantFit bool
	}{
		{"exactly 64 is left alone", exactly64, false},
		{"65 is fitted", exactly65, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-4","tools":[{"type":"function","function":{"name":"` + tc.fixture + `"}}]}`)
			got, toolMap := FitToolNames(body)
			if tc.wantFit && len(toolMap) != 1 {
				t.Fatalf("expected %d-char name to be fitted, got map %v", len(tc.fixture), toolMap)
			}
			if !tc.wantFit && len(toolMap) != 0 {
				t.Fatalf("expected %d-char name to be left alone, got map %v", len(tc.fixture), toolMap)
			}
			if !tc.wantFit && string(got) != string(body) {
				t.Errorf("body must be untouched at the limit, got: %s", got)
			}
		})
	}
}

func TestFitToolNames_GeminiContents(t *testing.T) {
	longName := "mcp__gemini_server__call_a_super_long_function_name_that_exceeds_limits_123"
	body := []byte(`{
		"contents": [
			{"parts": [{"functionCall": {"name": "` + longName + `"}}]},
			{"parts": [{"functionResponse": {"name": "` + longName + `"}}]}
		]
	}`)
	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 tool mapped, got %v", toolMap)
	}
	if strings.Contains(string(got), longName) {
		t.Errorf("expected long name replaced in contents: %s", string(got))
	}
}

func TestFitToolNames_LegacyFunctions(t *testing.T) {
	longName := "mcp__legacy_server__call_a_super_long_function_name_that_exceeds_limits_123"
	body := []byte(`{
		"functions": [{"name": "` + longName + `"}]
	}`)
	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 tool mapped, got %v", toolMap)
	}
	if strings.Contains(string(got), longName) {
		t.Errorf("expected long name replaced in functions: %s", string(got))
	}
}
