package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// Tests for the tool-name conceal/restore pair that satisfies the OpenCode
// free-tier gate. The regression being guarded: sending "Bash" together with
// "bash" makes the upstream reject the request with HTTP 500 (duplicates), while
// "Bash" without the lowercase form is rejected with 403.

func toolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, _ := m["tools"].([]any)
	names := make([]string, 0, len(raw))
	for _, tool := range raw {
		names = append(names, toolNameOf(tool))
	}
	return names
}

func TestConcealFingerprintToolsNoDuplicates(t *testing.T) {
	// The Claude Code CLI case: capitalised quartet members.
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","tools":[
		{"type":"function","name":"Task"},
		{"type":"function","name":"Bash"},
		{"type":"function","name":"Glob"},
		{"type":"function","name":"Grep"},
		{"type":"function","name":"Read"},
		{"type":"function","name":"Edit"}
	]}`)

	out, nameMap := ConcealFingerprintTools(body)
	names := toolNames(t, out)

	seen := map[string]int{}
	for _, n := range names {
		seen[strings.ToLower(n)]++
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("tool name %q appears %d times (duplicates trigger HTTP 500 upstream)", n, c)
		}
	}
	if len(nameMap) != 4 {
		t.Errorf("expected 4 concealed names, got %d", len(nameMap))
	}
	if nameMap["bash"] != "Bash" {
		t.Errorf("map bash -> %q, want \"Bash\"", nameMap["bash"])
	}
	hasLower := false
	for _, n := range names {
		if n == "bash" {
			hasLower = true
		}
		if n == "Bash" {
			t.Errorf("capitalised name still sent in capitalised form")
		}
	}
	if !hasLower {
		t.Error("lowercase quartet missing — upstream will reject with 403")
	}
	// Tools outside the quartet must be left alone.
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "Task") || !strings.Contains(joined, "Edit") {
		t.Errorf("non-quartet tools were modified: %v", names)
	}
}

func TestConcealFingerprintToolsDropsPlainDuplicates(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash"},{"name":"bash"},{"name":"Read"},{"name":"read"}]}`)
	out, _ := ConcealFingerprintTools(body)
	names := toolNames(t, out)
	// bash and read are already declared after collapsing, so nothing is added.
	if len(names) != 2 {
		t.Errorf("want 2 unique tools (bash, read), got %d: %v", len(names), names)
	}
}

func TestConcealFingerprintToolsInjectsWhenNoTools(t *testing.T) {
	// A tool-less request is also rejected upstream (403), so the required tool
	// is added. Only "read" is required — the wider quartet is not injected.
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","input":[]}`)
	out, _ := ConcealFingerprintTools(body)
	names := toolNames(t, out)
	if len(names) != 1 || names[0] != "read" {
		t.Fatalf(`want exactly one injected "read" tool, got %d: %v`, len(names), names)
	}
}

