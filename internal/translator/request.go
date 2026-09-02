package translator

import (
	"9router/proxy/internal/log"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
)

var billingHeaderRegex = regexp.MustCompile(`(?i)^x-anthropic-billing-header:[^\n]*(?:\r?\n)?`)

// requiresMaxCompletionTokens reports whether model needs max_completion_tokens instead of max_tokens (PR #3657).
// Matches gpt-5.x and o1/o3/o4 with hyphen, same as JS /gpt-5|o[134]-/i
func requiresMaxCompletionTokens(model string) bool {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "gpt-5") {
		return true
	}
	if strings.Contains(lower, "o1-") || strings.Contains(lower, "o3-") || strings.Contains(lower, "o4-") {
		return true
	}
	return false
}

func stripAnthropicBillingHeader(text string) string {
	return billingHeaderRegex.ReplaceAllString(text, "")
}

func parseSystemPrompt(systemRaw jsontext.Value) string {
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

	log.Warn("translator", "parse system prompt failed")
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
			ToolCalls:        toolCalls,
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

func convertToolChoice(choiceRaw *jsontext.Value) any {
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
	log.Warn("translator", "convert tool choice failed", "raw", string(*choiceRaw))
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
	// gpt-5/o-series use max_completion_tokens (PR #3657)
	if requiresMaxCompletionTokens(creq.Model) {
		oreq.MaxCompletionTokens = creq.MaxTokens
	} else {
		oreq.MaxTokens = creq.MaxTokens
	}
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

// SanitizeClaudePassthrough drops foreign server_tool_use blocks that would poison Claude history.
// Port of decolua/9router PR #3686 (fix/claude: drop server_tool_use blocks carrying a foreign id).
// Anthropic validates server_tool_use.id against ^srvtoolu_[a-zA-Z0-9_]+$ and 400s if not matched.
// Providers like z.ai/glm emit OpenAI-style call_ ids for built-in tools (e.g. analyze_image).
// In a mixed combo those blocks stay in history and every later Claude turn fails.
// This sanitizes before forwarding to a Claude/Anthropic upstream:
//   - drops server_tool_use whose id doesn't match srvtoolu_ pattern
//   - drops paired tool_result/web_search_tool_result referencing dropped ids
//   - strips empty text blocks and drops messages that end up empty (Anthropic rejects empty content)
var serverToolUseIDRegex = regexp.MustCompile(`^srvtoolu_[a-zA-Z0-9_]+$`)

func SanitizeClaudePassthrough(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	messagesRaw, ok := req["messages"].([]any)
	if !ok || len(messagesRaw) == 0 {
		return body
	}
	droppedIDs := make(map[string]bool)
	changed := false

	// First pass: drop foreign server_tool_use and collect their ids
	for _, mRaw := range messagesRaw {
		msgMap, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		contentRaw, ok := msgMap["content"]
		if !ok {
			continue
		}
		blocks, ok := contentRaw.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, bRaw := range blocks {
			block, ok := bRaw.(map[string]any)
			if !ok {
				kept = append(kept, bRaw)
				continue
			}
			bType, _ := block["type"].(string)
			if bType == "server_tool_use" {
				id, _ := block["id"].(string)
				if !serverToolUseIDRegex.MatchString(id) {
					droppedIDs[id] = true
					changed = true
					continue
				}
			}
			// Strip empty text blocks (Anthropic 400s on empty text)
			if bType == "text" {
				if txt, _ := block["text"].(string); strings.TrimSpace(txt) == "" {
					changed = true
					continue
				}
			}
			kept = append(kept, bRaw)
		}
		if len(kept) != len(blocks) {
			msgMap["content"] = kept
		}
	}

	// Second pass: drop tool_result referencing dropped server_tool_use ids
	if len(droppedIDs) > 0 {
		for _, mRaw := range messagesRaw {
			msgMap, ok := mRaw.(map[string]any)
			if !ok {
				continue
			}
			contentRaw, ok := msgMap["content"]
			if !ok {
				continue
			}
			blocks, ok := contentRaw.([]any)
			if !ok {
				continue
			}
			var kept []any
			for _, bRaw := range blocks {
				block, ok := bRaw.(map[string]any)
				if !ok {
					kept = append(kept, bRaw)
					continue
				}
				bType, _ := block["type"].(string)
				if bType == "tool_result" || bType == "web_search_tool_result" {
					toolUseID, _ := block["tool_use_id"].(string)
					if droppedIDs[toolUseID] {
						changed = true
						continue
					}
				}
				kept = append(kept, bRaw)
			}
			if len(kept) != len(blocks) {
				msgMap["content"] = kept
			}
		}
	}

	// Third pass: drop messages that ended up with empty content (Anthropic rejects empty array)
	var filtered []any
	for _, mRaw := range messagesRaw {
		msgMap, ok := mRaw.(map[string]any)
		if !ok {
			filtered = append(filtered, mRaw)
			continue
		}
		contentRaw, exists := msgMap["content"]
		if !exists {
			filtered = append(filtered, mRaw)
			continue
		}
		switch c := contentRaw.(type) {
		case string:
			if strings.TrimSpace(c) == "" {
				changed = true
				continue
			}
		case []any:
			if len(c) == 0 {
				changed = true
				continue
			}
		}
		filtered = append(filtered, mRaw)
	}
	if len(filtered) != len(messagesRaw) {
		req["messages"] = filtered
		changed = true
	}

	if !changed {
		return body
	}
	if out, err := json.Marshal(req); err == nil {
		return out
	}
	return body
}

// DefaultClaudeToolType ensures all tools in a Claude-format request have a type (defaulting to "custom" if missing).
// Strict Anthropic-compatible gateways (e.g. MiniMax) reject payloads omitting tool type with HTTP 400.
func DefaultClaudeToolType(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	toolsRaw, ok := req["tools"].([]any)
	if !ok || len(toolsRaw) == 0 {
		return body
	}
	changed := false
	for _, t := range toolsRaw {
		if toolMap, isMap := t.(map[string]any); isMap {
			if typeVal, hasType := toolMap["type"]; !hasType || typeVal == "" || typeVal == nil {
				toolMap["type"] = "custom"
				changed = true
			}
		}
	}
	if changed {
		if out, err := json.Marshal(req); err == nil {
			return out
		}
	}
	return body
}
