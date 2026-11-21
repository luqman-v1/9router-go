package translator

import (
	"encoding/json"
	"slices"
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

// A Gemini turn can carry the tool result without the call — the call is in
// the previous turn. The quick-check token list has to name functionResponse
// too, or such a body skips the fit entirely and the long name reaches the
// upstream unshortened while the declaration beside it was fitted.
func TestFitToolNames_GeminiResponseOnlyHistory(t *testing.T) {
	longName := "mcp__gemini_server__call_a_super_long_function_name_that_exceeds_limits_123"
	body := []byte(`{
		"contents": [
			{"parts": [{"functionResponse": {"name": "` + longName + `", "response": {}}}]}
		]
	}`)
	got, toolMap := FitToolNames(body)
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 tool mapped, got %v", toolMap)
	}
	if strings.Contains(string(got), longName) {
		t.Errorf("long name survived in a response-only turn: %s", string(got))
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

// wantHashAlias is the OmniRoute #20258 alias of orig: the first 51 chars of
// the sanitized name, "_", then the first 12 hex chars of sha256(original).
func wantHashAlias(t *testing.T, orig string) string {
	t.Helper()
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, orig)
	prefix := safe
	if len(prefix) > MaxToolNameLength-toolNameHashLen-1 {
		prefix = prefix[:MaxToolNameLength-toolNameHashLen-1]
	}
	return prefix + "_" + toolNameHash(orig)
}

// TestFitToolNamesForProvider_OpenAI_SHA256Alias pins the upstream wire format:
// `${safePrefix}_${hash12}`, at most 64 chars, ending in _<12 hex chars>.
func TestFitToolNamesForProvider_OpenAI_SHA256Alias(t *testing.T) {
	longName := "mcp__github_server__create_or_update_file_contents_with_commit_details_action" // 77 chars
	if len(longName) <= MaxToolNameLength {
		t.Fatalf("fixture name must exceed %d chars, got %d", MaxToolNameLength, len(longName))
	}
	body := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "` + longName + `", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "result"}
		],
		"tools": [{"type": "function", "function": {"name": "` + longName + `", "description": "test"}}],
		"tool_choice": {"type": "function", "function": {"name": "` + longName + `"}}
	}`)

	got, toolMap := FitToolNamesForProvider(body, "openai")
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 mapped tool, got %d: %v", len(toolMap), toolMap)
	}

	var alias string
	for k, v := range toolMap {
		alias = k
		if v != longName {
			t.Errorf("expected mapped value to be original name %q, got %q", longName, v)
		}
	}

	want := wantHashAlias(t, longName)
	if alias != want {
		t.Errorf("alias = %q, want %q", alias, want)
	}
	// The alias shape: prefix_ then exactly 12 hex chars, total <= 64.
	if len(alias) > MaxToolNameLength {
		t.Errorf("alias is %d chars, over the %d limit: %q", len(alias), MaxToolNameLength, alias)
	}
	idx := strings.LastIndexByte(alias, '_')
	if idx < 1 {
		t.Fatalf("alias %q has no _<hash> suffix", alias)
	}
	if hash := alias[idx+1:]; len(hash) != toolNameHashLen || !isHex(hash) {
		t.Errorf("alias %q does not end in _<%d hex chars>: suffix %q", alias, toolNameHashLen, hash)
	}
	if strings.HasPrefix(alias, "tool_") && len(alias) == 55 {
		t.Errorf("alias still uses the old tool_<sha256[:50]> shape: %q", alias)
	}

	// Body must not contain the long name anywhere; it must contain the alias
	// in tools[], tool_choice, and messages[].tool_calls[].
	gotStr := string(got)
	if strings.Contains(gotStr, longName) {
		t.Errorf("expected long name replaced in body, still present: %s", gotStr)
	}
	if count := strings.Count(gotStr, alias); count != 3 {
		t.Errorf("expected alias to appear 3 times (tools, tool_choice, tool_calls), got %d: %s", count, gotStr)
	}
}

// TestFitToolNamesForProvider_AzureVariants checks the whole built-in provider
// set from OmniRoute #20258 — openai, azure, azure-openai, azure-ai — aliases
// identically. azure-ai is the one the maintainers flagged as missing.
func TestFitToolNamesForProvider_AzureVariants(t *testing.T) {
	longName := "mcp__example-observability-prod__trace_top_time_consuming_operations_by_service" // 78 chars
	for _, provider := range []string{"openai", "azure", "azure-openai", "azure-ai"} {
		t.Run(provider, func(t *testing.T) {
			if !UsesToolNameHash(provider) {
				t.Fatalf("provider %q must use hash aliasing", provider)
			}
			body := []byte(`{"tools":[{"type":"function","function":{"name":"` + longName + `"}}]}`)
			_, toolMap := FitToolNamesForProvider(body, provider)
			if len(toolMap) != 1 {
				t.Fatalf("expected 1 mapped tool, got %d: %v", len(toolMap), toolMap)
			}
			want := wantHashAlias(t, longName)
			for alias, orig := range toolMap {
				if alias != want {
					t.Errorf("alias = %q, want %q", alias, want)
				}
				if orig != longName {
					t.Errorf("mapped original = %q, want %q", orig, longName)
				}
				if len(alias) > MaxToolNameLength {
					t.Errorf("alias is %d chars, over the %d limit", len(alias), MaxToolNameLength)
				}
			}
		})
	}
}

// TestUsesToolNameHash pins the provider set against accidental widening: the
// four OpenAI-format providers that declare a 64-char tool name limit alias,
// and every other provider (including sibling gateway ids that only look like
// them) keeps the legacy truncation.
func TestUsesToolNameHash(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{"openai", true},
		{"azure", true},
		{"azure-openai", true},
		{"azure-ai", true},
		{"OpenAI", true},  // case-insensitive: provider ids arrive as stored
		{" azure ", true}, // surrounding space tolerated
		{"azure-foundry", false},
		{"openai-compatible-chat", false},
		{"deepseek", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			if got := UsesToolNameHash(tc.provider); got != tc.want {
				t.Errorf("UsesToolNameHash(%q) = %v, want %v", tc.provider, got, tc.want)
			}
		})
	}
}

// TestFitToolNamesForProvider_InvalidCharsReplaced checks the charset half of
// the upstream contract: a name that fits the length limit but carries a dot,
// a slash, a space or a colon is aliased too, with each invalid char replaced
// by "_".
func TestFitToolNamesForProvider_InvalidCharsReplaced(t *testing.T) {
	cases := []struct {
		name string
		orig string
	}{
		{"dot", "mcp.github.get_issue"},
		{"slash", "mcp/github/get issue"},
		{"space", "get weather forecast"},
		{"colon", "namespace:action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.orig) > MaxToolNameLength {
				t.Fatalf("fixture %q is %d chars; this case is about the charset, not the length", tc.orig, len(tc.orig))
			}
			body := []byte(`{"tools":[{"type":"function","function":{"name":"` + tc.orig + `"}}]}`)
			got, toolMap := FitToolNamesForProvider(body, "openai")
			if len(toolMap) != 1 {
				t.Fatalf("expected invalid-char name %q to be aliased, got map %v", tc.orig, toolMap)
			}
			var alias string
			for a := range toolMap {
				alias = a
			}
			if strings.ContainsAny(alias, "./ :") {
				t.Errorf("alias %q still carries an invalid char", alias)
			}
			if !isOpenAICompatibleToolName(alias) {
				t.Errorf("alias %q violates ^[A-Za-z0-9_-]{1,64}$", alias)
			}
			if alias == tc.orig {
				t.Errorf("alias equals the original invalid name %q", tc.orig)
			}
			if want := wantHashAlias(t, tc.orig); alias != want {
				t.Errorf("alias = %q, want %q", alias, want)
			}
			if strings.Contains(string(got), tc.orig) {
				t.Errorf("original invalid name %q still present in body: %s", tc.orig, got)
			}
		})
	}
}

// TestFitToolNamesForProvider_NonHashProvider_KeepsNamesUntouched verifies that
// providers NOT in the 64-char-limit set (e.g. deepseek) keep the legacy
// behavior: over-length names get the _N truncation, and a short name with an
// invalid char is left exactly as the client sent it.
func TestFitToolNamesForProvider_NonHashProvider_KeepsTruncation(t *testing.T) {
	longName := "mcp__github_server__create_or_update_file_contents_with_commit_details_action"
	body := []byte(`{
		"model": "deepseek-chat",
		"tools": [
			{"type": "function", "function": {"name": "` + longName + `"}},
			{"type": "function", "function": {"name": "mcp.github.get_issue"}}
		]
	}`)
	got, toolMap := FitToolNamesForProvider(body, "deepseek")
	if len(toolMap) != 1 {
		t.Fatalf("expected only the over-length name mapped, got %d: %v", len(toolMap), toolMap)
	}
	for k := range toolMap {
		if !strings.HasSuffix(k, "_1") {
			t.Errorf("expected truncated name with _1 suffix, got %q", k)
		}
		if len(k) > MaxToolNameLength {
			t.Errorf("fitted name still exceeds %d chars: %q", MaxToolNameLength, k)
		}
	}
	if strings.Contains(string(got), "mcp.github.get_issue") == false {
		t.Errorf("deepseek must leave the short invalid-char name untouched, got: %s", got)
	}
}

// TestFitToolNamesForProvider_CollisionResolution checks that two long names
// sharing a 51-char prefix do not collapse onto one alias, that every alias
// stays within 64 chars, and that nothing panics (the original [:60] on a
// 53-char fallback alias did).
func TestFitToolNamesForProvider_CollisionResolution(t *testing.T) {
	// Every name exceeds 64 chars so each one needs an alias, and all share the
	// same first 51 chars so the `${safePrefix}_${hash12}` aliases collide in
	// their prefix and only the hash separates them.
	shared := strings.Repeat("a", 51)
	names := []string{
		shared + "_alpha_variant_that_makes_this_name_long_enough",
		shared + "_beta_variant_that_makes_this_name_long_enough",
		shared + "_gamma_variant_that_makes_this_name_long_enough",
	}
	for _, n := range names {
		if len(n) <= MaxToolNameLength {
			t.Fatalf("fixture %q is %d chars; it must exceed %d to need an alias", n, len(n), MaxToolNameLength)
		}
	}
	for _, provider := range []string{"openai", "azure-ai"} {
		t.Run(provider, func(t *testing.T) {
			req := map[string]any{"tools": []any{}}
			for _, n := range names {
				req["tools"] = append(req["tools"].([]any), map[string]any{
					"type":     "function",
					"function": map[string]any{"name": n},
				})
			}
			shortToOrig := FitToolNamesInMap(req, provider)
			if len(shortToOrig) != len(names) {
				t.Fatalf("expected %d aliases, got %d: %v", len(names), len(shortToOrig), shortToOrig)
			}
			for alias, orig := range shortToOrig {
				if len(alias) > MaxToolNameLength {
					t.Errorf("alias %q is %d chars, over %d", alias, len(alias), MaxToolNameLength)
				}
				if !isOpenAICompatibleToolName(alias) {
					t.Errorf("alias %q violates the tool-name charset", alias)
				}
				if !slices.Contains(names, orig) {
					t.Errorf("alias %q maps to unknown original %q", alias, orig)
				}
			}
		})
	}
}

// TestFitToolNamesForProvider_AliasShadowingShortName covers the collision the
// upstream explicitly rejects: a generated alias equals an already-valid short
// name declared beside the long one. The long name must fall back instead of
// producing two tools with one name.
func TestFitToolNamesForProvider_AliasShadowingShortName(t *testing.T) {
	longName := "mcp__github_server__create_or_update_file_contents_with_commit_details_action"
	shadowing := wantHashAlias(t, longName)
	if len(shadowing) > MaxToolNameLength {
		t.Fatalf("shadow fixture is %d chars, over the limit", len(shadowing))
	}
	body := []byte(`{"tools":[
		{"type":"function","function":{"name":"` + shadowing + `"}},
		{"type":"function","function":{"name":"` + longName + `"}}
	]}`)

	_, toolMap := FitToolNamesForProvider(body, "openai")
	if len(toolMap) != 1 {
		t.Fatalf("expected 1 mapped tool, got %d: %v", len(toolMap), toolMap)
	}
	var alias string
	for a := range toolMap {
		alias = a
	}
	if alias == shadowing {
		t.Errorf("alias %q collides with the short name already in the body", alias)
	}
	if len(alias) > MaxToolNameLength {
		t.Errorf("fallback alias %q is %d chars, over %d", alias, len(alias), MaxToolNameLength)
	}
	if !isOpenAICompatibleToolName(alias) {
		t.Errorf("fallback alias %q violates the tool-name charset", alias)
	}
	// The fallback alias must still round-trip: the upstream answers with it and
	// the client has to see the name it declared.
	resp := []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"` + alias + `"}}]}}]}`)
	restored := RestoreToolNamesInPayload(resp, toolMap)
	if strings.Contains(string(restored), alias) {
		t.Errorf("fallback alias %q leaked to the client: %s", alias, restored)
	}
	if !strings.Contains(string(restored), longName) {
		t.Errorf("original name %q not restored from fallback alias %q: %s", longName, alias, restored)
	}
	// The short name that shadowed the preferred alias must survive untouched.
	if !strings.Contains(string(body), shadowing) {
		t.Errorf("shadowing short name %q was rewritten: %s", shadowing, body)
	}
}

