package tokensaver

import "testing"

// The protected-tool list exists so that a Read, a Grep, an Edit or a Write
// result reaches the model intact: the agent asked for that content, and a
// rewritten copy is a different thing from what it asked for.
//
// The list used to be matched case-sensitively, so it protected only the
// clients that happened to spell the tool the way the list did. The same
// tool reaches the gateway spelled "Read" by one client and "read" by
// another — the repo's own e2e fixture uses the lowercase spelling, and
// upstream's own RTK filters key on the lowercase "grep". A tool name is not
// a case-sensitive identifier.
func TestProtectedToolNamesAreCaseInsensitive(t *testing.T) {
	for _, name := range []string{
		"Read", "read", "READ", "rEaD",
		"Grep", "grep", "GREP",
		"Edit", "edit",
		"Write", "write",
		"patch", "PATCH", "Patch",
		"anchor", "ANCHOR",
		"glob", "GLOB", "Glob",
		"view", "VIEW",
		"list_dir", "LIST_DIR",
	} {
		if !isProtectedTool(name) {
			t.Errorf("isProtectedTool(%q) = false, want true", name)
		}
	}
}

// A tool that is not on the list is still not protected: the list is a
// targeted exception, not an allowlist.
func TestUnrelatedToolsAreNotProtected(t *testing.T) {
	for _, name := range []string{"", "bash", "shell", "Bash", "webfetch", "  "} {
		if isProtectedTool(name) {
			t.Errorf("isProtectedTool(%q) = true, want false", name)
		}
	}
}

// The OpenAI Chat Completions tool shape carries no error flag:
//
//	{"role":"tool","tool_call_id":"call_1","content":"Error: something failed"}
//
// The only place the failure can be seen is the content text, so a tool
// result whose content opens with an error marker is an error result and
// must not be rewritten: the trace the model needs to diagnose the failure
// is the text that would be compressed away.
func TestOpenAIToolErrorIsDetected(t *testing.T) {
	for _, content := range []string{
		"Error: command not found",
		"Exception: null pointer",
		"Traceback (most recent call last):\n  File \"x.py\", line 1",
		"  Error: indented error still an error",
	} {
		msg := map[string]any{
			"role":         "tool",
			"tool_call_id": "call_1",
			"content":      content,
		}
		if !isErrorToolResult(msg) {
			t.Errorf("isErrorToolResult(%q) = false, want true", content)
		}
	}
}

// A successful tool result is not an error, including one whose text
// mentions the word "error" somewhere in the middle. A false positive would
// leave a normal result uncompressed, which costs tokens but loses nothing;
// a false negative would rewrite the very text the agent needed, so the
// marker has to be anchored to the start of the content.
func TestNormalToolResultIsNotAnError(t *testing.T) {
	for _, content := range []string{
		"file.txt:12: the error was handled by the caller",
		"2 errors and 0 warnings",
		"no problems here",
		"",
	} {
		msg := map[string]any{
			"role":         "tool",
			"tool_call_id": "call_1",
			"content":      content,
		}
		if isErrorToolResult(msg) {
			t.Errorf("isErrorToolResult(%q) = true, want false", content)
		}
	}
}

// The structured flags of the Anthropic shape still work.
func TestAnthropicStructuredErrorFlagsStillWork(t *testing.T) {
	if !isErrorToolResult(map[string]any{"type": "tool_result", "is_error": true}) {
		t.Error("is_error=true must be an error")
	}
	if !isErrorToolResult(map[string]any{"type": "tool_result", "isError": true}) {
		t.Error("isError=true must be an error")
	}
	if !isErrorToolResult(map[string]any{"type": "tool_result", "status": "Error"}) {
		t.Error("status=Error must be an error")
	}
	if isErrorToolResult(map[string]any{"type": "tool_result", "is_error": false}) {
		t.Error("is_error=false must not be an error")
	}
}
