package translator

import (
	json "encoding/json/v2"
	"strings"
)

// OpenCode 1.x/2.x fires tool-less title-generation requests: a short
// conversation whose system prompt asks for a title and nothing else. The
// free-tier gate accepts those, but answers 403 FreeTierError or 429
// FreeUsageLimitError once agent tool declarations are attached — so shaping a
// title request like an agent turn is what gets it refused, and the router then
// spends an entire proxy-pool walk retrying a request that should never have
// carried tools.
//
// Detection is deliberately conservative: the structural conditions alone are
// not enough, because a genuine short tool-less turn is a perfectly normal
// request that must keep the agent shaping. A known opener has to match as
// well, so the failure mode of a false negative is "behaves exactly as today"
// rather than "a real turn loses its tools".

// openCodeTitleOpeners are the request openers the title generation prompts
// use. Matched case-insensitively against the system text and the last user
// turn. Kept in one slice so a reworded upstream prompt is one edit away, and
// so the list is greppable from the audit that introduced it.
var openCodeTitleOpeners = []string{
	"generate a title for this conversation",
	"you name conversations so the user can find them later",
	"you generate a descriptive title",
	"whether the session title still matches the conversation",
	"reply with a title and nothing else",
	"reply with a title for it and nothing else",
}

// openCodeTitleMaxMessages bounds the conversation a title request may carry.
// The prompts summarize a conversation, so they see a short tail of it.
const openCodeTitleMaxMessages = 3

// IsOpenCodeTitleRequest reports whether body is a tool-less title-generation
// call rather than an agent turn.
//
// It requires every one of: at most openCodeTitleMaxMessages turns, no tool
// declarations of any spelling, no assistant or tool turn (a title request is
// one system instruction plus user text), and a known title opener.
func IsOpenCodeTitleRequest(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	if hasOpenCodeToolDeclarations(request) {
		return false
	}
	turns, ok := openCodeTurnsOf(request)
	if !ok || len(turns) == 0 || len(turns) > openCodeTitleMaxMessages {
		return false
	}
	for _, turn := range turns {
		switch roleOf(turn) {
		case "assistant", "tool", "function":
			return false
		}
	}
	return matchesOpenCodeTitleOpener(openCodePromptText(request, turns))
}

// hasOpenCodeToolDeclarations reports whether any tool spelling is present.
// A body carrying tools is an agent turn however it is worded.
func hasOpenCodeToolDeclarations(request map[string]any) bool {
	for _, key := range []string{"tools", "functions", "tool_choice", "tool_config"} {
		if value, ok := request[key]; ok && value != nil {
			if list, isList := value.([]any); isList && len(list) == 0 {
				continue
			}
			return true
		}
	}
	return false
}

// openCodeTurnsOf returns the conversation turns of a request in whichever
// shape it arrived: Chat/Claude "messages" or Responses "input".
func openCodeTurnsOf(request map[string]any) ([]map[string]any, bool) {
	if messages, ok := request["messages"].([]any); ok {
		return openCodeTurnMaps(messages), true
	}
	if input, ok := request["input"].([]any); ok {
		return openCodeTurnMaps(input), true
	}
	return nil, false
}

func openCodeTurnMaps(items []any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// roleOf reads a turn's role, which Responses spells as "type":"message" with
// a nested role, so both shapes resolve.
func roleOf(turn map[string]any) string {
	if role, _ := turn["role"].(string); role != "" {
		if turn["type"] == "function_call_output" {
			return "tool"
		}
		return strings.ToLower(strings.TrimSpace(role))
	}
	if kind, _ := turn["type"].(string); kind != "" {
		switch kind {
		case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
			return "tool"
		}
	}
	return ""
}

// openCodePromptText builds the lowercased haystack the opener list is matched
// against: the system/instructions text plus the user turns.
func openCodePromptText(request map[string]any, turns []map[string]any) string {
	var sb strings.Builder
	sb.WriteString(strings.ToLower(openCodeSystemText(request)))
	for _, turn := range turns {
		switch roleOf(turn) {
		case "system", "developer", "user", "":
			sb.WriteByte('\n')
			sb.WriteString(strings.ToLower(openCodeTurnText(turn)))
		}
	}
	return sb.String()
}

// openCodeSystemText concatenates whichever system carrier the body uses.
func openCodeSystemText(request map[string]any) string {
	var sb strings.Builder
	if instructions, ok := request["instructions"].(string); ok {
		sb.WriteString(instructions)
	}
	switch system := request["system"].(type) {
	case string:
		sb.WriteByte('\n')
		sb.WriteString(system)
	case []any:
		for _, block := range system {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					sb.WriteByte('\n')
					sb.WriteString(text)
				}
			}
		}
	}
	return sb.String()
}

// openCodeTurnText flattens a turn's content to text.
func openCodeTurnText(turn map[string]any) string {
	switch content := turn["content"].(type) {
	case string:
		return content
	case []any:
		var sb strings.Builder
		for _, block := range content {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					sb.WriteByte('\n')
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	}
	return ""
}

func matchesOpenCodeTitleOpener(haystack string) bool {
	for _, opener := range openCodeTitleOpeners {
		if strings.Contains(haystack, opener) {
			return true
		}
	}
	return false
}

// openCodeTitleNote overrides the harness preamble's Code Mode section for a
// title request. The preamble says to call the tools "listed below", but a
// title request declares none, so without this the model is told to reach for
// a tool set that is not there.
const openCodeTitleNote = "The Code Mode and tool-catalog instructions above do not apply to this session. No tools are available; answer directly."

// ShapeOpenCodeV2TitleRequest shapes a title-generation body for the free-tier
// gate: the harness preamble stays (the gate fingerprints it, and a request
// without it is refused), but the agent tool set is neither installed nor
// described.
//
// That is the whole difference from ConcealOpenCodeV2FingerprintTools, and it
// is the difference that matters — the gate accepts a tool-less title call and
// refuses the same call once agent declarations are attached. flat is true for
// Responses bodies, false for Chat Completions; a Claude Messages body is
// detected and shaped in its own format.
//
// Callers must gate this on IsOpenCodeTitleRequest: with no tools declared, a
// turn that needed one cannot use it, so it must never run for an agent turn.
func ShapeOpenCodeV2TitleRequest(body []byte, flat bool) []byte {
	if len(body) == 0 {
		return body
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return body
	}
	claude := isOpenCodeClaudeShapedRequest(request)
	suffix := "\n\n" + openCodeTitleNote
	// Carry the session's real model, same as the agent path: the harness
	// preamble's "# Your Model" is a fixed capture, and a title request should
	// not report a model that is not the one answering.
	if identityNote := openCodeModelIdentityNote(request); identityNote != "" {
		suffix += "\n\n" + identityNote
	}
	injectOpenCodeHarnessSuffix(request, flat, claude, suffix)
	out, err := json.Marshal(request, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}
