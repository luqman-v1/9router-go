package headroom

import "strings"

// The guards in this file decide whether a request body is safe to hand to a
// lossy compressor at all. They run before any network call, so a body that
// trips one never reaches the proxy — the compressor never gets to read content
// the gateway decided is not its to shorten.
//
// The rules are a port of open-sse/rtk/guard.js. They are deliberately
// conservative in the same direction the reference is: a tool result nobody can
// vouch for is skipped, not compressed and hoped over. Compression is a
// performance feature; losing the content of an edit or a failing command is a
// correctness bug, and the asymmetry decides the default.

// protectedToolNames are the code-editing tools whose output must never be
// shortened. The set and its matching rule match tokensaver's protectedTools
// on purpose; it is repeated here rather than exported from there because the
// two lists answer different questions — tokensaver asks "skip this result",
// this asks "skip the whole request" — and a future divergence should be a
// deliberate edit in both.
//
// The keys are the lowercase spelling and the lookup folds case: the same tool
// arrives as "Read" from one client and "read" from another, and a
// case-sensitive map would protect only one of them. That is exactly how this
// list and tokensaver's diverged before — the guard compressed a lowercase
// read while the tokensaver pass left it alone.
var protectedToolNames = map[string]bool{
	"read":   true,
	"grep":   true,
	"edit":   true,
	"write":  true,
	"patch":  true,
	"anchor": true,
}

// knownCommandTools are the terminal and file tools whose output the compressor
// has been shown to handle. A tool outside this set and outside the protected
// set is "unknown", and unknown is skipped.
var knownCommandTools = map[string]bool{
	"bash":             true,
	"sh":               true,
	"terminal":         true,
	"shell":            true,
	"exec":             true,
	"cmd":              true,
	"powershell":       true,
	"command_runner":   true,
	"run_terminal_cmd": true,
	"execute_command":  true,
	"read_file":        true,
	"write_file":       true,
	"read":             true,
	"f":                true,
}

func isProtectedToolName(name string) bool {
	return protectedToolNames[strings.ToLower(strings.TrimSpace(name))]
}

// isUnknownTool reports whether a tool name gives the compressor no safe ground
// to stand on: missing, explicitly unknown, or simply not one it has been shown
// to handle. An empty name is unknown on purpose — a tool result whose call id
// resolves to nothing is exactly the case where guessing is most expensive.
func isUnknownTool(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" || n == "unknown" || n == "unknown_tool" {
		return true
	}
	return !knownCommandTools[strings.ToLower(n)] && !isProtectedToolName(n)
}

// unsafeForCompression is the single entry point client.Compress uses. It is
// true when any part of the body carries an error tool result or a tool whose
// output must not be touched.
func unsafeForCompression(req map[string]any) bool {
	if hasErrorToolResult(req) {
		return true
	}
	return hasUnsafeToolResult(req)
}

// hasErrorToolResult looks for a tool result the upstream marked as failed. An
// error message is the operator's only record of what went wrong, and a lossy
// pass over a stack trace can remove the line that mattered.
func hasErrorToolResult(req map[string]any) bool {
	for _, msg := range messagesOf(req) {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if isErrorToolResultValue(m["is_error"]) || isErrorStatus(m["status"]) {
			// Only a tool-carrying message counts; a user message with a stray
			// "status" field is not a tool failure.
			if fmtRole(m["role"]) == "tool" || hasType(m["content"], "tool_result") {
				return true
			}
		}
		for _, block := range blocksOf(m["content"]) {
			if !hasType(block, "tool_result") {
				continue
			}
			if isErrorToolResultValue(block["is_error"]) || isErrorStatus(block["status"]) {
				return true
			}
		}
	}
	// Responses shape: function_call_output items live beside messages.
	for _, item := range itemsOf(req["input"]) {
		if item["type"] != "function_call_output" {
			continue
		}
		if isErrorToolResultValue(item["is_error"]) || isErrorStatus(item["status"]) {
			return true
		}
	}
	return false
}

// hasUnsafeToolResult reports whether any tool result in the body belongs to a
// protected tool or to one the compressor was never cleared for. The tool name
// is not always on the result block itself, so the call map is built from the
// whole body first: a result identifies its tool through the call id it answers.
func hasUnsafeToolResult(req map[string]any) bool {
	names := toolCallNames(req)
	check := func(name string) bool {
		return isProtectedToolName(name) || isUnknownTool(name)
	}

	for _, msg := range messagesOf(req) {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if fmtRole(m["role"]) == "tool" {
			name := firstString(m["name"], names[firstString(m["tool_call_id"])])
			if check(name) {
				return true
			}
		}
		for _, block := range blocksOf(m["content"]) {
			if !hasType(block, "tool_result") {
				continue
			}
			name := firstString(block["name"], names[firstString(block["tool_use_id"])])
			if check(name) {
				return true
			}
		}
	}
	for _, item := range itemsOf(req["input"]) {
		if item["type"] != "function_call_output" {
			continue
		}
		name := firstString(item["name"], names[firstString(item["call_id"])])
		if check(name) {
			return true
		}
	}
	return false
}

// toolCallNames maps a tool call id to its tool name, across the shapes a call
// can take: OpenAI tool_calls, the single function_call form, and Claude's
// tool_use block.
func toolCallNames(req map[string]any) map[string]string {
	out := map[string]string{}
	record := func(id, name string) {
		if id != "" && name != "" {
			out[id] = name
		}
	}
	for _, msg := range messagesOf(req) {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		for _, call := range sliceOf(m["tool_calls"]) {
			c, ok := call.(map[string]any)
			if !ok {
				continue
			}
			name := firstString(c["name"])
			if fn, ok := c["function"].(map[string]any); ok {
				name = firstString(fn["name"], name)
			}
			record(firstString(c["id"]), name)
		}
		if fc, ok := m["function_call"].(map[string]any); ok {
			record(firstString(fc["id"]), firstString(fc["name"]))
		}
		for _, block := range blocksOf(m["content"]) {
			if hasType(block, "tool_use") {
				record(firstString(block["id"]), firstString(block["name"]))
			}
		}
	}
	// Responses: a function_call item carries call_id and name directly.
	for _, item := range itemsOf(req["input"]) {
		if item["type"] == "function_call" {
			record(firstString(item["call_id"], item["id"]), firstString(item["name"]))
		}
	}
	return out
}

// --- small shape helpers ---------------------------------------------------

func messagesOf(req map[string]any) []any { return sliceOf(req["messages"]) }

func sliceOf(v any) []any {
	s, _ := v.([]any)
	return s
}

// blocksOf returns the content blocks of a message, treating a single object
// content as the one-element list it means.
func blocksOf(v any) []map[string]any {
	switch t := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{t}
	default:
		return nil
	}
}

func itemsOf(v any) []map[string]any { return blocksOf(v) }

func hasType(v any, want string) bool {
	found := false
	for _, block := range blocksOf(v) {
		if fmtType(block["type"]) == want {
			found = true
		}
	}
	return found
}

func fmtRole(v any) string { return firstString(v) }

func fmtType(v any) string { return firstString(v) }

// firstString returns the first value that is a non-empty string. Numbers and
// other scalars are ignored: a tool id is always a string on the wire, and
// coercing something else would invent a name that matches nothing.
func firstString(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func isErrorToolResultValue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func isErrorStatus(v any) bool {
	s, ok := v.(string)
	return ok && strings.EqualFold(s, "error")
}
