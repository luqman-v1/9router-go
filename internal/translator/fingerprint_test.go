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
	if len(names) != 4 {
		t.Errorf("want 4 unique tools (bash,read plus injected glob,grep), got %d: %v", len(names), names)
	}
}

func TestConcealFingerprintToolsInjectsWhenNoTools(t *testing.T) {
	// A tool-less request is also rejected upstream (403), so the quartet is added.
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","input":[]}`)
	out, _ := ConcealFingerprintTools(body)
	names := toolNames(t, out)
	if len(names) != 4 {
		t.Fatalf("want 4 injected tools, got %d: %v", len(names), names)
	}
	for _, n := range names {
		if n != strings.ToLower(n) {
			t.Errorf("injected tool must be lowercase, got %q", n)
		}
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
	// A client sending other tools without the quartet still needs it injected
	// (upstream returns 403 without those lowercase names), and caller tools must
	// survive untouched.
	body := []byte(`{"tools":[{"name":"terminal"},{"name":"read_file"}]}`)
	out, nameMap := ConcealFingerprintTools(body)
	if len(nameMap) != 0 {
		t.Errorf("nothing was capitalised, map should be empty: %v", nameMap)
	}
	names := toolNames(t, out)
	for _, q := range OpenCodeFingerprintTools {
		found := false
		for _, n := range names {
			if n == q {
				found = true
			}
		}
		if !found {
			t.Errorf("quartet member %q was not injected: %v", q, names)
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