// TestFitToolNamesForProvider_RestoreResponse verifies that
// RestoreToolNamesInPayload restores the original name from the alias.
func TestFitToolNamesForProvider_RestoreResponse(t *testing.T) {
	longName := "mcp__github_server__create_or_update_file_contents_with_commit_details_action"
	body := []byte(`{
		"tools": [{"type": "function", "function": {"name": "` + longName + `"}}]
	}`)
	_, toolMap := FitToolNamesForProvider(body, "openai")
	if len(toolMap) == 0 {
		t.Fatal("expected tool map, got none")
	}

	// Build a fake response that uses the alias.
	var alias string
	for k := range toolMap {
		alias = k
	}
	resp := []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"` + alias + `","arguments":"{}"}}]}}]}`)

	restored := RestoreToolNamesInPayload(resp, toolMap)
	if strings.Contains(string(restored), alias) {
		t.Errorf("expected alias replaced by original name, still present: %s", string(restored))
	}
	if !strings.Contains(string(restored), longName) {
		t.Errorf("expected original name %q in restored payload: %s", longName, string(restored))
	}
}

// isHex reports whether s is non-empty lowercase hex.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

// TestFitToolNamesForProvider_NoToolsUnchanged verifies that a body without
// tool names is returned unchanged with a nil map, regardless of provider.
func TestFitToolNamesForProvider_NoToolsUnchanged(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	for _, provider := range []string{"openai", "azure", "azure-openai", "azure-ai", "deepseek"} {
		got, toolMap := FitToolNamesForProvider(body, provider)
		if string(got) != string(body) {
			t.Errorf("provider %s: expected body unchanged, got: %s", provider, got)
		}
		if len(toolMap) != 0 {
			t.Errorf("provider %s: expected empty toolMap, got: %v", provider, toolMap)
		}
	}
}
