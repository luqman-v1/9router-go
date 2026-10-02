package executor

import (
	json "encoding/json/v2"
	"fmt"
)

// Port of open-sse/executors/codex.js 7bf931781: hosted web search on a Lite
// model. Lite cannot execute the search tool — a search call never comes back
// from the Lite transport — so a body asking for one has to travel on regular
// Responses with its tools lifted back to the top level, exactly as upstream
// does when it drops the `x-openai-internal-codex-responses-lite` header.

// codexWebSearchToolType is OpenAI's hosted search tool. Lite cannot execute
// it — a search call never comes back from the Lite transport — so a request
// carrying it has to travel on regular Responses instead.
const codexWebSearchToolType = "web_search"

// codexAutoWebSearchFlag is the marker a caller sets to have hosted search
// registered for it. It is consumed here and never forwarded.
const codexAutoWebSearchFlag = "_autoCodexWebSearch"

// codexAdditionalToolsType is the Lite input prefix item that carries tools.
const codexAdditionalToolsType = "additional_tools"

// hasHostedWebSearch reports whether a tool list asks for hosted search.
func hasHostedWebSearch(items []any) bool {
	for _, item := range items {
		m, isMap := item.(map[string]any)
		if !isMap {
			continue
		}
		if t, _ := m["type"].(string); t == codexWebSearchToolType {
			return true
		}
	}
	return false
}

// toolListWebSearch reports whether whatever sits under key is a tool list that
// asks for hosted search. A value of another shape answers false.
func toolListWebSearch(key any) bool {
	items, ok := responseInputItems(key)
	return ok && hasHostedWebSearch(items)
}

// codexToolKey mirrors upstream's `${tool.type}:${tool.name ||
// tool.function.name || ""}` dedupe key, so a tool declared both top-level and
// inside a Lite prefix is registered once.
func codexToolKey(tool any) string {
	m, isMap := tool.(map[string]any)
	if !isMap {
		return "\x00" + fmt.Sprint(tool)
	}
	kind, _ := m["type"].(string)
	name, _ := m["name"].(string)
	if name == "" {
		if fn, isFn := m["function"].(map[string]any); isFn {
			name, _ = fn["name"].(string)
		}
	}
	return kind + ":" + name
}

// registerCodexHostedWebSearch consumes the auto-injection marker and appends
// hosted search when the caller did not already list it. It reports whether a
// tool was appended — either answer leaves the marker deleted, because it is
// ours and the Codex backend would reject the unknown field.
//
// Only the Lite models resolve the marker: applyCodexModelShape is reached by
// every Responses-shaped provider, and hosted search is a codex capability.
func registerCodexHostedWebSearch(req map[string]any) bool {
	flag, _ := req[codexAutoWebSearchFlag].(bool)
	delete(req, codexAutoWebSearchFlag)
	if !flag || toolListWebSearch(req["tools"]) {
		return false
	}
	tools, _ := responseInputItems(req["tools"])
	req["tools"] = append(append(make([]any, 0, len(tools)+1), tools...), map[string]any{
		"type": codexWebSearchToolType,
	})
	return true
}

// liftCodexHostedWebSearch moves a toolset out of a native Lite prefix and onto
// the top-level tools field, because hosted search cannot run from the prefix.
// Definitions already present top-level are not repeated: a body can carry the
// same tool both ways after a replayed transcript, and the Codex backend
// rejects a duplicate definition.
//
// It reports whether the body carries hosted search at all. The prefix is
// dropped in that case, but the developer message that travelled beside it
// stays in the input — losing it would drop the caller's own instructions,
// which upstream is explicit about preserving (its convertedLitePrefix guard
// only spares them from being replaced by the default instructions).
func liftCodexHostedWebSearch(req map[string]any) bool {
	input, ok := responseInputItems(req["input"])
	if !ok {
		return false
	}
	prefix, _ := splitCodexLitePrefix(input)
	if !toolListWebSearch(req["tools"]) && !prefixedWebSearch(prefix) {
		return false
	}

	top, _ := responseInputItems(req["tools"])
	merged := make([]any, 0, len(top)+len(input))
	seen := make(map[string]bool, len(top))
	for _, tool := range top {
		merged = append(merged, tool)
		seen[codexToolKey(tool)] = true
	}
	for _, tools := range prefix {
		for _, tool := range tools {
			key := codexToolKey(tool)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, tool)
		}
	}

	req["tools"] = merged
	req["input"] = dropCodexLitePrefix(input)
	return true
}

// splitCodexLitePrefix separates the input into the tool lists its
// additional_tools items carry and the items that survive once those prefixes
// are gone. Both slices alias the caller's items: a prefix the caller supplied
// is forwarded exactly as it arrived.
func splitCodexLitePrefix(input []any) (prefix [][]any, rest []any) {
	rest = make([]any, 0, len(input))
	for _, item := range input {
		m, isMap := item.(map[string]any)
		if !isMap {
			rest = append(rest, item)
			continue
		}
		if t, _ := m["type"].(string); t != codexAdditionalToolsType {
			rest = append(rest, item)
			continue
		}
		if tools, readable := responseInputItems(m["tools"]); readable {
			prefix = append(prefix, tools)
		}
	}
	return prefix, rest
}

// prefixedWebSearch reports whether any Lite prefix item asks for hosted
// search — the one place a tool can be registered and never run.
func prefixedWebSearch(prefix [][]any) bool {
	for _, tools := range prefix {
		if hasHostedWebSearch(tools) {
			return true
		}
	}
	return false
}

// dropCodexLitePrefix returns the input without its additional_tools items. It
// answers the original slice when there was no prefix to drop.
func dropCodexLitePrefix(input []any) []any {
	_, rest := splitCodexLitePrefix(input)
	if len(rest) == len(input) {
		return input
	}
	return rest
}

// codexResponsesLiteBody reports whether a forwarded Codex body travels in the
// Lite shape, so the caller can send the header that tells the backend to
// serve it. Hosted search answers false: Lite cannot execute it.
func codexResponsesLiteBody(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	model, _ := req["model"].(string)
	if !isCodexResponsesLiteModel(model) {
		return false
	}
	return !toolListWebSearch(req["tools"])
}
