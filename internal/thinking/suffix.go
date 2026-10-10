package thinking

import (
	"maps"
	"strconv"
	"strings"
)

// ParseSuffix ports parseSuffix: "model(value)" → clean id + override intent.
// The model is ALWAYS stripped on match, even when the value is unknown
// (override nil) — the suffix is a 9router request override, not the model id.
// Last "(" wins (greedy upstream `.*`); inner must hold no parens; trailing
// spaces after ")" allowed (upstream `\s*$`).
func ParseSuffix(model string) (string, *Intent) {
	trimmed := strings.TrimRight(model, " \t")
	if !strings.HasSuffix(trimmed, ")") {
		return model, nil
	}
	open := strings.LastIndex(trimmed, "(")
	if open == -1 {
		return model, nil
	}
	inner := trimmed[open+1 : len(trimmed)-1]
	if inner == "" || strings.ContainsAny(inner, "()") {
		return model, nil
	}
	clean := strings.TrimSpace(trimmed[:open])
	raw := strings.ToLower(strings.TrimSpace(inner))
	switch {
	case raw == "none" || raw == "off":
		return clean, &Intent{Mode: ModeNone}
	case raw == "auto":
		return clean, &Intent{Mode: ModeAuto}
	case raw == "ultra":
		return clean, &Intent{Mode: ModeLevel, Level: "ultra"}
	case allDigits(raw):
		n, _ := strconv.Atoi(raw)
		return clean, &Intent{Mode: ModeBudget, Budget: n}
	default:
		if _, ok := LevelToBudget[raw]; ok {
			return clean, &Intent{Mode: ModeLevel, Level: raw}
		}
		return clean, nil
	}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range s {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// StripSuffix ports stripThinkingSuffix.
func StripSuffix(model string) string {
	clean, _ := ParseSuffix(model)
	return clean
}

// Strip ports stripAll: deletes every known thinking field, verbatim order.
func Strip(body map[string]any) {
	delete(body, "thinking")
	delete(body, "reasoning_effort")
	delete(body, "reasoning")
	delete(body, "thinkingConfig")
	delete(body, "enable_thinking")
	delete(body, "thinking_budget")
	delete(body, "output_config")
	if gc, ok := body["generationConfig"].(map[string]any); ok {
		delete(gc, "thinkingConfig")
	}
	if req, ok := body["request"].(map[string]any); ok {
		if gc, ok := req["generationConfig"].(map[string]any); ok {
			delete(gc, "thinkingConfig")
		}
	}
	if params, ok := body["params"].(map[string]any); ok {
		delete(params, "reasoning_effort")
		delete(params, "thinking")
	}
}

// NormalizeThinkingConfig ports services/provider.js normalizeThinkingConfig:
// a `thinking` block is request-scoped tool-state, valid only when the
// conversation ends on a user turn. OpenAI reasoning_effort is request-level
// and survives. Empty/absent messages → no-op (isLastMessageFromUser true).
func NormalizeThinkingConfig(body map[string]any) {
	if !isLastMessageFromUser(body) {
		delete(body, "thinking")
	}
}

func isLastMessageFromUser(body map[string]any) bool {
	msgs, ok := body["messages"].([]any)
	if !ok {
		msgs, _ = body["contents"].([]any)
	}
	if len(msgs) == 0 {
		return true
	}
	last, ok := msgs[len(msgs)-1].(map[string]any)
	if !ok {
		return true
	}
	role, _ := last["role"].(string)
	return role == "user"
}

// SnapshotKeys exposes the wire keys this package reads, for fast-path scans.
// `"reasoning` is a prefix: it catches both `reasoning` and `reasoning_effort`.
// `"thinking` likewise catches `thinking`, `thinkingConfig`, `thinking_budget`.
var SnapshotKeys = []string{
	`"thinking`, `"reasoning`, `"output_config"`, `"enable_thinking"`,
}

// BodyMentionsThinking reports whether raw JSON likely carries a thinking
// field, so hot paths can skip decode. Case-sensitive; keys are lowercase
// on the wire.
func BodyMentionsThinking(raw []byte) bool {
	s := string(raw)
	for _, k := range SnapshotKeys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// cloneShallow copies one map level for tests/docs helpers.
func cloneShallow(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}