func TestConcealFingerprintToolsChatShape(t *testing.T) {
	// The chat shape nests the name inside "function".
	body := []byte(`{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"terminal"}}]}`)
	out, nameMap := ConcealFingerprintTools(body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, _ := m["tools"].([]any)
	foundLower := false
	for _, tool := range raw {
		tm, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tm["function"].(map[string]any)
		if !ok {
			t.Errorf("the .function wrapper was lost during concealment")
			continue
		}
		if fn["name"] == "bash" {
			foundLower = true
		}
	}
	if !foundLower {
		t.Error("name inside .function was not renamed to lowercase")
	}
	if nameMap["bash"] != "Bash" {
		t.Errorf("map bash -> %q, want \"Bash\"", nameMap["bash"])
	}
}

func TestConcealFingerprintToolsRetargetsToolChoice(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash"}],"tool_choice":{"type":"tool","name":"Bash"}}`)
	out, _ := ConcealFingerprintTools(body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choice, _ := m["tool_choice"].(map[string]any)
	if choice["name"] != "bash" {
		t.Errorf("tool_choice.name = %v, want \"bash\"", choice["name"])
	}
}

// A forced Chat Completions choice nests the name under "function"; it has to
// follow the rename too, or upstream is asked to force a tool the request no
// longer declares.
func TestConcealFingerprintToolsRetargetsNestedChatToolChoice(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"Bash"}}],"tool_choice":{"type":"function","function":{"name":"Bash"}}}`)
	out, nameMap := ConcealFingerprintTools(body)
	if nameMap["bash"] != "Bash" {
		t.Fatalf("expected Bash -> bash rename, got %v", nameMap)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choice, _ := m["tool_choice"].(map[string]any)
	fn, _ := choice["function"].(map[string]any)
	if fn["name"] != "bash" {
		t.Errorf("tool_choice.function.name = %v, want \"bash\"", fn["name"])
	}
}

// Prior-turn tool calls naming the caller's original spelling have to be
// retargeted across every wire shape, so the conversation stays self-consistent
// once the tools array declares the canonical name.
func TestConcealFingerprintToolsRetargetsToolHistory(t *testing.T) {
	t.Run("chat tool_calls", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}],"tools":[{"type":"function","function":{"name":"Read"}}]}`)
		out, _ := ConcealFingerprintTools(body)
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		msg := m["messages"].([]any)[0].(map[string]any)
		call := msg["tool_calls"].([]any)[0].(map[string]any)
		if got := call["function"].(map[string]any)["name"]; got != "read" {
			t.Errorf("history tool call name = %v, want \"read\"", got)
		}
	})
	t.Run("anthropic tool_use", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Grep","input":{}}]}],"tools":[{"name":"Grep"}]}`)
		out, _ := ConcealFingerprintTools(body)
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		msg := m["messages"].([]any)[0].(map[string]any)
		block := msg["content"].([]any)[0].(map[string]any)
		if block["name"] != "grep" {
			t.Errorf("tool_use block name = %v, want \"grep\"", block["name"])
		}
	})
	t.Run("responses function_call", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"function_call","call_id":"fc_1","name":"Glob","arguments":"{}"}],"tools":[{"type":"function","name":"Glob"}]}`)
		out, _ := ConcealFingerprintTools(body)
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		item := m["input"].([]any)[0].(map[string]any)
		if item["name"] != "glob" {
			t.Errorf("function_call item name = %v, want \"glob\"", item["name"])
		}
	})
}

// A forced choice naming a tool whose declaration is a reviewed duplicate
// (dropped in favour of the canonical one) cannot be retargeted reliably, as
// the rename could belong to either spelling; it is dropped rather than
// forwarded dangling.
//
// Body order matters: the exact "read" is seen first, so the later "Read" is
// dropped as a duplicate and never recorded in the rename map.
func TestConcealFingerprintToolsDropsUnretargetableChoice(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"read"},{"type":"function","name":"Read"}],"tool_choice":{"type":"tool","name":"Read"}}`)
	out, nameMap := ConcealFingerprintTools(body)
	if original, ok := nameMap["read"]; !ok || original != "" {
		t.Fatalf("the dropped duplicate must be recorded as unresolvable (\"\"), got %v", nameMap)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["tool_choice"]; present {
		t.Errorf("dangling tool_choice survived: %v", m["tool_choice"])
	}
}

// An exact lowercase "read" is what the gate fingerprints; a client that
// already declares it must keep its own definition untouched (no rename, no
// response-side restoration) so downstream function calling is unaffected.
func TestConcealFingerprintToolsPreservesExactReadDefinition(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"read","description":"client read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
	out, nameMap := ConcealFingerprintTools(body)
	if len(nameMap) != 0 {
		t.Errorf("exact read must not be renamed, got %v", nameMap)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, raw := range m["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["name"] != "read" {
			continue
		}
		if tool["description"] != "client read" {
			t.Errorf("client read definition was rewritten: %v", tool["description"])
		}
	}
}

