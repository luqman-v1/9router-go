package tokensaver

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
)

// ----- helpers -----

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain substr %q", s, substr)
	}
}

// ----- test data generators -----

func longGitDiff() string {
	var b strings.Builder
	b.WriteString("diff --git a/file.go b/file.go\n")
	for i := 0; i < GitDiffMaxLines+10; i++ {
		b.WriteString("@@ -1 +1 @@ context\n")
	}
	return b.String()
}

func longGitLog() string {
	var b strings.Builder
	for i := 0; i < GitLogMaxLines+10; i++ {
		fmt.Fprintf(&b, "abc%04d some commit message\n", i)
	}
	return b.String()
}

func longGrepOutput() string {
	var b strings.Builder
	total := GrepPerFileMax*20 + 50
	for i := 0; i < total; i++ {
		fmt.Fprintf(&b, "file%d.go:%d: content\n", i%5, i)
	}
	return b.String()
}

func longTreeOutput() string {
	var b strings.Builder
	for i := 0; i < TreeMaxLines+10; i++ {
		b.WriteString("├── some_file.go\n")
	}
	return b.String()
}

func longGenericText() string {
	var b strings.Builder
	for i := 0; i < SmartTruncMin+10; i++ {
		b.WriteString("plain line of text without any special pattern\n")
	}
	return b.String()
}

// ============================================================
// CompressMessages
// ============================================================

