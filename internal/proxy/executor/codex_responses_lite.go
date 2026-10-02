package executor

import "strings"

// Codex 0.155 serves some models on a different request shape: the tools and
// the instructions travel as an input prefix instead of top-level fields, and
// reasoning carries no summary. Those models are flagged `responsesLite: true`
// on their registry entry upstream (open-sse/providers/registry/codex.js); the
// flag is mirrored here because the Go catalog is a flat id list.
//
// A model that is not in this set keeps the classic shape, so this is additive
// rather than a change to the existing Codex path.

// codexResponsesLiteModels mirrors the `responsesLite: true` registry flag.
var codexResponsesLiteModels = map[string]bool{
	"gpt-6-sol":  true,
	"gpt-6-luna": true,
}

// codexResponsesLiteDefaultEffort is the effort a lite model gets when the
// client asked for none; the classic models default to low instead.
const codexResponsesLiteDefaultEffort = "medium"

// responseInputItems coerces an input/tools array to []any. The two build paths
// produce different concrete slices — []any for a body that already arrives in
// Responses shape, []map[string]any for one converted from Chat Completions —
// and the lite rewrite has to see whichever it is handed. A type it cannot read
// is reported so the caller can leave that body alone rather than silently
// replacing the conversation with the prefix.
func responseInputItems(items any) ([]any, bool) {
	switch v := items.(type) {
	case nil:
		return nil, true
	case []any:
		return v, true
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, m := range v {
			out = append(out, m)
		}
		return out, true
	default:
		return nil, false
	}
}

// isCodexResponsesLiteModel reports whether a model uses the responses-lite
// shape. A trailing "(level)" override is stripped first: that suffix is a
// 9router request override, not part of the model id.
func isCodexResponsesLiteModel(model string) bool {
	base := model
	if open := strings.LastIndex(base, "("); open != -1 && strings.HasSuffix(base, ")") {
		base = strings.TrimSpace(base[:open])
	}
	return codexResponsesLiteModels[base]
}

// applyCodexResponsesLite rewrites a Codex request into the responses-lite
// shape: the top-level tools and instructions move into the input array as a
// developer prefix, and the top-level fields are cleared. A body that already
// carries the prefix is left alone, so replaying a transcript back does not
// double-wrap it. It reports false when the body asks for hosted search — Lite
// cannot execute it, so the caller has to keep regular Responses — or when the
// input shape is one this cannot read.
func applyCodexResponsesLite(req map[string]any) bool {
	input, ok := responseInputItems(req["input"])
	if !ok {
		return false
	}
	for _, item := range input {
		if m, isMap := item.(map[string]any); isMap {
			if t, _ := m["type"].(string); t == codexAdditionalToolsType {
				return true
			}
		}
	}

	tools, _ := responseInputItems(req["tools"])
	prefix := []any{map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": tools,
	}}
	if instructions, _ := req["instructions"].(string); strings.TrimSpace(instructions) != "" {
		prefix = append(prefix, map[string]any{
			"type":    "message",
			"role":    "developer",
			"content": []any{map[string]any{"type": "input_text", "text": instructions}},
		})
	}

	req["input"] = append(prefix, input...)
	req["instructions"] = ""
	req["tools"] = nil
	if tc, _ := req["tool_choice"].(string); tc == "" {
		req["tool_choice"] = "auto"
	}
	req["parallel_tool_calls"] = false
	return true
}

// applyCodexLiteReasoning gives a lite model the reasoning block it accepts:
// an effort (defaulting higher than the classic models, which sit on low) and
// the all_turns context, but no "summary":"auto" — lite returns the reasoning
// itself and rejects the summary field.
func applyCodexLiteReasoning(req map[string]any) {
	reasoning, _ := req["reasoning"].(map[string]any)
	if reasoning == nil {
		reasoning = map[string]any{"effort": codexResponsesLiteDefaultEffort}
	} else {
		if e, _ := reasoning["effort"].(string); e == "" {
			reasoning["effort"] = codexResponsesLiteDefaultEffort
		}
	}
	delete(reasoning, "summary")
	reasoning["context"] = "all_turns"
	req["reasoning"] = reasoning
}

// applyCodexModelShape dispatches on the model: a lite one gets the prefix shape
// and the lite reasoning block, anything else keeps the classic one. Called once
// per request, after the body has been normalised to Responses shape.
//
// Hosted search is registered and lifted out of any prefix first, so a body
// asking for it stays on regular Responses rather than losing the tool inside
// the prefix (upstream 7bf931781).
func applyCodexModelShape(req map[string]any, cleanModel string) {
	if !isCodexResponsesLiteModel(cleanModel) {
		return
	}
	registered := registerCodexHostedWebSearch(req)
	if liftCodexHostedWebSearch(req) || registered {
		return
	}
	if !applyCodexResponsesLite(req) {
		return
	}
	applyCodexLiteReasoning(req)
}
