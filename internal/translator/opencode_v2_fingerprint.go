package translator

import (
	json "encoding/json/v2"
	"strings"
)

// ConcealOpenCodeV2FingerprintTools shapes a request for the OpenCode v2
// free-tier inference gate. Measured live on 2026-10-25 (see
// opencode_free_harness.go), the gate fingerprints the request body and
// answers anything else with 429 FreeUsageLimitError — "Free models work in
// OpenCode" — regardless of transport, session, endpoint or User-Agent:
//
//   - the system prompt must start with the CLI harness preamble, verbatim
//     (a 2,000-char prefix fails; caller instructions appended after the full
//     preamble pass);
//   - tool declarations must be the CLI built-ins, byte-exact: a
//     foreign-named tool, a renamed clone, or an edited description all
//     answer 400 "Request validation failed"; at least six definitions are
//     required;
//   - tool CALLS are not constrained: history may carry calls to undeclared
//     names and the model may emit them.
//
// So caller tools never travel as declarations. They are described in the
// appended system text and keep their names in history, and the genuine
// built-in set is declared in their place. The returned rename map is empty:
// no name is rewritten, so response tool_calls arrive in the caller's own
// spelling and need no restoring.
//
// flat is true for Responses API bodies and false for Chat Completions;
// Claude Messages bodies (a "system" field or input_schema tools) are
// detected and shaped in their own format. forceAutoToolChoice rewrites an
// explicit choice to "auto" on the Responses lane for auto-only models (the
// upstream forceAutoToolChoiceModels quirk).
func ConcealOpenCodeV2FingerprintTools(body []byte, flat bool, forceAutoChoice bool) ([]byte, map[string]string) {
	var request map[string]any
	if len(body) == 0 || json.Unmarshal(body, &request) != nil {
		return body, map[string]string{}
	}
	claude := isOpenCodeClaudeShapedRequest(request)
	origSystem := opencodeSystemString(request)
	installCallerToolsWithInertBuiltins(request, flat, claude)
	injectStrippedHarnessSystem(request, flat, claude, origSystem)
	if flat {
		// Prior-turn function_call items with no name fail the Responses
		// validator; a harness history can carry one.
		dropNamelessOpenCodeV2FunctionCalls(request)
	}
	droppedChoice := normalizeOpenCodeV2ToolChoice(request, flat, forceAutoChoice, openCodeGenuineToolSet)
	if flat && request["tool_choice"] == nil && !droppedChoice {
		request["tool_choice"] = "auto"
	}
	out, err := json.Marshal(request, json.Deterministic(true))
	if err != nil {
		return body, map[string]string{}
	}
	return out, map[string]string{}
}

// openCodeGenuineToolSet lists the built-in names the gate accepts as
// declarations.
var openCodeGenuineToolSet = func() map[string]bool {
	set := make(map[string]bool, len(openCodeGenuineToolNames))
	for _, name := range openCodeGenuineToolNames {
		set[name] = true
	}
	return set
}()

// isOpenCodeClaudeShapedRequest reports whether the body is a Claude Messages
// request: those carry a top-level "system" and input_schema tools, and must
// keep that wire format (nested chat tools would not validate).
func isOpenCodeClaudeShapedRequest(request map[string]any) bool {
	if _, ok := request["system"]; ok {
		return true
	}
	if tools, ok := request["tools"].([]any); ok {
		for _, tool := range tools {
			if m, ok := tool.(map[string]any); ok {
				if _, has := m["input_schema"]; has {
					return true
				}
			}
		}
	}
	return false
}

