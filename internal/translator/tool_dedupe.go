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
	kept, dropped := dedupeToolList(tools)
	if dropped == 0 {
		return body
	}
	req["tools"] = kept
	// Deterministic marshaling keeps the serialized request prefix stable
	// across turns, which DeepSeek's prefix prompt cache requires;
	// json/v2 randomizes map member order by default.
	out, err := json.Marshal(req, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

// dedupeToolList drops every tool whose name was already declared and reports
// how many were removed.
func dedupeToolList(tools []any) (kept []any, dropped int) {
	seen := make(map[string]bool, len(tools))
	kept = make([]any, 0, len(tools))
	for _, tool := range tools {
		name := declaredToolName(tool)
		if name != "" {
			if seen[name] {
				dropped++
				continue
			}
			seen[name] = true
		}
		kept = append(kept, tool)
	}
	return kept, dropped
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
