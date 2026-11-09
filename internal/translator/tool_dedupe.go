package translator

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
)

// thinkingOverrideSuffix matches the 9router thinking override a client may
// append to a model id ("deepseek-v4-flash(max)"). It is stripped before a
// model is matched against a catalog, and so before the DeepSeek check below.
var thinkingOverrideSuffix = regexp.MustCompile(`\([^()]+\)\s*$`)

// IsDeepSeekModel reports whether model id is a DeepSeek model. Upstream
// (open-sse/providers/models/helpers.js isDeepSeekModel) strips a trailing
// "(level)" thinking override and accepts a vendor-prefixed id
// ("deepseek-v4-pro", "some-gateway/deepseek-chat") before matching.
func IsDeepSeekModel(model string) bool {
	clean := strings.TrimSpace(thinkingOverrideSuffix.ReplaceAllString(model, ""))
	// A vendor-prefixed id ("openrouter/deepseek-chat") carries the vendor
	// before the slash and the model after it.
	if idx := strings.LastIndexByte(clean, '/'); idx >= 0 {
		clean = clean[idx+1:]
	}
	return strings.HasPrefix(strings.ToLower(clean), "deepseek-")
}

// DedupeToolsDeepSeek collapses same-name tool declarations in a request body
// that is about to be sent to a DeepSeek model. DeepSeek rejects the whole
// request with 400 "Tool names must be unique" (upstream commit 7f5bd155,
// open-sse/utils/toolDeduper.js), while GLM/MiniMax/Kimi accept duplicates.
//
// The key is the declared name in whichever shape the body uses — "name" for a
// Claude/Gemini tool, "function"."name" for an OpenAI one — and the first
// declaration wins. tool_choice and the message history address tools by name,
// so dropping a later duplicate cannot orphan a reference. The body is
// returned untouched for any other model, and when nothing was dropped the
// caller's own bytes are returned, so the hot path does not re-marshal.
func DedupeToolsDeepSeek(body []byte, model string) []byte {
	if !IsDeepSeekModel(model) || !strings.Contains(string(body), `"tools"`) {
		return body
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	tools, ok := req["tools"].([]any)
	if !ok {
		return body
	}
	kept, droppedNames := dedupeToolList(tools)
	if len(droppedNames) == 0 {
		return body
	}
	req["tools"] = kept
	CleanStrippedToolReferences(req["messages"], droppedNames)
	// Deterministic marshaling keeps the serialized request prefix stable
	// across turns, which DeepSeek's prefix prompt cache requires;
	// json/v2 randomizes map member order by default.
	out, err := json.Marshal(req, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

// dedupeToolList drops every tool whose name was already declared and returns
// the names that were dropped.
func dedupeToolList(tools []any) (kept []any, droppedNames map[string]bool) {
	seen := make(map[string]bool, len(tools))
	kept = make([]any, 0, len(tools))
	droppedNames = make(map[string]bool)
	for _, tool := range tools {
		name := declaredToolName(tool)
		if name != "" {
			if seen[name] {
				droppedNames[name] = true
				continue
			}
			seen[name] = true
		}
		kept = append(kept, tool)
	}
	return kept, droppedNames
}

// CleanStrippedToolReferences removes Claude Code tool_reference blocks inside
// tool_result messages that name a stripped or unavailable tool. Strict
// Claude-compatible upstreams (e.g. z.ai 400 code 1210) reject the request when
// a tool_reference points to a tool not present in tools[]. If all content items
// are removed from a tool_result, it is replaced with a placeholder text block
// ("[Tool <name> is unavailable]") so the content array is never empty (decolua/9router #a3039ab1).
func CleanStrippedToolReferences(messages any, strippedNames map[string]bool) int {
	if len(strippedNames) == 0 || messages == nil {
		return 0
	}
	msgList, ok := messages.([]any)
	if !ok {
		return 0
	}
	cleaned := 0
	for _, msg := range msgList {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, blk := range content {
			b, ok := blk.(map[string]any)
			if !ok || b["type"] != "tool_result" {
				continue
			}
			rawInner, ok := b["content"].([]any)
			if !ok {
				continue
			}
			var kept []any
			var removedNames []string
			for _, item := range rawInner {
				itemMap, ok := item.(map[string]any)
				if ok && itemMap["type"] == "tool_reference" {
					toolName, _ := itemMap["tool_name"].(string)
					if strippedNames[toolName] {
						removedNames = append(removedNames, toolName)
						cleaned++
						continue
					}
				}
				kept = append(kept, item)
			}
			if len(removedNames) > 0 {
				if len(kept) > 0 {
					b["content"] = kept
				} else {
					lines := make([]string, 0, len(removedNames))
					for _, name := range removedNames {
						lines = append(lines, "[Tool "+name+" is unavailable]")
					}
					b["content"] = []any{
						map[string]any{
							"type": "text",
							"text": strings.Join(lines, "\n"),
						},
					}
				}
			}
		}
	}
	return cleaned
}

// declaredToolName reads the tool name in either wire shape: a bare "name" or
// an OpenAI function wrapper.
func declaredToolName(tool any) string {
	obj, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	if name, ok := obj["name"].(string); ok && name != "" {
		return name
	}
	fn, ok := obj["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fn["name"].(string)
	return name
}
