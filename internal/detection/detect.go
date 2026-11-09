package detection

import (
	"encoding/json"
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

// cleanArg cleans and normalizes tool argument strings for comparison.
func cleanArg(arg string) string {
	return strings.TrimSpace(arg)
}

// DetectLoops runs smart deterministic detection rules over a list of tool calls and errors.
// Focuses on the recent trailing window of the conversation to avoid false positives on large codebases.
func DetectLoops(tools []ToolCallItem, lastError string) []LoopFinding {
	var findings []LoopFinding

	if len(tools) == 0 {
		return findings
	}

	// Focus detection on recent trailing window (up to last 15 tool calls)
	// so ancient exploratory sequences from 50 turns ago do not trigger warnings.
	window := tools
	const trailingWindowSize = 15
	if len(window) > trailingWindowSize {
		window = window[len(window)-trailingWindowSize:]
	}

	// Rule 1: consecutive_tool
	for i := 0; i < len(window); {
		name := window[i].Name
		if name == "" {
			i++
			continue
		}
		end := i + 1
		for end < len(window) && window[end].Name == name {
			end++
		}
		length := end - i

		// Check if arguments are identical across the consecutive calls
		allSameArgs := true
		firstArg := cleanArg(window[i].Arguments)
		for k := i + 1; k < end; k++ {
			if cleanArg(window[k].Arguments) != firstArg {
				allSameArgs = false
				break
			}
		}

		if !allSameArgs {
			i = end
			continue
		}
		if length >= 3 {
			// Stuck loop: exact same tool called with exact same arguments 3+ times
			findings = append(findings, LoopFinding{
				Rule:     "consecutive_tool",
				Severity: SeverityCritical,
				Message:  fmt.Sprintf("Stuck loop: \"%s\" called with identical arguments %d times in a row", name, length),
				Tool:     name,
				Count:    length,
			})
		}

		i = end
	}

	// Rule 2: cycle_pattern (2-tool and 3-tool trailing cycles)
	for block := 2; block <= 3; block++ {
		// A cycle needs at least 2 full repetitions (block * 2) or 3 for 2-tool cycles
		minReps := 2
		if block == 2 {
			minReps = 3 // [A, B, A, B, A, B] = 6 calls
		}
		totalNeeded := block * minReps
		if len(window) < totalNeeded {
			continue
		}
		start := len(window) - totalNeeded
		repeated := true
		var blockNames []string
		distinct := make(map[string]struct{})

		for r := 0; r < minReps; r++ {
			for k := 0; k < block; k++ {
				curr := window[start+r*block+k].Name
				if curr == "" {
					repeated = false
					break
				}
				if r == 0 {
					blockNames = append(blockNames, curr)
					distinct[curr] = struct{}{}
				} else {
					if curr != blockNames[k] {
						repeated = false
						break
					}
				}
			}
			if !repeated {
				break
			}
		}

		if repeated && len(distinct) >= 2 {
			findings = append(findings, LoopFinding{
				Rule:     "cycle_pattern",
				Severity: SeverityCritical,
				Message:  fmt.Sprintf("Possible loop: pattern [%s] repeated %d times", strings.Join(blockNames, ", "), minReps),
				Count:    minReps,
			})
		}
	}

	// Rule 3: repeated_error
	errCounts := make(map[string]int)
	for _, t := range window {
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

func isGenuineError(content string, isErrFlag bool) bool {
	if isErrFlag {
		return true
	}
	lower := strings.ToLower(content)
	for _, marker := range []string{
		"exit code 1", "exit code 2", "exit code 127", "exit status",
		"command not found", "no such file or directory",
		"syntaxerror:", "typeerror:", "referenceerror:",
		"permission denied", "fatal error:", "panic:",
		"traceback (most recent call last):",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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

				switch bType {
				case "tool_use":
					name, _ := bm["name"].(string)
					id, _ := bm["id"].(string)
					var argsStr string
					if input, exists := bm["input"]; exists && input != nil {
						if inputBytes, err := json.Marshal(input); err == nil {
							argsStr = string(inputBytes)
						}
					}
					idx := len(tools)
					tools = append(tools, ToolCallItem{
						ID:        id,
						Name:      name,
						Arguments: argsStr,
					})
					if id != "" {
						toolIndexByID[id] = idx
					}
				case "tool_result":
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

					if isGenuineError(contentStr, isErr) {
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
			if isGenuineError(contentStr, false) {
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
