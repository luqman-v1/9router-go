package thinking

import (
	"bytes"
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
		n, err := strconv.Atoi(raw)
		if err != nil {
			// A number that does not fit is not a budget: the suffix is still
			// stripped, but no intent is expressed.
			return clean, nil
		}
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

// stripKeys are the top-level reasoning dialect fields, in the order
// upstream's stripAll removes them.
var stripKeys = []string{
	"thinking", "reasoning_effort", "reasoning", "thinkingConfig",
	"enable_thinking", "thinking_budget", "output_config",
}

// Strip ports stripAll: deletes every known thinking field, verbatim order.
// The bool reports whether anything was removed, so a caller can tell a body
// the pass left alone from one it rewrote.
func Strip(body map[string]any) bool {
	changed := false
	for _, k := range stripKeys {
		if _, has := body[k]; has {
			delete(body, k)
			changed = true
		}
	}
	if gc, ok := body["generationConfig"].(map[string]any); ok {
		if _, has := gc["thinkingConfig"]; has {
			delete(gc, "thinkingConfig")
			changed = true
		}
	}
	if req, ok := body["request"].(map[string]any); ok {
		if gc, ok := req["generationConfig"].(map[string]any); ok {
			if _, has := gc["thinkingConfig"]; has {
				delete(gc, "thinkingConfig")
				changed = true
			}
		}
	}
	if params, ok := body["params"].(map[string]any); ok {
		for _, k := range []string{"reasoning_effort", "thinking"} {
			if _, has := params[k]; has {
				delete(params, k)
				changed = true
			}
		}
	}
	return changed
}

// dialectKeyPresent reports whether the body carries a reasoning dialect
// field. Strip removes every one of them and each writer sets exactly one,
// so the body differs from its input exactly when Strip removed something or
// one of these keys is present again.
func dialectKeyPresent(body map[string]any) bool {
	for _, k := range stripKeys {
		if _, has := body[k]; has {
			return true
		}
	}
	if gc, ok := body["generationConfig"].(map[string]any); ok {
		if _, has := gc["thinkingConfig"]; has {
			return true
		}
	}
	if req, ok := body["request"].(map[string]any); ok {
		if gc, ok := req["generationConfig"].(map[string]any); ok {
			if _, has := gc["thinkingConfig"]; has {
				return true
			}
		}
	}
	if params, ok := body["params"].(map[string]any); ok {
		for _, k := range []string{"reasoning_effort", "thinking"} {
			if _, has := params[k]; has {
				return true
			}
		}
	}
	return false
}

// NormalizeThinkingConfig ports services/provider.js normalizeThinkingConfig:
// a `thinking` block is request-scoped tool-state, valid only when the
// conversation ends on a user turn. OpenAI reasoning_effort is request-level
// and survives. Empty/absent messages → no-op (isLastMessageFromUser true).
// The bool reports whether the block was dropped.
func NormalizeThinkingConfig(body map[string]any) bool {
	if isLastMessageFromUser(body) {
		return false
	}
	if _, has := body["thinking"]; !has {
		return false
	}
	delete(body, "thinking")
	return true
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

// snapshotKeyBytes is SnapshotKeys as the byte prefixes a JSON key starts
// with. The quotes are stripped and one leading quote is put back, so the
// prefix is the key's first characters: "thinking also covers thinkingConfig
// and thinking_budget, and "reasoning covers reasoning_effort.
var snapshotKeyBytes = func() [][]byte {
	out := make([][]byte, 0, len(SnapshotKeys))
	for _, k := range SnapshotKeys {
		out = append(out, []byte(`"`+strings.Trim(k, `"`)))
	}
	return out
}()

// BodyMentionsThinking reports whether raw JSON likely carries a thinking
// field, so hot paths can skip decode. Case-sensitive; keys are lowercase
// on the wire.
//
// The scan matches a JSON KEY, not the word: a user message that talks about
// reasoning is not a thinking field, and treating it as one would defeat the
// byte-identical fast path for ordinary traffic. A prefix match is the start
// of a longer key ("reasoning_effort"), so the key ends at the next quote
// and only a colon after it makes the match a key.
func BodyMentionsThinking(raw []byte) bool {
	for _, k := range snapshotKeyBytes {
		for off := 0; off < len(raw); {
			rel := bytes.Index(raw[off:], k)
			if rel == -1 {
				break
			}
			i := off + rel + len(k)
			end := bytes.IndexByte(raw[i:], '"')
			if end == -1 {
				break
			}
			off = i + end + 1
			if j := skipKeySpace(raw, off); j < len(raw) && raw[j] == ':' {
				return true
			}
		}
	}
	return false
}

// ModelSuffixIntent returns the intent the body's own "model" field carries in
// a "(level)" suffix, or nil when it carries none.
//
// The model field is the name the client asked for, so a combo or an alias
// name carries the suffix with it — and the suffix has to come off that name
// before it is resolved, or "mycombo(high)" resolves to neither.
func ModelSuffixIntent(body []byte) *Intent {
	model := bodyModelValue(body)
	if model == "" {
		return nil
	}
	_, intent := ParseSuffix(model)
	return intent
}

// bodyModelValue returns the body's "model" field as a string, or "" when the
// body carries none. A model id has no escapes, so the value ends at the
// next quote.
func bodyModelValue(body []byte) string {
	for off := 0; off < len(body); {
		rel := bytes.Index(body[off:], []byte(`"model"`))
		if rel == -1 {
			return ""
		}
		i := off + rel + len(`"model"`)
		off = i
		i = skipJSONSpace(body, i)
		if i >= len(body) || body[i] != '"' {
			continue
		}
		end := bytes.IndexByte(body[i+1:], '"')
		if end == -1 {
			return ""
		}
		return string(body[i+1 : i+1+end])
	}
	return ""
}

// skipKeySpace returns the index of the first byte at or after i that is not
// JSON whitespace.
func skipKeySpace(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}
