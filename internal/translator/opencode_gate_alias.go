package translator

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
)

// OpenCode free-tier gate: measured name/parameter filters.
//
// A declared tool whose name contains "terminal" (case-insensitive) or
// "run_code" (case-insensitive) answers 429 FreeUsageLimitError even when the
// account has quota (decolua/9router#5319, #5328). The Codex harness pair
// ReportFindings + (ScheduleWakeup|DeferredToolPlaceholder) is refused as well
// (#5690); renaming ReportFindings breaks both pairs and is harmless on its
// own. Client tool declarations whose `parameters.properties` carries
// `old_string` or `new_string` are refused too, while `old_text`/`new_text`
// pass (measured by the sanitized bridge, 2026-11-21). The refusal wears the
// quota wording, so it reads like an exhausted allowance.
//
// The clash is entirely in the declaration: history tool calls and system
// text are not inspected, and the model is free to emit calls to undeclared
// names. So only declarations are rewritten, and the caller's spelling is put
// back on the response by RestoreOpenCodeGateAliases.

type OpenCodeGateAliases struct {
	// Sent name -> caller's name.
	ToolNames map[string]string `json:"toolNames,omitempty"`
	// Sent parameter key -> caller's key.
	ParamKeys map[string]string `json:"paramKeys,omitempty"`
}

// HasAliases reports whether any rewrite needs restoring.
func (a OpenCodeGateAliases) HasAliases() bool {
	return len(a.ToolNames) > 0 || len(a.ParamKeys) > 0
}

var openCodeGateParamKeys = map[string]string{
	"old_string": "old_text",
	"new_string": "new_text",
}

// Only ReportFindings needs an alias: issue #5690's refusal requires it
// together with ScheduleWakeup or DeferredToolPlaceholder. Renaming the other
// two would change a tool name on requests where no pair exists.
var openCodeGatePairNames = map[string]bool{
	"reportfindings": true,
}

// isOpenCodeGateGatedName reports whether a declaration name is filtered by
// the free gate.
func isOpenCodeGateGatedName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "terminal") ||
		strings.Contains(lower, "run_code") ||
		openCodeGatePairNames[lower]
}

// AliasOpenCodeGateTools rewrites gated declarations in a shaped request. It
// mutates request in place and returns the alias maps for restoration.
func AliasOpenCodeGateTools(request map[string]any, flat, claude bool) OpenCodeGateAliases {
	aliases := OpenCodeGateAliases{}
	tools, ok := request["tools"].([]any)
	if !ok {
		return aliases
	}
	// Names already taken by other declarations, so a generated alias never
	// collides with a real tool.
	taken := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if name := opencodeToolDeclarationName(tool, flat, claude); name != "" {
			taken[strings.ToLower(name)] = true
		}
	}
	aliases.ToolNames = map[string]string{}
	aliases.ParamKeys = map[string]string{}
	// One global key space: the response side has a single alias map, so two
	// tools must never agree on the same sent key for different originals.
	takenParamKeys := map[string]bool{}
	for _, tool := range tools {
		if m, ok := tool.(map[string]any); ok {
			for key := range declarationProperties(m, claude) {
				takenParamKeys[strings.ToLower(key)] = true
			}
		}
	}

	for _, tool := range tools {
		m, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		name := opencodeToolDeclarationName(tool, flat, claude)
		if name != "" && isOpenCodeGateGatedName(name) {
			alias := gateAliasForName(name, taken)
			taken[strings.ToLower(alias)] = true
			aliases.ToolNames[alias] = name
			setOpenCodeDeclarationName(m, alias, flat, claude)
		}
		aliasToolDeclarationParams(m, claude, aliases.ParamKeys, takenParamKeys)
	}
	if len(aliases.ToolNames) == 0 {
		aliases.ToolNames = nil
	}
	if len(aliases.ParamKeys) == 0 {
		aliases.ParamKeys = nil
	}
	// A forced tool_choice still naming the original tool would point at a
	// declaration upstream no longer sees. History tool calls are not
	// inspected by the gate, but retargeting them keeps the body consistent.
	retargetFingerprintToolReferences(request, aliases.ToolNames)
	return aliases
}

