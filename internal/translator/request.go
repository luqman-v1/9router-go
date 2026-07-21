package translator

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
)

var billingHeaderRegex = regexp.MustCompile(`(?i)^x-anthropic-billing-header:[^\n]*(?:\r?\n)?`)

func stripAnthropicBillingHeader(text string) string {
	return billingHeaderRegex.ReplaceAllString(text, "")
}

func parseSystemPrompt(systemRaw json.RawMessage) string {
	if len(systemRaw) == 0 {
		return ""
	}

	var sysStr string
	if err := json.Unmarshal(systemRaw, &sysStr); err == nil {
		return stripAnthropicBillingHeader(sysStr)
	}

	var sysBlocks []ClaudeSystemBlock
	if err := json.Unmarshal(systemRaw, &sysBlocks); err == nil {
		var parts []string
		for _, block := range sysBlocks {
			text := stripAnthropicBillingHeader(block.Text)
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}

	log.Printf("[translator] parseSystemPrompt: both string and block unmarshal failed")
	return ""
}

func systemReminderText(content string) string {
	text := strings.TrimSpace(content)
	if text == "" {
		return ""
	}
	return fmt.Sprintf("<instructions>\n%s\n</instructions>", text)
}

func collapseTextParts(parts []OpenAIContentBlock) any {
	if len(parts) == 1 && parts[0].Type == "text" {
		return parts[0].Text
	}
	return parts
}

func convertClaudeMessage(msg ClaudeMessage) ([]OpenAIMessage, error) {
	if msg.Role == "system" {
		var contentStr string
		if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
			rem := systemReminderText(contentStr)
			if rem != "" {
				return []OpenAIMessage{{Role: "user", Content: rem}}, nil
			}
			return nil, nil
		}

		var contentBlocks []ClaudeContentBlock
		if err := json.Unmarshal(msg.Content, &contentBlocks); err == nil {
			var textParts []string
			for _, block := range contentBlocks {
				if block.Type == "text" && block.Text != "" {
					textParts = append(textParts, block.Text)
				}
			}
			rem := systemReminderText(strings.Join(textParts, "\n"))
			if rem != "" {
				return []OpenAIMessage{{Role: "user", Content: rem}}, nil
			}
		}
		return nil, nil
	}

	role := "user"
	if msg.Role == "assistant" {
		role = "assistant"
	}

	var simpleContent string
	if err := json.Unmarshal(msg.Content, &simpleContent); err == nil {
		return []OpenAIMessage{{Role: role, Content: simpleContent}}, nil
	}

	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, err
	}

	var textParts []OpenAIContentBlock
	var toolCalls []OpenAIToolCall
	var toolResults []OpenAIMessage
	var reasoningContent string

	for _, block := range blocks {
		switch block.Type {
		case "text":
			textParts = append(textParts, OpenAIContentBlock{
				Type: "text",
				Text: block.Text,
			})
		case "thinking":
			if block.Thinking != "" {
				reasoningContent += block.Thinking
			}
		case "image":
			if block.Source != nil && block.Source.Type == "base64" {
				url := fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
				textParts = append(textParts, OpenAIContentBlock{
					Type:     "image_url",
					ImageUrl: &OpenAIImageUrl{URL: url},
				})
			}
		case "tool_use":
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   block.ID,
				Type: "function",
				Function: OpenAIFunctionCall{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		case "tool_result":
			var resultContent string
			if err := json.Unmarshal(block.Content, &resultContent); err != nil {
				var contentArr []ClaudeContentBlock
				if err2 := json.Unmarshal(block.Content, &contentArr); err2 == nil {
					var parts []string
					for _, c := range contentArr {
						if c.Type == "text" {
							parts = append(parts, c.Text)
						}
					}
					resultContent = strings.Join(parts, "\n")
				} else {
					resultContent = string(block.Content)
				}
			}
			toolResults = append(toolResults, OpenAIMessage{
				Role:       "tool",
				ToolCallID: block.ToolUseID,
				Content:    resultContent,
			})
		}
	}

	var results []OpenAIMessage
	if len(toolResults) > 0 {
		results = append(results, toolResults...)
		if len(textParts) > 0 {
			results = append(results, OpenAIMessage{
				Role:    "user",
				Content: collapseTextParts(textParts),
			})
		}
		return results, nil
	}
	if len(toolCalls) > 0 {
		msg := OpenAIMessage{
			Role:             "assistant",
			ReasoningContent: reasoningContent,
			ToolCalls: toolCalls,
		}
		if len(textParts) > 0 {
			msg.Content = collapseTextParts(textParts)
		}
		return []OpenAIMessage{msg}, nil
	}
	if len(textParts) > 0 {
		return []OpenAIMessage{{
			Role:    role,
			Content: collapseTextParts(textParts),
		}}, nil
	}
	if len(blocks) == 0 {
		return []OpenAIMessage{{Role: role, Content: ""}}, nil
	}
	return nil, nil
}

