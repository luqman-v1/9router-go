package translator

import (
	"encoding/json/jsontext"
	"strings"
)

// responsesRequestConv accumulates the Chat messages while walking the input
// items, mirroring the local variable group of openaiResponsesToOpenAIRequest.
type responsesRequestConv struct {
	messages []any

	// assistant holds the tool-calling assistant turn being assembled. It is only
	// pushed once a later item (or the end of input) proves no more tool calls
	// belong to it, so consecutive calls collapse into one message.
	assistant   map[string]any
	toolResults []any

	// Reasoning arrives as its own input item, ahead of the assistant turn it
	// belongs to, so it is buffered and attached when that turn is built.
	reasoning    string
	reasoningEnc string

	additionalTools  []map[string]any
	customToolNames  map[string]bool
	customToolNameIn []string
}

func newResponsesRequestConv() *responsesRequestConv {
	return &responsesRequestConv{
		messages:        []any{},
		customToolNames: map[string]bool{},
	}
}

func (c *responsesRequestConv) flushAssistant() {
	if c.assistant != nil {
		c.messages = append(c.messages, c.assistant)
		c.assistant = nil
	}
}

func (c *responsesRequestConv) flushToolResults() {
	if len(c.toolResults) == 0 {
		return
	}
	c.messages = append(c.messages, c.toolResults...)
	c.toolResults = nil
}

// attachReasoning moves the buffered reasoning onto the message that follows it.
func (c *responsesRequestConv) attachReasoning(msg map[string]any) {
	if c.reasoning != "" {
		msg["reasoning_content"] = c.reasoning
	}
	// The encrypted blob is what restores store=false continuity on a later hop
	// (Grok CLI / Codex multi-turn), so it rides along with the visible text.
	if c.reasoningEnc != "" {
		msg["encrypted_content"] = c.reasoningEnc
	}
	c.reasoning = ""
	c.reasoningEnc = ""
}

func (c *responsesRequestConv) discardReasoning() {
	c.reasoning = ""
	c.reasoningEnc = ""
}

func (c *responsesRequestConv) consumeItems(items []ResponsesInputItem) {
	for _, item := range items {
		itemType := item.Type
		if itemType == "" && item.Role != "" {
			// Droid CLI sends role-based items with no type field.
			itemType = responsesItemMessage
		}

		switch itemType {
		case responsesItemMessage:
			c.consumeMessage(item)
		case responsesItemFunctionCall, responsesItemCustomToolCall:
			c.consumeToolCall(item, itemType)
		case responsesItemFunctionCallOutput, responsesItemCustomToolCallOutput:
			c.consumeToolResult(item)
		case responsesItemAdditionalTools:
			c.additionalTools = append(c.additionalTools, item.Tools...)
		case responsesItemReasoning:
			c.consumeReasoning(item)
		}
	}
}

func (c *responsesRequestConv) consumeMessage(item ResponsesInputItem) {
	c.flushAssistant()
	c.flushToolResults()

	content, err := convertResponsesContent(item.Content)
	if err != nil {
		content = jsontext.Value(nil)
	}

	msg := map[string]any{"role": item.Role, "content": content}
	if item.Role == responsesAssistantRole {
		c.attachReasoning(msg)
	} else {
		c.discardReasoning()
	}
	c.messages = append(c.messages, msg)
}

func (c *responsesRequestConv) consumeToolCall(item ResponsesInputItem, itemType string) {
	if c.assistant == nil {
		c.assistant = map[string]any{
			"role":       responsesAssistantRole,
			"content":    nil,
			"tool_calls": []any{},
		}
		c.attachReasoning(c.assistant)
	}

	name := strings.TrimPrefix(item.Name, "functions.")
	// Codex and OpenAI reject a nameless tool call outright, so an item that
	// carries no usable name is skipped rather than forwarded.
	if strings.TrimSpace(name) == "" {
		return
	}
	if itemType == responsesItemCustomToolCall || strings.HasPrefix(item.Name, "functions.") {
		c.markCustomTool(name)
	}

	call := map[string]any{
		"id":   item.CallID,
		"type": openaiBlockFunction,
		"function": map[string]any{
			"name":      name,
			"arguments": toolCallArguments(item, itemType),
		},
	}
	c.assistant["tool_calls"] = append(c.assistant["tool_calls"].([]any), call)
}

func (c *responsesRequestConv) consumeToolResult(item ResponsesInputItem) {
	c.flushAssistant()
	c.flushToolResults()

	c.messages = append(c.messages, map[string]any{
		"role":         roleTool,
		"tool_call_id": item.CallID,
		"content":      rawToJSONString(item.Output),
	})
}

func (c *responsesRequestConv) consumeReasoning(item ResponsesInputItem) {
	if txt := responsesReasoningText(item); txt != "" {
		if c.reasoning != "" {
			c.reasoning += "\n" + txt
		} else {
			c.reasoning = txt
		}
	}
	if item.EncryptedContent != "" {
		c.reasoningEnc = item.EncryptedContent
	}
}

func (c *responsesRequestConv) markCustomTool(name string) {
	name = strings.TrimPrefix(name, "functions.")
	if name != "" && !c.customToolNames[name] {
		c.customToolNames[name] = true
		c.customToolNameIn = append(c.customToolNameIn, name)
	}
}