func gateAliasForName(name string, taken map[string]bool) string {
	lower := strings.ToLower(name)
	base := "run_command"
	switch {
	case strings.Contains(lower, "run_code"):
		base = "run_program"
	case openCodeGatePairNames[lower]:
		base = "report_notes"
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := base + "_" + itoa(i)
		if !taken[candidate] {
			return candidate
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func setOpenCodeDeclarationName(m map[string]any, name string, flat, claude bool) {
	switch {
	case claude, flat:
		m["name"] = name
	default:
		if fn, ok := m["function"].(map[string]any); ok {
			fn["name"] = name
		}
	}
}

// aliasToolDeclarationParams renames gated property keys and their mentions
// in `required`. Property schemas are untouched: only the key is inspected.
func declarationProperties(m map[string]any, claude bool) map[string]any {
	var params map[string]any
	if claude {
		params, _ = m["input_schema"].(map[string]any)
	} else {
		params, _ = m["parameters"].(map[string]any)
		if params == nil {
			if fn, ok := m["function"].(map[string]any); ok {
				params, _ = fn["parameters"].(map[string]any)
			}
		}
	}
	if params == nil {
		return nil
	}
	properties, _ := params["properties"].(map[string]any)
	return properties
}

func aliasToolDeclarationParams(m map[string]any, claude bool, out map[string]string, takenParamKeys map[string]bool) {
	properties := declarationProperties(m, claude)
	if properties == nil {
		return
	}
	var params map[string]any
	if claude {
		params, _ = m["input_schema"].(map[string]any)
	} else {
		params, _ = m["parameters"].(map[string]any)
		if params == nil {
			if fn, ok := m["function"].(map[string]any); ok {
				params, _ = fn["parameters"].(map[string]any)
			}
		}
	}
	if params == nil {
		return
	}
	taken := takenParamKeys
	renames := map[string]string{}
	for key := range properties {
		base, gated := openCodeGateParamKeys[key]
		if !gated {
			continue
		}
		alias := base
		for i := 2; taken[alias]; i++ {
			alias = base + itoa(i)
		}
		taken[alias] = true
		renames[key] = alias
		out[alias] = key
	}
	if len(renames) == 0 {
		return
	}
	for old, alias := range renames {
		properties[alias] = properties[old]
		delete(properties, old)
	}
	if required, ok := params["required"].([]any); ok {
		for i, item := range required {
			if key, ok := item.(string); ok {
				if alias, found := renames[key]; found {
					required[i] = alias
				}
			}
		}
	}
	// The description often spells out the parameter names in prose. Keeping
	// the old spelling there is fine: the gate reads schema keys, not prose,
	// and the direct-call note tells the model to use the declared schema.
}

// RestoreOpenCodeGateAliases puts the caller's tool names and argument keys
// back in a response: SSE frames or a single non-streaming JSON body.
func RestoreOpenCodeGateAliases(payload []byte, aliases OpenCodeGateAliases) []byte {
	if !aliases.HasAliases() || len(payload) == 0 {
		return payload
	}
	trimmed := bytes.TrimSpace(payload)
	// A JSON body can contain the text "data:" in a message. Decide by the
	// first byte before looking for SSE framing, or a chat reply would be
	// mistaken for a stream.
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return restoreOpenCodeGateAliasesJSON(payload, aliases)
	}
	if bytes.Contains(payload, []byte("data:")) {
		return restoreOpenCodeGateAliasesSSE(payload, aliases)
	}
	return payload
}

func restoreOpenCodeGateAliasesSSE(chunk []byte, aliases OpenCodeGateAliases) []byte {
	var out bytes.Buffer
	out.Grow(len(chunk))
	remaining := chunk
	changedAny := false
	for len(remaining) > 0 {
		nl := bytes.IndexByte(remaining, '\n')
		var line []byte
		if nl < 0 {
			line = remaining
			remaining = nil
		} else {
			line = remaining[:nl+1]
			remaining = remaining[nl+1:]
		}
		body := bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(body, []byte("data:")) {
			jsonPart := bytes.TrimSpace(body[len("data:"):])
			if len(jsonPart) > 0 && !bytes.Equal(jsonPart, []byte("[DONE]")) {
				restored := restoreOpenCodeGateAliasesJSON(jsonPart, aliases)
				if !bytes.Equal(restored, jsonPart) {
					out.WriteString("data: ")
					out.Write(restored)
					out.WriteByte('\n')
					changedAny = true
					continue
				}
			}
		}
		out.Write(line)
	}
	if !changedAny {
		return chunk
	}
	return out.Bytes()
}

func restoreOpenCodeGateAliasesJSON(payload []byte, aliases OpenCodeGateAliases) []byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return payload
	}
	var value any
	if json.Unmarshal(trimmed, &value) != nil {
		return payload
	}
	if !restoreOpenCodeGateAliasesValue(value, aliases) {
		return payload
	}
	out, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		return payload
	}
	return out
}