func fixMissingToolResponsesOpenAI(messages []OpenAIMessage) []OpenAIMessage {
	result := make([]OpenAIMessage, len(messages))
	copy(result, messages)
	for i := 0; i < len(result); i++ {
		msg := result[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			var toolCallIds []string
			for _, tc := range msg.ToolCalls {
				toolCallIds = append(toolCallIds, tc.ID)
			}
			respondedIds := make(map[string]bool)
			insertPosition := i + 1
			for j := i + 1; j < len(result); j++ {
				nextMsg := result[j]
				if nextMsg.Role == "tool" && nextMsg.ToolCallID != "" {
					respondedIds[nextMsg.ToolCallID] = true
					insertPosition = j + 1
				} else {
					break
				}
			}
			var missingIds []string
			for _, id := range toolCallIds {
				if !respondedIds[id] {
					missingIds = append(missingIds, id)
				}
			}
			if len(missingIds) > 0 {
				var missingResponses []OpenAIMessage
				for _, id := range missingIds {
					missingResponses = append(missingResponses, OpenAIMessage{
						Role:       "tool",
						ToolCallID: id,
						Content:    "[No response received]",
					})
				}
				temp := make([]OpenAIMessage, 0, len(result)+len(missingResponses))
				temp = append(temp, result[:insertPosition]...)
				temp = append(temp, missingResponses...)
				temp = append(temp, result[insertPosition:]...)
				result = temp
				i = insertPosition + len(missingResponses) - 1
			}
		}
	}
	return result
}

func budgetToEffort(budget int) string {
	switch {
	case budget >= 20000:
		return "high"
	case budget >= 5000:
		return "medium"
	default:
		return "low"
	}
}

func convertToolChoice(choiceRaw *json.RawMessage) any {
	if choiceRaw == nil {
		return "auto"
	}
	var choiceStr string
	if err := json.Unmarshal(*choiceRaw, &choiceStr); err == nil {
		return choiceStr
	}
	var choiceObj ClaudeToolChoice
	if err := json.Unmarshal(*choiceRaw, &choiceObj); err == nil {
		switch choiceObj.Type {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "tool":
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": choiceObj.Name,
				},
			}
		}
	}
	log.Printf("[translator] convertToolChoice: both string and object unmarshal failed for %s", string(*choiceRaw))
	return "auto"
}

// TranslateClaudeToOpenAI converts a Claude request payload to an OpenAI request payload.
func TranslateClaudeToOpenAI(claudeBody []byte) ([]byte, error) {
	var creq ClaudeRequest
	if err := json.Unmarshal(claudeBody, &creq); err != nil {
		return nil, fmt.Errorf("unmarshal Claude request body: %w", err)
	}

	var oreq OpenAIRequest
	oreq.Model = creq.Model
	oreq.Temperature = creq.Temperature
	oreq.MaxTokens = creq.MaxTokens
	oreq.Stream = creq.Stream

	sysContent := parseSystemPrompt(creq.System)
	if sysContent != "" {
		oreq.Messages = append(oreq.Messages, OpenAIMessage{
			Role:    "system",
			Content: sysContent,
		})
	}

	for i, msg := range creq.Messages {
		converted, err := convertClaudeMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("convert msg[%d]: %w", i, err)
		}
		oreq.Messages = append(oreq.Messages, converted...)
	}

	oreq.Messages = fixMissingToolResponsesOpenAI(oreq.Messages)

	if len(creq.Tools) > 0 {
		var otools []OpenAITool
		for _, tool := range creq.Tools {
			otools = append(otools, OpenAITool{
				Type: "function",
				Function: OpenAIFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			})
		}
		oreq.Tools = otools
	}

	if creq.ToolChoice != nil {
		oreq.ToolChoice = convertToolChoice(creq.ToolChoice)
	}

	// Claude thinking config → OpenAI reasoning_effort
	if creq.Thinking != nil && creq.Thinking.Type == "enabled" {
		oreq.ReasoningEffort = budgetToEffort(creq.Thinking.Budget)
	}

	out, err := json.Marshal(oreq)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI request: %w", err)
	}
	return out, nil
}