func TestConcealFingerprintToolsAlreadyCanonical(t *testing.T) {
	// A body that already carries the lowercase quartet needs no rename and no
	// extra declarations.
	body := []byte(`{"tools":[{"name":"terminal"},{"name":"bash"},{"name":"glob"},{"name":"grep"},{"name":"read"}]}`)
	out, nameMap := ConcealFingerprintTools(body)
	if len(nameMap) != 0 {
		t.Errorf("map should be empty, got %v", nameMap)
	}
	names := toolNames(t, out)
	if len(names) != 5 {
		t.Errorf("no tools should be added, want 5, got %d: %v", len(names), names)
	}
	seen := map[string]int{}
	for _, n := range names {
		seen[strings.ToLower(n)]++
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("name %q is duplicated", n)
		}
	}
}

func TestConcealFingerprintToolsInjectsWhenQuartetAbsent(t *testing.T) {
	// A client sending other tools without the required one still needs "read"
	// injected (upstream returns 403 without it), and caller tools must survive
	// untouched. The rest of the quartet is NOT injected: the gate does not ask
	// for it, so adding it would only offer the model tools that do nothing.
	body := []byte(`{"tools":[{"name":"terminal"},{"name":"read_file"}]}`)
	out, nameMap := ConcealFingerprintTools(body)
	if len(nameMap) != 0 {
		t.Errorf("nothing was capitalised, map should be empty: %v", nameMap)
	}
	names := toolNames(t, out)
	if len(names) != 3 {
		t.Errorf(`want terminal, read_file and one injected "read", got %d: %v`, len(names), names)
	}
	injected := 0
	for _, n := range names {
		if n == "read" {
			injected++
		}
	}
	if injected != 1 {
		t.Errorf(`want exactly one "read", got %d: %v`, injected, names)
	}
	for _, unneeded := range []string{"bash", "glob", "grep"} {
		for _, n := range names {
			if n == unneeded {
				t.Errorf("%q must not be injected — the gate does not require it: %v", unneeded, names)
			}
		}
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "terminal") || !strings.Contains(joined, "read_file") {
		t.Errorf("caller tools disappeared: %v", names)
	}
}

func TestRestoreToolNamesClaudeContentBlockStart(t *testing.T) {
	nameMap := map[string]string{"bash": "Bash", "read": "Read"}
	chunk := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}}`)
	out := RestoreToolNames(chunk, nameMap)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	block := m["content_block"].(map[string]any)
	if block["name"] != "Bash" {
		t.Errorf("name = %v, want \"Bash\"", block["name"])
	}
}

func TestRestoreToolNamesStreamingDeltaToolCalls(t *testing.T) {
	nameMap := map[string]string{"bash": "Bash"}
	chunk := []byte(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"bash","arguments":""}}]}}]}`)
	out := RestoreToolNames(chunk, nameMap)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices := m["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	calls := delta["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "Bash" {
		t.Errorf("name = %v, want \"Bash\"", fn["name"])
	}
}

func TestRestoreToolNamesNonStreamMessageToolCalls(t *testing.T) {
	nameMap := map[string]string{"grep": "Grep"}
	body := []byte(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"grep","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	out := RestoreToolNames(body, nameMap)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices := m["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "Grep" {
		t.Errorf("name = %v, want \"Grep\"", fn["name"])
	}
}

func TestRestoreToolNamesResponsesOutput(t *testing.T) {
	nameMap := map[string]string{"read": "Read"}
	body := []byte(`{"id":"resp_1","output":[{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"}]}`)
	out := RestoreToolNames(body, nameMap)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	output := m["output"].([]any)
	item := output[0].(map[string]any)
	if item["name"] != "Read" {
		t.Errorf("name = %v, want \"Read\"", item["name"])
	}
}

func TestRestoreToolNamesNoOpWithoutMap(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"bash"}}]}}]}`)
	if out := RestoreToolNames(body, nil); string(out) != string(body) {
		t.Error("without a map the body must be returned unchanged")
	}
	if out := RestoreToolNames(body, map[string]string{}); string(out) != string(body) {
		t.Error("with an empty map the body must be returned unchanged")
	}
}

// A "" entry marks a caller spelling whose declaration was dropped as a
// duplicate: the response must keep the canonical name rather than blank it.
func TestRestoreToolNamesIgnoresDroppedDuplicateMarker(t *testing.T) {
	body := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"read"}}]}}]}`)
	nameMap := map[string]string{"read": ""}
	if out := RestoreToolNames(body, nameMap); string(out) != string(body) {
		t.Errorf("a \"\" marker must not rewrite the frame, got %s", out)
	}
}