// isToolNameHolder limits name restoration to objects that actually describe
// a tool call or declaration. A user payload field named "name" must not be
// rewritten just because its value matches a generated alias.
func isToolNameHolder(m map[string]any) bool {
	for _, key := range []string{"arguments", "parameters", "input_schema", "function", "input"} {
		if _, ok := m[key]; ok {
			return true
		}
	}
	t, _ := m["type"].(string)
	return t == "function" || t == "tool" || t == "function_call" || t == "custom_tool_call" || t == "tool_use"
}

func restoreOpenCodeGateAliasesValue(value any, aliases OpenCodeGateAliases) bool {
	changed := false
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if name, ok := child.(string); ok && (key == "name" || key == "tool" || key == "function") {
				if original, found := aliases.ToolNames[name]; found && isToolNameHolder(v) {
					v[key] = original
					changed = true
				}
			}
			if (key == "arguments" || key == "input" || key == "args") && len(aliases.ParamKeys) > 0 {
				if s, ok := child.(string); ok {
					if restored := restoreOpenCodeGateArgKeys(s, aliases.ParamKeys); restored != s {
						v[key] = restored
						changed = true
					}
				}
			}
			if restoreOpenCodeGateAliasesValue(child, aliases) {
				changed = true
			}
		}
	case []any:
		for _, child := range v {
			if restoreOpenCodeGateAliasesValue(child, aliases) {
				changed = true
			}
		}
	}
	return changed
}

// restoreOpenCodeGateArgKeys rewrites aliased keys inside a tool call's JSON
// argument string. Fragments that are not valid JSON on their own are handled
// with a token-level replacement so streamed arguments still come back with the
// caller's spelling.
// RestoreOpenCodeGateArgumentString restores aliased argument keys in one
// tool call's argument JSON. Exported for the executor's streaming writer,
// which has to assemble fragments before rewriting them.
func RestoreOpenCodeGateArgumentString(args string, aliases OpenCodeGateAliases) string {
	if !aliases.HasAliases() || args == "" {
		return args
	}
	return restoreOpenCodeGateArgKeys(args, aliases.ParamKeys)
}

func restoreOpenCodeGateArgKeys(args string, paramKeys map[string]string) string {
	if args == "" {
		return args
	}
	var value any
	if json.Unmarshal([]byte(args), &value) == nil {
		if restoreOpenCodeGateArgKeysValue(value, paramKeys) {
			if out, err := json.Marshal(value, json.Deterministic(true)); err == nil {
				return string(out)
			}
		}
		return args
	}
	// Streaming fragment: replace quoted keys only, never a substring of a
	// value. A fragment boundary can split a key; the next fragment then
	// carries the remainder, which has no opening quote and stays untouched.
	out := args
	for alias, original := range paramKeys {
		for _, quoted := range []string{`"` + alias + `"`, `\"` + alias + `\"`} {
			if strings.Contains(out, quoted) {
				replacement := strings.Replace(quoted, alias, original, 1)
				out = strings.ReplaceAll(out, quoted, replacement)
			}
		}
	}
	return out
}

func restoreOpenCodeGateArgKeysValue(value any, paramKeys map[string]string) bool {
	changed := false
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if original, found := paramKeys[key]; found {
				delete(v, key)
				v[original] = child
				changed = true
			}
			if restoreOpenCodeGateArgKeysValue(child, paramKeys) {
				changed = true
			}
		}
	case []any:
		for _, child := range v {
			if restoreOpenCodeGateArgKeysValue(child, paramKeys) {
				changed = true
			}
		}
	}
	return changed
}
