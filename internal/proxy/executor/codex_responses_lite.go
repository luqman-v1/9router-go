package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// Unlike upstream's model flag, Lite is selected only by the incoming contract.
// Classic allows parallel tool calls on models which upstream defaults to Lite.
const CodexResponsesLiteHeader = "x-openai-internal-codex-responses-lite"

type codexLiteContextKey struct{}

// WithCodexLiteHeader preserves the caller's explicit Lite choice across routing.
func WithCodexLiteHeader(ctx context.Context, value string) context.Context {
	if !strings.EqualFold(strings.TrimSpace(value), "true") {
		return ctx
	}
	return context.WithValue(ctx, codexLiteContextKey{}, true)
}

func codexLiteRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	requested, _ := ctx.Value(codexLiteContextKey{}).(bool)
	return requested
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

// applyCodexResponsesLite rewrites a Codex request into the responses-lite
// shape: the top-level tools and instructions move into the input array as a
// developer prefix, and the top-level fields are cleared. Existing prefixes
// remain intact; distinct top-level tools gain a separate prefix. It reports
// false when the input shape cannot be read.
func applyCodexResponsesLite(req map[string]any) bool {
	input, ok := responseInputItems(req["input"])
	if !ok {
		return false
	}
	tools, _ := responseInputItems(req["tools"])
	// Native Codex prefixes have stable IDs and may appear at different points
	// in history. Never merge, move or rebuild them: only add missing top-level
	// tools as a separate prefix, preserving all client-supplied items verbatim.
	if hasCodexLitePrefix(req) {
		addCodexLiteTopLevel(req, input, tools)
		finishCodexLiteFields(req)
		return true
	}
	if tools == nil {
		tools = []any{}
	}
	prefix := []any{map[string]any{"type": codexAdditionalToolsType, "role": "developer", "tools": tools}}
	if instructions, _ := req["instructions"].(string); strings.TrimSpace(instructions) != "" {
		prefix = append(prefix, map[string]any{
			"type":    "message",
			"role":    "developer",
			"content": []any{map[string]any{"type": "input_text", "text": instructions}},
		})
	}

	req["input"] = append(prefix, input...)
	finishCodexLiteFields(req)
	return true
}

func addCodexLiteTopLevel(req map[string]any, input, tools []any) {
	prefixLists, _ := splitCodexLitePrefix(input)
	seen := make(map[string]bool)
	for _, list := range prefixLists {
		for _, tool := range list {
			if key, err := marshalStable(tool); err == nil {
				seen[string(key)] = true
			}
		}
	}
	var missing []any
	for _, tool := range tools {
		key, err := marshalStable(tool)
		if err == nil && seen[string(key)] {
			continue
		}
		missing = append(missing, tool)
		if err == nil {
			seen[string(key)] = true
		}
	}
	var prefix []any
	if len(missing) > 0 {
		prefix = append(prefix, map[string]any{"type": codexAdditionalToolsType, "role": "developer", "tools": missing})
	}
	if instructions, _ := req["instructions"].(string); strings.TrimSpace(instructions) != "" {
		prefix = append(prefix, map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": instructions}}})
	}
	if len(prefix) > 0 {
		req["input"] = append(prefix, input...)
	}
}

func finishCodexLiteFields(req map[string]any) {
	req["instructions"] = ""
	req["tools"] = nil
	if tc, ok := req["tool_choice"]; !ok || tc == nil || tc == "" {
		req["tool_choice"] = "auto"
	}
	req["parallel_tool_calls"] = false
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

// shapeCodexResponsesBody owns both the wire shape and the header decision.
// Hosted search takes precedence over Lite, as in upstream 7bf931781.
func shapeCodexResponsesBody(body []byte, requested bool) ([]byte, bool, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false, fmt.Errorf("decode Responses body: %w", err)
	}
	translateCodexClaudeWebSearch(req)
	translateCodexClaudeWebSearchChoice(req)
	registerCodexHostedWebSearch(req)
	lite := requested || hasCodexLitePrefix(req)
	if liftCodexHostedWebSearch(req) {
		wasLite := lite
		lite = false
		if reasoning, ok := req["reasoning"].(map[string]any); ok && wasLite {
			delete(reasoning, "context")
			if reasoning["summary"] == nil {
				reasoning["summary"] = "auto"
			}
		}
	}
	if lite {
		if !applyCodexResponsesLite(req) {
			return nil, false, fmt.Errorf("codex lite requires an input array")
		}
		applyCodexLiteReasoning(req)
	}
	out, err := marshalStable(req)
	if err != nil {
		return nil, false, fmt.Errorf("encode Responses body: %w", err)
	}
	return out, lite, nil
}

func hasCodexLitePrefix(req map[string]any) bool {
	input, ok := responseInputItems(req["input"])
	if !ok {
		return false
	}
	for _, item := range input {
		if m, ok := item.(map[string]any); ok && m["type"] == codexAdditionalToolsType {
			return true
		}
	}
	return false
}