func TestRestoreToolNamesInSSEKeepsFraming(t *testing.T) {
	nameMap := map[string]string{"bash": "Bash"}
	chunk := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"bash\"}}\n\n")
	out := string(RestoreToolNamesInSSE([]byte(chunk), nameMap))
	if !strings.Contains(out, "event: content_block_start") {
		t.Error("the 'event:' line was dropped")
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("name was not restored: %s", out)
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Error("the SSE frame separator was dropped")
	}
}

func TestRestoreToolNamesInSSELeavesDoneAlone(t *testing.T) {
	nameMap := map[string]string{"bash": "Bash"}
	chunk := []byte("data: [DONE]\n\n")
	if out := RestoreToolNamesInSSE(chunk, nameMap); string(out) != string(chunk) {
		t.Errorf("[DONE] must pass through unchanged, got %q", string(out))
	}
}

func TestRestoreToolNamesInPayloadPicksShape(t *testing.T) {
	nameMap := map[string]string{"bash": "Bash"}

	// Non-streaming JSON body
	jsonBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"bash"}}]}}]}`)
	if out := string(RestoreToolNamesInPayload(jsonBody, nameMap)); !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("JSON path failed: %s", out)
	}

	// SSE frame
	sse := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"bash\"}}]}}]}\n\n")
	if out := string(RestoreToolNamesInPayload(sse, nameMap)); !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("SSE path failed: %s", out)
	}
}

func TestFingerprintToolKey(t *testing.T) {
	cases := map[string]string{
		"Bash": "bash", " bash ": "bash", "GLOB": "glob",
		"Read": "read", "Grep": "grep",
		"Edit": "", "terminal": "", "": "",
	}
	for in, want := range cases {
		if got := FingerprintToolKey(in); got != want {
			t.Errorf("FingerprintToolKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolNameMapContext(t *testing.T) {
	nameMap := map[string]string{"bash": "Bash"}
	ctx := WithToolNameMap(nil, nameMap)
	if got := ToolNameMapFromContext(ctx); got["bash"] != "Bash" {
		t.Errorf("map was not stored on the context: %v", got)
	}
	if got := ToolNameMapFromContext(nil); got != nil {
		t.Errorf("a nil context must yield a nil map, got %v", got)
	}
}

func TestConcealFingerprintToolsChatShapeWhenNoTools(t *testing.T) {
	body := []byte(`{"model":"space-bunny-free","messages":[{"role":"user","content":"hi"}]}`)
	out, _ := ConcealFingerprintTools(body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("want the single required tool, got %v", tools)
	}
	for _, tool := range tools {
		tm, ok := tool.(map[string]any)
		if !ok {
			t.Fatalf("expected map tool, got %v", tool)
		}
		fn, ok := tm["function"].(map[string]any)
		if !ok {
			t.Fatalf("chat shape must nest name inside 'function', got %v", tm)
		}
		if fn["name"] != "read" {
			t.Errorf(`want "read", got %v`, fn["name"])
		}
	}
	if m["tool_choice"] != "none" {
		t.Errorf("expected tool_choice 'none' for tool-less chat request, got %v", m["tool_choice"])
	}
}