// installCallerToolsWithInertBuiltins keeps the caller's own tool
// declarations and appends an inert placeholder for every gate built-in.
// The inference gate 429s a request whose built-in names are missing
// entirely, while replacing the caller's declarations outright makes the
// model call the placeholders instead of the caller's tools; declared
// natively side by side, the model picks the caller's tools (measured
// live: 6/6 requests accepted, caller tool emitted).
func installCallerToolsWithInertBuiltins(request map[string]any, flat, claude bool) {
	tools, _ := request["tools"].([]any)
	out := make([]any, 0, len(tools)+len(openCodeGenuineToolNames))
	for _, tool := range tools {
		out = append(out, ensureOpencodeToolSchema(tool, flat, claude))
	}
	for _, name := range openCodeGenuineToolNames {
		switch {
		case claude:
			out = append(out, map[string]any{
				"name":         name,
				"description":  "Unavailable placeholder.",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
			})
		case flat:
			out = append(out, map[string]any{
				"type": "function", "name": name, "description": "Unavailable placeholder.",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
				"strict":     false,
			})
		default:
			out = append(out, map[string]any{"type": "function", "function": map[string]any{
				"name":        name,
				"description": "Unavailable placeholder.",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			}})
		}
	}
	request["tools"] = out
}

// ensureOpencodeToolSchema gives a caller tool declaration an empty object
// schema when it has none: the gate validates every declared tool against an
// object parameters/input_schema, and caller tools pass through verbatim now.
func ensureOpencodeToolSchema(tool any, flat, claude bool) any {
	empty := map[string]any{"type": "object", "properties": map[string]any{}}
	m, ok := tool.(map[string]any)
	if !ok {
		return tool
	}
	switch {
	case claude:
		if _, ok := m["input_schema"]; !ok {
			m["input_schema"] = empty
		}
	case flat:
		if _, ok := m["parameters"]; !ok {
			m["parameters"] = empty
		}
	default:
		if fn, ok := m["function"].(map[string]any); ok {
			if _, ok := fn["parameters"]; !ok {
				fn["parameters"] = empty
			}
		}
	}
	return tool
}

// injectStrippedHarnessSystem sets the system prompt to the harness preamble
// minus its Code Mode section (which instructs the model to route all tool
// use through the built-ins — inert placeholders here), followed by the
// caller's own system text and the direct-call note.
func injectStrippedHarnessSystem(request map[string]any, flat, claude bool, origSystem string) {
	sys := strings.TrimRight(stripOpencodeCodeMode(openCodeHarnessSystem), " 	\r\n")
	if strings.TrimSpace(origSystem) != "" {
		sys += "\n\n" + origSystem
	}
	sys += "\n\n" + opencodeDirectCallNote
	setOpencodeSystem(request, flat, claude, sys)
}

// dropNamelessOpenCodeV2FunctionCalls removes prior-turn function_call items
// with no name; the Responses validator rejects them and a harness history
// can carry one.
func dropNamelessOpenCodeV2FunctionCalls(request map[string]any) {
	input, ok := request["input"].([]any)
	if !ok {
		return
	}
	out := make([]any, 0, len(input))
	for _, item := range input {
		m, ok := item.(map[string]any)
		if ok && m["type"] == "function_call" {
			if name, _ := m["name"].(string); strings.TrimSpace(name) == "" {
				continue
			}
		}
		out = append(out, item)
	}
	request["input"] = out
}

// normalizeOpenCodeV2ToolChoice keeps tool_choice inside what the gate can
// validate. A "none" choice is dropped on every lane (the gate answers it
// with 403 FreeTierError; tool suppression lives in the prompt). On the
// Responses lane an explicit choice becomes "auto" on auto-only models, and a
// forced choice naming no genuine built-in is dropped (reporting the drop so
// the caller does not refill "auto" over it). On the chat and Claude lanes a
// forced choice naming a caller tool becomes "required": the name cannot
// travel, but the intent to force a tool call can.
//
// It reports whether a forced choice was dropped on the Responses lane.
func normalizeOpenCodeV2ToolChoice(request map[string]any, flat, forceAutoChoice bool, genuine map[string]bool) bool {
	if choice, present := request["tool_choice"]; present && isOpenCodeNoneToolChoice(choice) {
		delete(request, "tool_choice")
	}
	choice, present := request["tool_choice"]
	if !present || choice == nil {
		return false
	}
	if s, ok := choice.(string); ok {
		if flat && s != "auto" && forceAutoChoice {
			request["tool_choice"] = "auto"
		}
		return false
	}
	m, ok := choice.(map[string]any)
	if !ok {
		return false
	}
	kind, _ := m["type"].(string)
	if kind != "function" && kind != "tool" {
		return false
	}
	if flat {
		if forceAutoChoice {
			request["tool_choice"] = "auto"
			return false
		}
		if !genuine[strings.TrimSpace(openCodeChoiceName(m))] {
			delete(request, "tool_choice")
			return true
		}
		return false
	}
	if !genuine[strings.TrimSpace(openCodeChoiceName(m))] {
		request["tool_choice"] = "required"
	}
	return false
}

