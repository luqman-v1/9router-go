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
//   - the built-in tool names must be declared: a request whose declarations
//     omit them entirely is refused (429 FreeUsageLimitError), and the built-in
//     definitions themselves must stay the verbatim CLI 2.x text — a stubbed
//     description answers 400 "Request validation failed" / 403 FreeTierError.
//     Caller declarations carried alongside them are accepted;
//   - two declarations sharing a name answer a server error, so a caller tool
//     that reuses a built-in name must replace it rather than join it;
//   - tool CALLS are not constrained: history may carry calls to undeclared
//     names and the model may emit them.
//
// Declaring the caller's tools in place of the built-ins passes the gate but
// loses the turn: the harness preamble runs in Code Mode, which says the
// built-ins are the only tools there are, and the model follows it — it
// narrates, or calls a built-in, instead of the caller's tool (measured live
// 2026-11-25). So the caller's declarations travel natively beside the
// built-in set (whose definitions must stay the verbatim CLI text — the gate
// refuses a stub, internal/integration encodes the rule), the preamble's Code
// Mode section is stripped, and a note after the caller's own text states the
// built-ins are inert and the caller's tools are called directly by name. The
// returned rename map is empty: no name is rewritten, so response tool_calls
// arrive in the caller's own spelling and need no restoring.
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
	injectStrippedHarnessSystem(request, flat, claude, origSystem, openCodeModelIdentityNote(request))
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

