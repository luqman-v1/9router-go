package chat

import (
	"9router/proxy/internal/translator"
	json "encoding/json/v2"
)

func bodySpeaksOpenAIChat(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	if tools, ok := req["tools"].([]any); ok {
		for _, tRaw := range tools {
			tool, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if _, has := tool["function"]; has {
				return true
			}
			if typ, _ := tool["type"].(string); typ == "function" {
				return true
			}
		}
	}
	if msgs, ok := req["messages"].([]any); ok {
		for _, mRaw := range msgs {
			msg, ok := mRaw.(map[string]any)
			if !ok {
				continue
			}
			if _, has := msg["tool_calls"]; has {
				return true
			}
			if role, _ := msg["role"].(string); role == "tool" {
				return true
			}
		}
	}
	return false
}

// claudeBodyForNonClaudeUpstream converts a Messages-shaped body to Chat
// Completions for an upstream that does not answer Messages itself.
//
// It exists because a combo fixes the body's wire format at its FIRST member:
// a combo whose first seat is Anthropic leaves a /v1/messages client's body in
// Messages shape for every seat behind it. The conversions at the upstreamClaude
// branch below only repair the other direction (OpenAI body → Claude upstream),
// so a Claude body used to reach a Responses upstream — the call sites in
// buildResponsesBody keep only content blocks carrying a "text" key, which drops
// every tool_use and tool_result. The model then never saw its tool output and
// re-issued the same command indefinitely. Reported against combos whose
// Anthropic seat is rate-limited, so the turn lands on cx/gpt-6.x every time.
//
// upstreamSpeaksClaude is the candidate's own lane decision (claudeNative in
// tryForwardWithConnection): a provider that serves the Messages endpoint —
// minimax-code, opencode-zen's Claude lane — must keep receiving Messages, so
// only the providers that do not are converted here.
func claudeBodyForNonClaudeUpstream(body []byte, upstreamSpeaksClaude bool, provider, model string) []byte {
	// A body that already speaks Chat is never converted, whatever blocks its
	// user content happens to carry: converting it again would rebuild the
	// conversation from fields Chat does not use and drop the system turn it
	// keeps inside messages. The two probes are exclusive by construction.
	if upstreamSpeaksClaude || bodySpeaksOpenAIChat(body) || !bodySpeaksClaudeMessages(body) {
		return body
	}
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return body
	}
	converted, err := translator.TranslateClaudeToOpenAI(body, provider, model)
	if err != nil || len(converted) == 0 {
		return body
	}
	// The converter writes its own "stream": false. Carrying the client's flag
	// over keeps a streamed combo turn streamed instead of handing the client a
	// buffered answer it reads as a dead connection.
	stream, hadStream := src["stream"]
	if !hadStream {
		return converted
	}
	var dst map[string]any
	if err := json.Unmarshal(converted, &dst); err != nil {
		return converted
	}
	dst["stream"] = stream
	out, err := json.Marshal(dst)
	if err != nil {
		return converted
	}
	return out
}

// bodySpeaksClaudeMessages reports whether body is an Anthropic Messages
// request rather than Chat Completions.
//
// It keys on what only Messages carries: a top-level `system` (Chat puts the
// system turn inside messages), tools declared with `input_schema` (Chat uses
// `function.parameters`), and the block types Chat has no spelling for. A plain
// text-only Messages body with neither system nor tools is left alone on
// purpose — it is wire-compatible either way, and guessing wrong would hand
// TranslateClaudeToOpenAI a body it must not touch.
func bodySpeaksClaudeMessages(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	if _, ok := req["system"]; ok {
		return true
	}
	if tools, ok := req["tools"].([]any); ok {
		for _, tRaw := range tools {
			tool, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if _, has := tool["input_schema"]; has {
				return true
			}
		}
	}
	if msgs, ok := req["messages"].([]any); ok {
		for _, mRaw := range msgs {
			msg, ok := mRaw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := msg["role"].(string)
			blocks, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, bRaw := range blocks {
				block, ok := bRaw.(map[string]any)
				if !ok {
					continue
				}
				// Paired with the role the Messages API requires, so content a
				// Chat request happens to shape this way cannot steer the
				// conversion: a tool_use belongs to an assistant turn and a
				// tool_result to a user turn.
				switch block["type"] {
				case "tool_use", "thinking", "redacted_thinking", "server_tool_use":
					if role == "assistant" {
						return true
					}
				case "tool_result", "web_search_tool_result", "document":
					if role == "user" {
						return true
					}
				}
			}
		}
	}
	return false
}

func detectReaFormat(body []byte, claudeNative bool) string {
	if claudeNative {
		return "claude"
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "openai"
	}
	if _, ok := m["conversationState"]; ok {
		return "kiro"
	}
	if _, ok := m["systemInstruction"]; ok {
		return "gemini"
	}
	if _, ok := m["system_instruction"]; ok {
		return "gemini"
	}
	if _, ok := m["instructions"]; ok {
		if _, ok := m["input"]; ok {
			return "openai-responses"
		}
	}
	if _, ok := m["input"]; ok {
		// responses if input is array of items
		if arr, ok := m["input"].([]any); ok && len(arr) > 0 {
			if it, ok := arr[0].(map[string]any); ok {
				if _, ok := it["type"]; ok {
					return "openai-responses"
				}
			}
		}
	}
	return "openai"
}