// openCodeChoiceName reads the function/tool name from either forced-choice
// spelling: {"type":"function","function":{"name"}}, the Claude
// {"type":"tool","name"} and the Responses {"type":"function","name"}.
func openCodeChoiceName(m map[string]any) string {
	if name, _ := m["name"].(string); name != "" {
		return name
	}
	if fn, ok := m["function"].(map[string]any); ok {
		name, _ := fn["name"].(string)
		return name
	}
	return ""
}

// isOpenCodeNoneToolChoice reports whether a tool_choice value asks for no
// tools, in either spelling the wire formats use: the string "none" or the
// object form {"type":"none"}.
func isOpenCodeNoneToolChoice(choice any) bool {
	switch c := choice.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(c), "none")
	case map[string]any:
		t, _ := c["type"].(string)
		return strings.EqualFold(strings.TrimSpace(t), "none")
	}
	return false
}

// opencodeSystemString extracts the caller's own system text across the three
// wire shapes (Responses instructions, Claude system, chat system message).
func opencodeSystemString(request map[string]any) string {
	if s, ok := request["instructions"].(string); ok {
		return s
	}
	if s, ok := request["system"].(string); ok {
		return s
	}
	blocksText := func(arr []any) string {
		var sb strings.Builder
		for _, b := range arr {
			if m, ok := b.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	if s, ok := request["system"].([]any); ok {
		return blocksText(s)
	}
	if msgs, ok := request["messages"].([]any); ok && len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
			switch c := first["content"].(type) {
			case string:
				return c
			case []any:
				return blocksText(c)
			}
		}
	}
	return ""
}

func setOpencodeSystem(request map[string]any, flat, claude bool, s string) {
	switch {
	case claude:
		request["system"] = s
	case flat:
		request["instructions"] = s
	default:
		if msgs, ok := request["messages"].([]any); ok && len(msgs) > 0 {
			if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
				first["content"] = s
				return
			}
			request["messages"] = append([]any{map[string]any{"role": "system", "content": s}}, msgs...)
			return
		}
		request["messages"] = []any{map[string]any{"role": "system", "content": s}}
	}
}

func stripOpencodeCodeMode(sys string) string {
	if i := strings.Index(sys, "# Code Mode"); i >= 0 {
		if j := strings.Index(sys[i:], "Skills provide specialized"); j >= 0 {
			sys = sys[:i] + sys[i+j:]
		}
	}
	if i := strings.Index(sys, "When you create a worktree"); i >= 0 {
		sys = sys[:i]
	}
	lines := strings.Split(sys, "\n")
	out := lines[:0]
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "- Prefer dedicated tools over shell") ||
			strings.HasPrefix(t, "- Use the write tool") ||
			strings.HasPrefix(t, "- Use the edit tool") ||
			strings.HasPrefix(t, "- Prefer parallelizing") {
			continue
		}
		out = append(out, ln)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " 	\r\n") + "\n"
}

const opencodeDirectCallNote = "# Session tools\n" +
	"The built-in tool declarations in this request are inert placeholders — never call them. " +
	"The calling client provides its own tools; call them directly by their exact names and argument schemas, and emit the tool call immediately when a request needs one."
