package agy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatRequest is the subset of an OpenAI Chat Completions body the ACP
// adapter needs. Unknown fields are preserved in Raw for passthrough only.
type ChatRequest struct {
	Model     string          `json:"model"`
	Messages  []ChatMessage   `json:"messages"`
	Stream    bool            `json:"stream"`
	Tools     json.RawMessage `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

// gatewayPreamble is prepended to every folded prompt. agy 1.5.x sometimes
// answers a turn with a single "checking model/config in background" status
// line and then emits no further chunks, leaving the client waiting forever
// with no answer. The preamble tells the agent to finish the answer in the
// same turn; it is protocol data for this gateway, not a user message.
const gatewayPreamble = `[9router gateway instruction]
Answer the request below directly and completely in THIS turn. Do not reply
with a status note such as "checking model and configuration in the
background" and do not defer the answer to a later message. Any progress
notice must be followed in the same turn by the actual answer, or by a tool
call whose result you will use to produce the answer. If you cannot answer,
end the turn with a short error explanation.
`

type ChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

func (m ChatMessage) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	// Content may be an array of {type:text,text:...} blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) == nil {
		var out strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				out.WriteString(b.Text)
			}
		}
		return out.String()
	}
	return ""
}

// PromptBlock is one ACP content block.
type PromptBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// BuildPrompt folds an OpenAI message history into one ACP prompt using the
// labelled format proven by upstream devin-cli.js. Tool calls and results are
// rendered explicitly so agy treats a returned result as data, not as a
// fresh instruction or prompt injection.
func BuildPrompt(req *ChatRequest) []PromptBlock {
	var out strings.Builder
	out.WriteString(gatewayPreamble)
	out.WriteString("\n\n")
	for _, m := range req.Messages {
		text := m.text()
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var calls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(m.ToolCalls, &calls) == nil {
				for _, c := range calls {
					text += fmt.Sprintf("\n[Tool call %s id=%s]\n%s\n", c.Function.Name, c.ID, c.Function.Arguments)
				}
			}
		}
		if m.Role == "tool" {
			text = fmt.Sprintf("[Tool result id=%s]\n%s", m.ToolCallID, text)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		label := "User"
		switch m.Role {
		case "system", "developer":
			label = "System"
		case "assistant":
			label = "Assistant"
		case "tool":
			label = "Tool"
		}
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString("[" + label + "]\n" + text)
	}
	if out.Len() == 0 {
		out.WriteString("(empty)")
	}
	return []PromptBlock{{Type: "text", Text: out.String()}}
}

// SessionUpdate is the ACP notification payload relevant to streaming.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Content       struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stopReason"`
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title"`
	RawInput   any    `json:"rawInput"`
}

// UpdateKind classifies an ACP update for the executor.
type UpdateKind int

const (
	UpdateIgnore UpdateKind = iota
	UpdateText
	UpdateThinking
	UpdateToolCall
	UpdateDone
	UpdateError
)

// ParseUpdate interprets a session/update notification. It tolerates both the
// flat and the nested (params.update.sessionUpdate) shapes seen across ACP
// versions, and the legacy snake_case method used by some agy builds.
func ParseUpdate(raw json.RawMessage) (kind UpdateKind, text string, update SessionUpdate, err error) {
	var outer struct {
		Update  json.RawMessage `json:"update"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Message string          `json:"message"`
	}
	if err = json.Unmarshal(raw, &outer); err != nil {
		return UpdateIgnore, "", update, err
	}
	payload := raw
	if len(outer.Update) > 0 {
		payload = outer.Update
	}
	if err = json.Unmarshal(payload, &update); err != nil {
		return UpdateIgnore, "", update, err
	}
	typ := update.SessionUpdate
	if typ == "" {
		typ = outer.Type
	}
	switch typ {
	case "agent_message_chunk", "message_delta", "text_delta", "content_delta":
		return UpdateText, update.Content.Text, update, nil
	case "agent_thought_chunk", "thought_delta":
		return UpdateThinking, update.Content.Text, update, nil
	case "tool_call", "tool_call_update":
		return UpdateToolCall, "", update, nil
	case "message_stop", "stop", "done":
		return UpdateDone, "", update, nil
	case "error":
		return UpdateError, outer.Message, update, nil
	default:
		return UpdateIgnore, "", update, nil
	}
}

// FinalResult extracts text and stopReason from a session/prompt response.
func FinalResult(raw json.RawMessage) (text, stopReason string) {
	var result struct {
		StopReason string `json:"stopReason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", ""
	}
	var out strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	return out.String(), result.StopReason
}