func TestCompressMessages_InvalidJSON(t *testing.T) {
	in := []byte("{invalid")
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false for invalid JSON")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_NoMessagesKey(t *testing.T) {
	in := []byte(`{"model":"gpt-4"}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false when no messages/input key")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_EmptyArray(t *testing.T) {
	in := []byte(`{"messages":[]}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false for empty array")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_NonArrayValue(t *testing.T) {
	in := []byte(`{"messages":"string"}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false for non-array messages")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_NonMapItem(t *testing.T) {
	in := []byte(`{"messages":["string"]}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false when item is not a map")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_FunctionCallOutputShort(t *testing.T) {
	in := []byte(`{"messages":[{"type":"function_call_output","output":"short"}]}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false for short function_call_output")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_FunctionCallOutputLong(t *testing.T) {
	diff := longGitDiff()
	msg := map[string]any{"messages": []any{map[string]any{"type": "function_call_output", "output": diff}}}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if !ok {
		t.Fatal("expected true for long function_call_output")
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal("invalid JSON output:", err)
	}
	val := res["messages"].([]any)[0].(map[string]any)["output"].(string)
	assertContains(t, val, "... (")
}

func TestCompressMessages_ToolRoleShort(t *testing.T) {
	in := []byte(`{"messages":[{"role":"tool","content":"short"}]}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false for short tool content")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_ToolRoleLong(t *testing.T) {
	diff := longGitDiff()
	msg := map[string]any{"messages": []any{map[string]any{"role": "tool", "content": diff}}}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if !ok {
		t.Fatal("expected true for long tool content")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	val := res["messages"].([]any)[0].(map[string]any)["content"].(string)
	assertContains(t, val, "... (")
}

func TestCompressMessages_ToolResultShort(t *testing.T) {
	in := []byte(`{"messages":[{"content":[{"type":"tool_result","text":"short"}]}]}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false for short tool_result")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_ToolResultLong(t *testing.T) {
	diff := longGitDiff()
	msg := map[string]any{
		"messages": []any{
			map[string]any{
				"content": []any{
					map[string]any{"type": "tool_result", "text": diff},
				},
			},
		},
	}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if !ok {
		t.Fatal("expected true for long tool_result")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	arr := res["messages"].([]any)[0].(map[string]any)["content"].([]any)
	val := arr[0].(map[string]any)["text"].(string)
	assertContains(t, val, "... (")
}

func TestCompressMessages_TextBlockIsNotCompressed(t *testing.T) {
	diff := longGitDiff()
	msg := map[string]any{
		"messages": []any{
			map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": diff},
				},
			},
		},
	}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if ok {
		t.Fatal("expected false for text block: prose/conversation text must not be compressed")
	}
	if string(out) != string(in) {
		t.Fatal("expected body to remain unchanged for text block")
	}
}

func TestCompressMessages_InputKey(t *testing.T) {
	diff := longGitDiff()
	msg := map[string]any{"input": []any{map[string]any{"type": "function_call_output", "output": diff}}}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if !ok {
		t.Fatal("expected true when using input key")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	if _, has := res["messages"]; has {
		t.Error("unexpected messages key in output")
	}
	val := res["input"].([]any)[0].(map[string]any)["output"].(string)
	assertContains(t, val, "... (")
}

func TestCompressMessages_MessagesPrecedenceOverInput(t *testing.T) {
	diff := longGitDiff()
	msg := map[string]any{
		"messages": []any{
			map[string]any{"type": "function_call_output", "output": diff},
		},
		"input": []any{
			map[string]any{"type": "function_call_output", "output": "uncached"},
		},
	}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	// messages key was processed
	val := res["messages"].([]any)[0].(map[string]any)["output"].(string)
	assertContains(t, val, "... (")
}

func TestCompressMessages_MultipleMessagesAllCompressed(t *testing.T) {
	diff := longGitDiff()
	msg := map[string]any{
		"messages": []any{
			map[string]any{"type": "function_call_output", "output": diff},
			map[string]any{"type": "function_call_output", "output": diff},
		},
	}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	arr := res["messages"].([]any)
	for _, item := range arr {
		val := item.(map[string]any)["output"].(string)
		assertContains(t, val, "... (")
	}
}

func TestCompressMessages_ContentArrayNotMapSkip(t *testing.T) {
	in := []byte(`{"messages":[{"content":["notamap"]}]}`)
	out, ok := CompressMessages(in)
	if ok {
		t.Error("expected false when content array item is not a map")
	}
	if string(out) != string(in) {
		t.Error("expected original body returned")
	}
}

func TestCompressMessages_InputOnlyMessagesFallback(t *testing.T) {
	diff := longGitDiff()
	// messages key missing, input key used
	msg := map[string]any{
		"input": []any{
			map[string]any{
				"content": []any{
					map[string]any{"type": "tool_result", "text": diff},
				},
			},
		},
	}
	in, _ := json.Marshal(msg)
	out, ok := CompressMessages(in)
	if !ok {
		t.Fatal("expected true when using input fallback")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	arr := res["input"].([]any)[0].(map[string]any)["content"].([]any)
	val := arr[0].(map[string]any)["text"].(string)
	assertContains(t, val, "... (")
}

// ============================================================
// CompressText
// ============================================================

func TestCompressText_ShortText(t *testing.T) {
	in := "short text"
	out := CompressText(in)
	if out != in {
		t.Error("expected short text returned as-is")
	}
}

func TestCompressText_GitDiff(t *testing.T) {
	in := longGitDiff()
	out := CompressText(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "more lines")
}

func TestCompressText_GitLog(t *testing.T) {
	in := longGitLog()
	out := CompressText(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "more commits")
}

func TestCompressText_GrepOutput(t *testing.T) {
	in := longGrepOutput()
	out := CompressText(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "matches filtered")
}

func TestCompressText_TreeOutput(t *testing.T) {
	in := longTreeOutput()
	out := CompressText(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "more entries")
}

func TestCompressText_SmartTruncate(t *testing.T) {
	in := longGenericText()
	out := CompressText(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "lines truncated")
}

func TestCompressText_TrimWhitespace(t *testing.T) {
	text := longGenericText()
	in := "  \n  " + text + "  \n  "
	out := CompressText(in)
	// trimming happens, then smartTruncate runs on trimmed version
	if out == in {
		t.Error("expected output to differ from untrimmed input")
	}
	assertContains(t, out, "... (")
}

// ============================================================
// isGitDiff
// ============================================================

func TestIsGitDiff_Match(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"diff_git_prefix", "diff --git a/file.go b/file.go"},
		{"diff_git_longer", "diff --git a/src/main.go b/src/main.go\n+func new() {}"},
		{"ataan_a_prefix", "--- a/file.go"},
		{"ataan_a_longer", "--- a/src/main.go\n+++ b/src/main.go"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !isGitDiff(tc.input) {
				t.Errorf("isGitDiff(%q) = false, want true", tc.input)
			}
		})
	}
}

func TestIsGitDiff_NoMatch(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"random_text", "hello world"},
		{"empty_string", ""},
		{"diff_not_git", "diff --notgit a/file"},
		{"ataan_b_prefix", "--- b/file.go"},
		{"minus_char_only", "--- c/file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if isGitDiff(tc.input) {
				t.Errorf("isGitDiff(%q) = true, want false", tc.input)
			}
		})
	}
}

// ============================================================
// isGitLog
// ============================================================

func TestIsGitLog_Match(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"seven_hex_with_space", "abc1234 commit message"},
		{"forty_hex_with_space", "abcdef0123456789abcdef0123456789abcdef01 message"},
		{"commit_prefix", "commit abcdef1234567890"},
		{"commit_prefix_short_hash", "commit abc1234"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !isGitLog(tc.input) {
				t.Errorf("isGitLog(%q) = false, want true", tc.input)
			}
		})
	}
}

func TestIsGitLog_NoMatch(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"random_text", "hello world"},
		{"empty_string", ""},
		{"six_hex_only", "abc123 no space suffix"},
		{"hex_without_space", "abcdefg_no_space"},
		{"commit_no_hash", "commit "},
		{"hex_dash_no_space", "abc1234-message"},
		{"commit_in_middle", "prefix commit abcdef"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if isGitLog(tc.input) {
				t.Errorf("isGitLog(%q) = true, want false", tc.input)
			}
		})
	}
}

// ============================================================
// isGrepOutput
// ============================================================

func TestIsGrepOutput_TooFewLines(t *testing.T) {
	if isGrepOutput("single line without colon") {
		t.Error("expected false for single line")
	}
}

func TestIsGrepOutput_NoMatch(t *testing.T) {
	if isGrepOutput("line one\nline two\nline three") {
		t.Error("expected false for lines without colons")
	}
}

func TestIsGrepOutput_Match(t *testing.T) {
	if !isGrepOutput("file.go:1: content\nfile.go:2: content\nfile.go:3: content") {
		t.Error("expected true for file:line pattern")
	}
}

// ============================================================
// isTreeOutput
// ============================================================

func TestIsTreeOutput_MatchBoxDraw(t *testing.T) {
	if !isTreeOutput("├── src") {
		t.Error("expected true for ├──")
	}
}

func TestIsTreeOutput_MatchCornerDraw(t *testing.T) {
	if !isTreeOutput("└── src") {
		t.Error("expected true for └──")
	}
}

func TestIsTreeOutput_NoMatch(t *testing.T) {
	if isTreeOutput("plain directory listing") {
		t.Error("expected false for plain text")
	}
}

// ============================================================
// compressGitDiff
// ============================================================

func TestCompressGitDiff_UnderLimit(t *testing.T) {
	in := "diff --git a/f b/f\n+line1\n+line2"
	out := compressGitDiff(in)
	if out != in {
		t.Error("expected unchanged when under limit")
	}
}

func TestCompressGitDiff_OverLimit(t *testing.T) {
	in := longGitDiff()
	out := compressGitDiff(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "more lines")
	if len(out) >= len(in) {
		t.Error("expected compressed output shorter than input")
	}
}

// ============================================================
// compressGitLog
// ============================================================

func TestCompressGitLog_UnderLimit(t *testing.T) {
	in := "abc1234 message"
	out := compressGitLog(in)
	if out != in {
		t.Error("expected unchanged when under limit")
	}
}

func TestCompressGitLog_OverLimit(t *testing.T) {
	in := longGitLog()
	out := compressGitLog(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "more commits")
	if len(out) >= len(in) {
		t.Error("expected compressed output shorter than input")
	}
}

// ============================================================
// compressGrep
// ============================================================

func TestCompressGrep_UnderLimit(t *testing.T) {
	in := "file.go:1: content\nfile.go:2: content"
	out := compressGrep(in)
	if out != in {
		t.Error("expected unchanged when under limit")
	}
}

func TestCompressGrep_OverLimit(t *testing.T) {
	in := longGrepOutput()
	out := compressGrep(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "matches filtered")
	if len(out) >= len(in) {
		t.Error("expected compressed output shorter than input")
	}
}

func TestCompressGrep_PerFileLimit(t *testing.T) {
	// Generate lines all from same file to verify per-file limit
	var b strings.Builder
	for i := 0; i < GrepPerFileMax*20+5; i++ {
		fmt.Fprintf(&b, "samefile.go:%d: content\n", i)
	}
	in := b.String()
	out := compressGrep(in)
	// Should have exactly GrepPerFileMax lines from samefile.go + 1 summary
	lines := strings.Split(out, "\n")
	if len(lines) > GrepPerFileMax+2 {
		t.Errorf("expected <= %d lines from single file, got %d", GrepPerFileMax+1, len(lines))
	}
}

func TestCompressGrep_NoFilteringNeeded(t *testing.T) {
	// Only 5 files with GrepPerFileMax lines each = 50 lines total, under limit of 200
	var b strings.Builder
	for f := 0; f < 5; f++ {
		for i := 0; i < GrepPerFileMax; i++ {
			fmt.Fprintf(&b, "file%d.go:%d: content\n", f, i)
		}
	}
	in := b.String()
	out := compressGrep(in)
	if out != in {
		t.Error("expected unchanged when total under limit")
	}
}

// ============================================================
// compressTree
// ============================================================

func TestCompressTree_UnderLimit(t *testing.T) {
	in := "├── file1\n└── file2"
	out := compressTree(in)
	if out != in {
		t.Error("expected unchanged when under limit")
	}
}

func TestCompressTree_OverLimit(t *testing.T) {
	in := longTreeOutput()
	out := compressTree(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "more entries")
	if len(out) >= len(in) {
		t.Error("expected compressed output shorter than input")
	}
}

// ============================================================
// smartTruncate
// ============================================================

func TestSmartTruncate_UnderMin(t *testing.T) {
	in := "line1\nline2\nline3"
	out := smartTruncate(in)
	if out != in {
		t.Error("expected unchanged when under min lines")
	}
}

func TestSmartTruncate_OverMin(t *testing.T) {
	in := longGenericText()
	out := smartTruncate(in)
	assertContains(t, out, "... (")
	assertContains(t, out, "lines truncated")
	if len(out) >= len(in) {
		t.Error("expected truncated output shorter than input")
	}
}

func TestSmartTruncate_HeadTailStructure(t *testing.T) {
	// Verify head content preserved and tail content preserved
	in := longGenericText()
	out := smartTruncate(in)
	lines := strings.Split(out, "\n")
	// Should have head (120) + summary (1) + tail (60) = 181 lines
	expected := SmartTruncHead + 1 + SmartTruncTail
	if len(lines) != expected {
		t.Errorf("expected %d lines, got %d", expected, len(lines))
	}
	// Verify summary line contains correct count
	inLines := strings.Split(in, "\n")
	truncated := len(inLines) - SmartTruncHead - SmartTruncTail
	expectedSummary := fmt.Sprintf("... (%d lines truncated)", truncated)
	if !strings.Contains(out, expectedSummary) {
		t.Errorf("expected summary %q in output", expectedSummary)
	}
}

// ============================================================
// itoa
// ============================================================

func TestItoa(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{-1, "-1"},
		{42, "42"},
		{100, "100"},
		{999999, "999999"},
		{-999999, "-999999"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d", tc.input), func(t *testing.T) {
			got := itoa(tc.input)
			if got != tc.want {
				t.Errorf("itoa(%d) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ============================================================
// InjectSystemPrompt
// ============================================================

func TestInjectSystemPrompt_InvalidJSON(t *testing.T) {
	in := []byte("{invalid")
	out, ok := InjectSystemPrompt(in, "prompt")
	if ok {
		t.Error("expected false for invalid JSON")
	}
	if string(out) != string(in) {
		t.Error("expected original body")
	}
}

func TestInjectSystemPrompt_NoRelevantKeys(t *testing.T) {
	in := []byte(`{}`)
	out, ok := InjectSystemPrompt(in, "prompt")
	if ok {
		t.Error("expected false when no relevant keys")
	}
	if string(out) != string(in) {
		t.Error("expected original body")
	}
}

func TestInjectSystemPrompt_MessagesAndInputBothMissing(t *testing.T) {
	in := []byte(`{"model":"gpt-4"}`)
	out, ok := InjectSystemPrompt(in, "prompt")
	if ok {
		t.Error("expected false")
	}
	if string(out) != string(in) {
		t.Error("expected original body")
	}
}

func TestInjectSystemPrompt_InstructionsNonEmpty(t *testing.T) {
	prompt := "custom prompt"
	in := []byte(`{"instructions":"original instructions"}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true for instructions field")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	got, _ := res["instructions"].(string)
	want := "original instructions\n\n" + prompt
	if got != want {
		t.Errorf("instructions = %q, want %q", got, want)
	}
}

func TestInjectSystemPrompt_InstructionsEmpty(t *testing.T) {
	prompt := "new prompt"
	in := []byte(`{"instructions":""}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true for empty instructions")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	got, _ := res["instructions"].(string)
	if got != prompt {
		t.Errorf("instructions = %q, want %q", got, prompt)
	}
}

func TestInjectSystemPrompt_InstructionsKeyPrecedesMessages(t *testing.T) {
	in := []byte(`{"instructions":"old","messages":[{"role":"system","content":"sys"}]}`)
	out, ok := InjectSystemPrompt(in, "prompt")
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	// instructions should be modified, messages should be unchanged
	inst, _ := res["instructions"].(string)
	if !strings.Contains(inst, "prompt") {
		t.Error("expected instructions to contain appended prompt")
	}
	msgs := res["messages"].([]any)
	content, _ := msgs[0].(map[string]any)["content"].(string)
	if content != "sys" {
		t.Errorf("expected messages content unchanged, got %q", content)
	}
}

func TestInjectSystemPrompt_SystemRoleAppend(t *testing.T) {
	prompt := "extra instructions"
	in := []byte(`{"messages":[{"role":"system","content":"original system message"}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	content, _ := res["messages"].([]any)[0].(map[string]any)["content"].(string)
	want := "original system message\n\n" + prompt
	if content != want {
		t.Errorf("system content = %q, want %q", content, want)
	}
}

func TestInjectSystemPrompt_DeveloperRoleAppend(t *testing.T) {
	prompt := "extra instructions"
	in := []byte(`{"messages":[{"role":"developer","content":"original dev message"}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	content, _ := res["messages"].([]any)[0].(map[string]any)["content"].(string)
	want := "original dev message\n\n" + prompt
	if content != want {
		t.Errorf("developer content = %q, want %q", content, want)
	}
}

func TestInjectSystemPrompt_SystemRoleEmptyContent(t *testing.T) {
	prompt := "replacement prompt"
	in := []byte(`{"messages":[{"role":"system","content":""}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	content, _ := res["messages"].([]any)[0].(map[string]any)["content"].(string)
	if content != prompt {
		t.Errorf("system content = %q, want %q", content, prompt)
	}
}

// A system turn whose content is already a block array must keep every block.
// The old injector read the content as a string, missed, and replaced the whole
// array with the prompt — silently deleting the client's own instructions.
func TestInjectSystemPrompt_SystemRoleArrayContentIsPreserved(t *testing.T) {
	prompt := "extra instructions"
	in := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"original instructions"},{"type":"text","text":"second block"}]}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	blocks, isArr := res["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if !isArr {
		t.Fatalf("content is no longer a block array: %T", res["messages"].([]any)[0].(map[string]any)["content"])
	}
	if len(blocks) != 3 {
		t.Fatalf("block count = %d, want 3 (two original plus the prompt)", len(blocks))
	}
	first := blocks[0].(map[string]any)["text"]
	if first != "original instructions" {
		t.Errorf("first block text = %v, want %q", first, "original instructions")
	}
	second := blocks[1].(map[string]any)["text"]
	if second != "second block" {
		t.Errorf("second block text = %v, want %q", second, "second block")
	}
	last := blocks[2].(map[string]any)
	if last["text"] != prompt || last["type"] != "text" {
		t.Errorf("appended block = %v, want type text with %q", last, prompt)
	}
}

// An array-shaped system turn that already carries the prompt is left alone.
func TestInjectSystemPrompt_SystemRoleArrayContentAlreadyCarries(t *testing.T) {
	prompt := "extra instructions"
	in := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"has extra instructions inside"}]}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if ok {
		t.Errorf("expected false when the prompt is already in the block array, got body %s", out)
	}
}

// A string element inside the block array counts as carrying the prompt too:
// some clients send content as a mixed array of strings and blocks.
func TestInjectSystemPrompt_SystemRoleArrayContentStringElement(t *testing.T) {
	prompt := "extra instructions"
	in := []byte(`{"messages":[{"role":"system","content":["plain string holding extra instructions"]}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if ok {
		t.Errorf("expected false when a string element already carries the prompt, got %s", out)
	}
}

func TestInjectSystemPrompt_SystemRoleContentNotString(t *testing.T) {
	prompt := "fallback prompt"
	in := []byte(`{"messages":[{"role":"system","content":123}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	content, _ := res["messages"].([]any)[0].(map[string]any)["content"].(string)
	if content != prompt {
		t.Errorf("system content = %q, want %q", content, prompt)
	}
}

func TestInjectSystemPrompt_NoSystemRoleInsertAtZero(t *testing.T) {
	prompt := "new system prompt"
	in := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	msgs := res["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("first message role = %q, want %q", first["role"], "system")
	}
	if first["content"] != prompt {
		t.Errorf("first message content = %q, want %q", first["content"], prompt)
	}
	second := msgs[1].(map[string]any)
	origContent, _ := second["content"].(string)
	if origContent != "hello" {
		t.Errorf("second message content = %q, want %q", origContent, "hello")
	}
}

// responsesItem is one decoded entry of an input[] array.
func responsesItem(t *testing.T, res map[string]any, i int) map[string]any {
	t.Helper()
	items, ok := res["input"].([]any)
	if !ok || len(items) <= i {
		t.Fatalf("input[%d] missing: %v", i, res["input"])
	}
	item, ok := items[i].(map[string]any)
	if !ok {
		t.Fatalf("input[%d] is not an object: %v", i, items[i])
	}
	return item
}

// responsesTexts returns the text of every block in an item's content[], which
// is the only place a Responses message item is allowed to carry text. Reading
// it any other way is what let a Chat-shaped string through to the wire.
func responsesTexts(t *testing.T, item map[string]any) []string {
	t.Helper()
	blocks, ok := item["content"].([]any)
	if !ok {
		t.Fatalf("input item content is %T, want an array of blocks: %v", item["content"], item["content"])
	}
	var out []string
	for _, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			t.Fatalf("content block is %T, want an object: %v", b, b)
		}
		if got, _ := bm["type"].(string); got != "input_text" {
			t.Errorf("content block type = %q, want input_text", got)
		}
		text, _ := bm["text"].(string)
		out = append(out, text)
	}
	return out
}

func TestInjectSystemPrompt_ResponsesAppendsToDeveloperItem(t *testing.T) {
	prompt := "system prompt"
	in := []byte(`{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"original"}]}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	if _, has := res["messages"]; has {
		t.Error("unexpected messages key")
	}
	texts := responsesTexts(t, responsesItem(t, res, 0))
	want := []string{"original", prompt}
	if len(texts) != len(want) {
		t.Fatalf("content blocks = %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("content block %d = %q, want %q", i, texts[i], want[i])
		}
	}
}

// A developer turn a client wrote as a plain string is not valid Responses
// either, but a client does send it — so the prompt must be appended by
// widening that content into blocks rather than leaving a string in its place.
func TestInjectSystemPrompt_ResponsesWidensStringContent(t *testing.T) {
	prompt := "system prompt"
	in := []byte(`{"input":[{"type":"message","role":"system","content":"original"}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	texts := responsesTexts(t, responsesItem(t, res, 0))
	want := []string{"original", prompt}
	if len(texts) != len(want) {
		t.Fatalf("content blocks = %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("content block %d = %q, want %q", i, texts[i], want[i])
		}
	}
}

func TestInjectSystemPrompt_ResponsesInsertsMessageItem(t *testing.T) {
	prompt := "new prompt"
	in := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	out, ok := InjectSystemPrompt(in, prompt)
	if !ok {
		t.Fatal("expected true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	items := res["input"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 items in input, got %d", len(items))
	}
	first := responsesItem(t, res, 0)
	if got, _ := first["type"].(string); got != "message" {
		// Codex rejects a bare Chat turn here: `400 Unknown parameter:
		// 'input[0].content'` (#307) — the item has no content block array.
		t.Errorf("first item type = %q, want message", got)
	}
	if got, _ := first["role"].(string); got != "system" {
		t.Errorf("first item role = %q, want system", got)
	}
	texts := responsesTexts(t, first)
	if len(texts) != 1 || texts[0] != prompt {
		t.Errorf("first item text = %v, want [%q]", texts, prompt)
	}
}

// ============================================================
// toMessageArray
// ============================================================

func TestToMessageArray_Nil(t *testing.T) {
	if got := toMessageArray(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestToMessageArray_NonArray(t *testing.T) {
	if got := toMessageArray("string"); got != nil {
		t.Errorf("expected nil for string, got %v", got)
	}
}

func TestToMessageArray_ValidArray(t *testing.T) {
	arr := []any{map[string]any{"role": "user"}}
	got := toMessageArray(arr)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if len(got) != 1 {
		t.Errorf("expected length 1, got %d", len(got))
	}
}

func TestCompressMessagesWithConfig_PerBlockPreservation(t *testing.T) {
	diff := longGitDiff()
	cfg := DefaultRTKConfig()

	t.Run("OpenAI Chat role tool", func(t *testing.T) {
		chatPayload := map[string]any{
			"messages": []any{
				map[string]any{"role": "tool", "tool_call_id": "c1", "is_error": true, "content": diff},
				map[string]any{"role": "tool", "tool_call_id": "c2", "name": "Read", "content": diff},
				map[string]any{"role": "tool", "tool_call_id": "c3", "name": "bash", "content": diff},
			},
		}
		in, err := json.Marshal(chatPayload)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		out, ok := CompressMessagesWithConfig(in, cfg)
		if !ok {
			t.Fatal("expected compression for bash block")
		}
		var res map[string]any
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		msgs := res["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(msgs))
		}
		// c1: is_error -> must remain untouched
		m1 := msgs[0].(map[string]any)
		if m1["content"].(string) != diff {
			t.Errorf("expected c1 (is_error) to remain uncompressed")
		}
		// c2: Read -> protected tool -> must remain untouched
		m2 := msgs[1].(map[string]any)
		if m2["content"].(string) != diff {
			t.Errorf("expected c2 (Read) to remain uncompressed")
		}
		// c3: bash -> compressed
		m3 := msgs[2].(map[string]any)
		assertContains(t, m3["content"].(string), "... (")
	})

	t.Run("Claude Messages tool_result", func(t *testing.T) {
		claudePayload := map[string]any{
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu1", "is_error": true, "content": diff},
						map[string]any{"type": "tool_result", "tool_use_id": "tu2", "name": "Grep", "content": diff},
						map[string]any{"type": "tool_result", "tool_use_id": "tu3", "name": "shell", "content": diff},
						map[string]any{"type": "tool_result", "tool_use_id": "tu4", "isError": true, "text": diff},
						map[string]any{"type": "tool_result", "tool_use_id": "tu5", "status": "error", "text": diff},
					},
				},
			},
		}
		in, err := json.Marshal(claudePayload)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		out, ok := CompressMessagesWithConfig(in, cfg)
		if !ok {
			t.Fatal("expected compression for shell tool_result")
		}
		var res map[string]any
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		blocks := res["messages"].([]any)[0].(map[string]any)["content"].([]any)
		if len(blocks) != 5 {
			t.Fatalf("expected 5 blocks, got %d", len(blocks))
		}
		// tu1: is_error
		if blocks[0].(map[string]any)["content"].(string) != diff {
			t.Errorf("expected tu1 (is_error) to remain uncompressed")
		}
		// tu2: Grep
		if blocks[1].(map[string]any)["content"].(string) != diff {
			t.Errorf("expected tu2 (Grep) to remain uncompressed")
		}
		// tu3: shell -> compressed
		assertContains(t, blocks[2].(map[string]any)["content"].(string), "... (")
		// tu4: isError
		if blocks[3].(map[string]any)["text"].(string) != diff {
			t.Errorf("expected tu4 (isError) to remain uncompressed")
		}
		// tu5: status error
		if blocks[4].(map[string]any)["text"].(string) != diff {
			t.Errorf("expected tu5 (status error) to remain uncompressed")
		}
	})

	t.Run("Claude Messages nested blocks array", func(t *testing.T) {
		nestedPayload := map[string]any{
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type": "tool_result",
							"name": "Edit",
							"content": []any{
								map[string]any{"type": "text", "text": diff},
							},
						},
						map[string]any{
							"type": "tool_result",
							"name": "terminal",
							"content": []any{
								map[string]any{"type": "text", "text": diff},
							},
						},
					},
				},
			},
		}
		in, _ := json.Marshal(nestedPayload)
		out, ok := CompressMessagesWithConfig(in, cfg)
		if !ok {
			t.Fatal("expected compression for nested terminal block")
		}
		var res map[string]any
		json.Unmarshal(out, &res)
		blocks := res["messages"].([]any)[0].(map[string]any)["content"].([]any)
		// Edit: protected
		b1Text := blocks[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		if b1Text != diff {
			t.Errorf("expected Edit nested text to remain uncompressed")
		}
		// terminal: compressed
		b2Text := blocks[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		assertContains(t, b2Text, "... (")
	})

	t.Run("OpenAI Responses function_call_output", func(t *testing.T) {
		responsesPayload := map[string]any{
			"input": []any{
				map[string]any{"type": "function_call_output", "call_id": "fc1", "status": "error", "output": diff},
				map[string]any{"type": "function_call_output", "call_id": "fc2", "name": "Write", "output": diff},
				map[string]any{"type": "function_call_output", "call_id": "fc3", "name": "command_runner", "output": diff},
			},
		}
		in, err := json.Marshal(responsesPayload)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		out, ok := CompressMessagesWithConfig(in, cfg)
		if !ok {
			t.Fatal("expected compression for command_runner block")
		}
		var res map[string]any
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		items := res["input"].([]any)
		if len(items) != 3 {
			t.Fatalf("expected 3 items, got %d", len(items))
		}
		// fc1: status error
		if items[0].(map[string]any)["output"].(string) != diff {
			t.Errorf("expected fc1 (status error) to remain uncompressed")
		}
		// fc2: Write
		if items[1].(map[string]any)["output"].(string) != diff {
			t.Errorf("expected fc2 (Write) to remain uncompressed")
		}
		// fc3: command_runner -> compressed
		assertContains(t, items[2].(map[string]any)["output"].(string), "... (")
	})

	t.Run("Historical tool calls resolution", func(t *testing.T) {
		chatPayload := map[string]any{
			"messages": []any{
				map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{"id": "tc_read", "type": "function", "function": map[string]any{"name": "patch"}},
						map[string]any{"id": "tc_cmd", "type": "function", "function": map[string]any{"name": "cmd"}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "tc_read", "content": diff},
				map[string]any{"role": "tool", "tool_call_id": "tc_cmd", "content": diff},
			},
		}
		in, _ := json.Marshal(chatPayload)
		out, ok := CompressMessagesWithConfig(in, cfg)
		if !ok {
			t.Fatal("expected compression for cmd block")
		}
		var res map[string]any
		json.Unmarshal(out, &res)
		msgs := res["messages"].([]any)
		// tc_read -> patch -> protected
		if msgs[1].(map[string]any)["content"].(string) != diff {
			t.Errorf("expected tc_read (patch) to remain uncompressed")
		}
		// tc_cmd -> compressed
		assertContains(t, msgs[2].(map[string]any)["content"].(string), "... (")
	})

	t.Run("Protected tools helper validation", func(t *testing.T) {
		tools := []string{"Read", "Grep", "Edit", "Write", "patch", "anchor"}
		for _, tool := range tools {
			if !isProtectedTool(tool) {
				t.Errorf("expected %q to be protected", tool)
			}
			if !isProtectedTool("  " + tool + "  ") {
				t.Errorf("expected %q with whitespace to be protected", tool)
			}
		}
		unprotected := []string{"bash", "sh", "terminal", "python", "curl"}
		for _, tool := range unprotected {
			if isProtectedTool(tool) {
				t.Errorf("expected %q to be unprotected", tool)
			}
		}

		if isErrorToolResult(nil) {
			t.Error("expected nil map to not be error")
		}
		if !isErrorToolResult(map[string]any{"is_error": true}) {
			t.Error("expected is_error: true to be recognized")
		}
		if isErrorToolResult(map[string]any{"is_error": false}) {
			t.Error("expected is_error: false to not be error")
		}
		if !isErrorToolResult(map[string]any{"isError": true}) {
			t.Error("expected isError: true to be recognized")
		}
		if !isErrorToolResult(map[string]any{"status": "error"}) {
			t.Error("expected status: error to be recognized")
		}
		if !isErrorToolResult(map[string]any{"status": "ERROR"}) {
			t.Error("expected status: ERROR to be recognized")
		}
		if isErrorToolResult(map[string]any{"status": "success"}) {
			t.Error("expected status: success to not be error")
		}
	})
}

// ============================================================
// Benchmarks
// ============================================================

func BenchmarkCompressMessages(b *testing.B) {
	diff := longGitDiff()
	body := fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"tool_result","content":%q}]}]}`, diff)
	data := []byte(body)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = CompressMessages(data)
	}
}

func BenchmarkInjectSystemPrompt(b *testing.B) {
	data := []byte(`{"messages":[{"role":"user","content":"hello world"}],"model":"mock"}`)
	prompt := "You are a concise, helpful assistant."
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = InjectSystemPrompt(data, prompt)
	}
}
