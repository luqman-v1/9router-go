package detection

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// truncateRuneSafe limits a string to maxRunes without splitting UTF-8 characters.
func truncateRuneSafe(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}

// DetectLoops runs deterministic detection rules over a list of tool calls and errors.
func DetectLoops(tools []ToolCallItem, lastError string) []LoopFinding {
	var findings []LoopFinding

	if len(tools) == 0 {
		return findings
	}

	// Rule 1: consecutive_tool
	// Scan segments. We prioritize trailing segments (closer to session end) so active
	// loops appear at findings[0] rather than ancient loops from early turns.
	var consecutiveFindings []LoopFinding
	for i := 0; i < len(tools); {
		name := tools[i].Name
		if name == "" {
			i++
			continue
		}
		end := i + 1
		for end < len(tools) && tools[end].Name == name {
			end++
		}
		length := end - i
		if length >= 3 {
			severity := SeverityWarning
			if length >= 5 {
				severity = SeverityCritical
			}
			consecutiveFindings = append(consecutiveFindings, LoopFinding{
				Rule:     "consecutive_tool",
				Severity: severity,
				Message:  fmt.Sprintf("Possible loop: \"%s\" called %d times in a row", name, length),
				Tool:     name,
				Count:    length,
			})
		}
		i = end
	}

	// Reverse consecutive findings so trailing ones come first
	for i := len(consecutiveFindings) - 1; i >= 0; i-- {
		findings = append(findings, consecutiveFindings[i])
	}

	// Rule 2: cycle_pattern (2-tool and 3-tool trailing cycles)
	for block := 2; block <= 3; block++ {
		if len(tools) < block*2 {
			continue
		}
		start := len(tools) - block*2
		repeated := true
		var blockNames []string
		distinct := make(map[string]struct{})
		for k := 0; k < block; k++ {
			left := tools[start+k].Name
			right := tools[start+block+k].Name
			blockNames = append(blockNames, left)
			distinct[left] = struct{}{}
			if left != right || left == "" {
				repeated = false
				break
			}
		}
		if repeated && len(distinct) >= 2 {
			findings = append(findings, LoopFinding{
				Rule:     "cycle_pattern",
				Severity: SeverityCritical,
				Message:  fmt.Sprintf("Possible loop: pattern [%s] repeated", strings.Join(blockNames, ", ")),
				Count:    2,
			})
		}
	}

	// Rule 3: repeated_error
	errCounts := make(map[string]int)
	for _, t := range tools {
		if strings.TrimSpace(t.Error) != "" {
			errSig := truncateRuneSafe(strings.TrimSpace(t.Error), 100)
			errCounts[errSig]++
		}
	}
	if strings.TrimSpace(lastError) != "" {
		errSig := truncateRuneSafe(strings.TrimSpace(lastError), 100)
		errCounts[errSig]++
	}

	// Sort error signatures for deterministic order
	errKeys := make([]string, 0, len(errCounts))
	for k := range errCounts {
		errKeys = append(errKeys, k)
	}
	sort.Strings(errKeys)

	for _, errSig := range errKeys {
		count := errCounts[errSig]
		if count >= 3 {
			findings = append(findings, LoopFinding{
				Rule:     "repeated_error",
				Severity: SeverityCritical,
				Message:  fmt.Sprintf("Error \"%s\" occurred %d times", errSig, count),
				Count:    count,
			})
		}
	}

	return findings
}

// ExtractToolCallsFromOpenAIMessages extracts tool calls from messages history.
// Supports both OpenAI (tool_calls) and Anthropic Claude format (tool_use/tool_result in content blocks).
func ExtractToolCallsFromOpenAIMessages(messages []OpenAIMessage) []ToolCallItem {
	var tools []ToolCallItem
	toolIndexByID := make(map[string]int)

	for _, m := range messages {
		// 1. Direct ToolCalls on assistant message (OpenAI format)
		for _, tc := range m.ToolCalls {
			idx := len(tools)
			tools = append(tools, ToolCallItem{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
			if tc.ID != "" {
				toolIndexByID[tc.ID] = idx
			}
		}

		// 2. Content blocks (Anthropic tool_use or content array)
		if blocks, ok := m.Content.([]any); ok {
			for _, b := range blocks {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				bType, _ := bm["type"].(string)

				if bType == "tool_use" {
					name, _ := bm["name"].(string)
					id, _ := bm["id"].(string)
					idx := len(tools)
					tools = append(tools, ToolCallItem{
						ID:   id,
						Name: name,
					})
					if id != "" {
						toolIndexByID[id] = idx
					}
				} else if bType == "tool_result" {
					toolUseID, _ := bm["tool_use_id"].(string)
					isErr, _ := bm["is_error"].(bool)
					var contentStr string
					switch c := bm["content"].(type) {
					case string:
						contentStr = c
					case []any:
						for _, sub := range c {
							if subMap, ok := sub.(map[string]any); ok {
								if text, ok := subMap["text"].(string); ok {
									contentStr += text + " "
								}
							}
						}
					}

					lower := strings.ToLower(contentStr)
					hasErrText := strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "exit code")
					if isErr || hasErrText {
						if targetIdx, ok := toolIndexByID[toolUseID]; ok && targetIdx < len(tools) {
							tools[targetIdx].Error = contentStr
						} else if len(tools) > 0 {
							tools[len(tools)-1].Error = contentStr
						}
					}
				}
			}
		}

		// 3. OpenAI tool role message
		if m.Role == "tool" && len(tools) > 0 {
			contentStr := ""
			switch c := m.Content.(type) {
			case string:
				contentStr = c
			}
			lower := strings.ToLower(contentStr)
			if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "exit code") {
				if m.ToolCallID != "" {
					if targetIdx, ok := toolIndexByID[m.ToolCallID]; ok && targetIdx < len(tools) {
						tools[targetIdx].Error = contentStr
					} else {
						tools[len(tools)-1].Error = contentStr
					}
				} else {
					tools[len(tools)-1].Error = contentStr
				}
			}
		}
	}

	return tools
}