// openCodeGenuineToolNames lists the CLI built-ins the gate accepts, in
// declaration order.
var openCodeGenuineToolNames = []string{
	"edit",
	"glob",
	"grep",
	"question",
	"read",
	"shell",
	"skill",
	"subagent",
	"webfetch",
	"websearch",
	"write",
	"execute",
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

// openCodeGenuineTools are the CLI's built-in definitions parsed once from the
// verbatim capture. They are injected byte-exact, not as stubs: the gate
// validates the "read" definition against the CLI 2.x text and answers a stub
// with 403 FreeTierError (internal/integration: the inference-gate fake encodes
// the same rule).
var openCodeGenuineTools = func() []map[string]any {
	var tools []map[string]any
	if err := json.Unmarshal([]byte(openCodeGenuineToolsJSON), &tools); err != nil {
		return nil
	}
	return tools
}()

// installCallerToolsWithInertBuiltins keeps the caller's own tool declarations
// and appends the built-in definitions the gate requires. Dropping the built-in
// names entirely is refused (429 FreeUsageLimitError), and replacing the
// caller's declarations outright loses the turn: the preamble's Code Mode
// section tells the model the built-ins are the only tools, so it narrates or
// calls a built-in instead of the caller's tool. Declared natively side by side
// with the Code Mode section stripped and the direct-call note appended, the
// model picks the caller's tools (measured live: 6/6 caller tool emitted).
func installCallerToolsWithInertBuiltins(request map[string]any, flat, claude bool) {
	tools, _ := request["tools"].([]any)
	out := make([]any, 0, len(tools)+len(openCodeGenuineToolNames))
	declared := make(map[string]bool, len(tools))
	for _, tool := range tools {
		out = append(out, ensureOpencodeToolSchema(tool, flat, claude))
		if name := opencodeToolDeclarationName(tool, flat, claude); name != "" {
			declared[name] = true
		}
	}
	for _, name := range openCodeGenuineToolNames {
		// Two tools with one name answer a server error, so a caller tool of
		// that name replaces the built-in rather than joining it.
		if declared[name] {
			continue
		}
		out = append(out, opencodeGenuineToolDefinition(name, flat, claude))
	}
	request["tools"] = out
}

// opencodeToolDeclarationName reads a declared tool's name in the lane's own
// spelling.
func opencodeToolDeclarationName(tool any, flat, claude bool) string {
	m, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	switch {
	case claude:
		name, _ := m["name"].(string)
		return name
	case flat:
		name, _ := m["name"].(string)
		return name
	default:
		fn, _ := m["function"].(map[string]any)
		name, _ := fn["name"].(string)
		return name
	}
}

// opencodeGenuineToolDefinition re-shapes one captured built-in definition into
// the lane's wire format. The chat lane gets the captured object verbatim.
func opencodeGenuineToolDefinition(name string, flat, claude bool) any {
	for _, def := range openCodeGenuineTools {
		fn, _ := def["function"].(map[string]any)
		if fn == nil || fn["name"] != name {
			continue
		}
		switch {
		case claude:
			return map[string]any{
				"name":         fn["name"],
				"description":  fn["description"],
				"input_schema": fn["parameters"],
			}
		case flat:
			return map[string]any{
				"type":        "function",
				"name":        fn["name"],
				"description": fn["description"],
				"parameters":  fn["parameters"],
				"strict":      false,
			}
		default:
			return def
		}
	}
	return nil
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

// openCodeModelIdentityNote overrides the harness preamble's static "Your
// Model" block with the model the client actually selected. The preamble is a
// verbatim CLI capture (see opencode_free_harness.go) and the inference gate
// fingerprints its bytes, so it cannot be edited; a correction appended after
// it is accepted and stops the model from reporting GLM-5.3-Flash Free for
// every free-tier id.
func openCodeModelIdentityNote(request map[string]any) string {
	model, _ := request["model"].(string)
	if model == "" {
		return ""
	}
	return "# Your Model (this session)\n" +
		"The '# Your Model' block in the harness above is a fixed capture used by the inference gate and does not describe this request.\n" +
		"Your actual model for this session is: " + model
}

// openCodeHarnessPrefix is the part of the harness preamble that a stripped
// system prompt keeps byte-exact: everything before the first section whose
// instructions assume the built-in tools are the ones to call.
func openCodeHarnessPrefix() string {
	cut := len(openCodeHarnessSystem)
	for _, marker := range openCodeStrippedMarkers {
		if i := strings.Index(openCodeHarnessSystem, marker); i >= 0 && i < cut {
			cut = i
		}
	}
	return openCodeHarnessSystem[:cut]
}

// injectStrippedHarnessSystem sets the system prompt to the harness preamble
// minus its Code Mode section (which instructs the model to route all tool
// use through the built-ins — inert placeholders here), followed by the
// caller's own system text, the direct-call note and the model identity note.
func injectStrippedHarnessSystem(request map[string]any, flat, claude bool, origSystem, identityNote string) {
	preamble := strings.TrimRight(stripOpencodeCodeMode(openCodeHarnessSystem), " \t\r\n")
	suffix := "\n\n" + opencodeDirectCallNote
	if strings.TrimSpace(origSystem) != "" {
		suffix = "\n\n" + origSystem + suffix
	}
	if identityNote != "" {
		suffix += "\n\n" + identityNote
	}
	replacePreambleSuffix(request, flat, claude, preamble, suffix)
}

// injectOpenCodeHarnessSuffix prepends the full harness preamble and appends
// suffix after the caller's own text. The preamble itself is not optional: the
// gate fingerprints it, and a request without it is refused regardless of
// lane. It is what a title request and an agent turn share; they differ only
// in suffix.
func injectOpenCodeHarnessSuffix(request map[string]any, flat, claude bool, suffix string) {
	injectPreambleSuffix(request, flat, claude, openCodeHarnessSystem, suffix)
}

// replacePreambleSuffix sets the system prompt to preamble+suffix, discarding
// whatever caller text the request carried. The stripped path already carries
// that text inside suffix, so prepending the original as well would send it
// twice — doubling every caller system prompt on the wire.
func replacePreambleSuffix(request map[string]any, flat, claude bool, preamble, suffix string) {
	switch {
	case claude:
		request["system"] = preamble + suffix
	case flat:
		request["instructions"] = preamble + suffix
	default:
		replaceChatSystem(request, preamble, suffix)
	}
}

func replaceChatSystem(request map[string]any, preamble, suffix string) {
	messages, ok := request["messages"].([]any)
	if ok && len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok && first["role"] == "system" {
			first["content"] = preamble + suffix
			return
		}
	}
	request["messages"] = append([]any{map[string]any{"role": "system", "content": preamble + suffix}}, messages...)
}

func injectPreambleSuffix(request map[string]any, flat, claude bool, preamble, suffix string) {
	switch {
	case claude:
		injectClaudeSystem(request, preamble, suffix)
	case flat:
		instructions, _ := request["instructions"].(string)
		if strings.TrimSpace(instructions) == "" {
			request["instructions"] = preamble + suffix
			return
		}
		request["instructions"] = preamble + "\n\n" + instructions + suffix
	default:
		injectChatSystem(request, preamble, suffix)
	}
}

func injectClaudeSystem(request map[string]any, preamble, suffix string) {
	switch system := request["system"].(type) {
	case string:
		if strings.TrimSpace(system) == "" {
			request["system"] = preamble + suffix
			return
		}
		request["system"] = preamble + "\n\n" + system + suffix
	case []any:
		blocks := system
		if len(blocks) > 0 {
			request["system"] = append([]any{openCodeHarnessTextBlock(preamble)}, blocks...)
		} else {
			request["system"] = []any{openCodeHarnessTextBlock(preamble)}
		}
		if trimmed := strings.TrimPrefix(suffix, "\n\n"); trimmed != "" {
			request["system"] = append(request["system"].([]any), openCodeHarnessTextBlock(trimmed))
		}
	default:
		request["system"] = preamble + suffix
	}
}

func openCodeHarnessTextBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func injectChatSystem(request map[string]any, preamble, suffix string) {
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		request["messages"] = []any{map[string]any{"role": "system", "content": preamble + suffix}}
		return
	}
	first, ok := messages[0].(map[string]any)
	if !ok || first["role"] != "system" {
		request["messages"] = append([]any{map[string]any{"role": "system", "content": preamble + suffix}}, messages...)
		return
	}
	switch content := first["content"].(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			first["content"] = preamble + suffix
			return
		}
		first["content"] = preamble + "\n\n" + content + suffix
	case []any:
		blocks := content
		if len(blocks) > 0 {
			first["content"] = append([]any{openCodeHarnessTextBlock(preamble)}, blocks...)
		} else {
			first["content"] = []any{openCodeHarnessTextBlock(preamble)}
		}
		if trimmed := strings.TrimPrefix(suffix, "\n\n"); trimmed != "" {
			first["content"] = append(first["content"].([]any), openCodeHarnessTextBlock(trimmed))
		}
	default:
		first["content"] = preamble + suffix
	}
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
// forced choice naming no genuine name is dropped (reporting the drop so the
// caller does not refill "auto" over it). On the chat and Claude lanes a
// forced choice naming anything else becomes "required": the name cannot
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

// openCodeStrippedMarkers are the preamble passages that only make sense when
// the built-ins are the live tool set: the tool-preference bullets, the Code
// Mode section and the worktree guidance after it.
var openCodeStrippedMarkers = []string{
	"- Prefer dedicated tools over shell",
	"- Use the write tool",
	"- Use the edit tool",
	"- Prefer parallelizing",
	"# Code Mode",
	"When you create a worktree",
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
