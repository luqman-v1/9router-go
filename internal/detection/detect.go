package detection

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DetectLoops runs deterministic detection rules over a list of tool calls and errors.
func DetectLoops(tools []ToolCallItem, lastError string) []LoopFinding {
	var findings []LoopFinding

	if len(tools) == 0 {
		return findings
	}

	// Rule 1: consecutive_tool
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
			findings = append(findings, LoopFinding{
				Rule:     "consecutive_tool",
				Severity: severity,
				Message:  fmt.Sprintf("Possible loop: \"%s\" called %d times in a row", name, length),
				Tool:     name,
				Count:    length,
			})
		}
		i = end
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
			errSig := strings.TrimSpace(t.Error)
			if len(errSig) > 100 {
				errSig = errSig[:100] + "..."
			}
			errCounts[errSig]++
		}
	}
	if strings.TrimSpace(lastError) != "" {
		errSig := strings.TrimSpace(lastError)
		if len(errSig) > 100 {
			errSig = errSig[:100] + "..."
		}
		errCounts[errSig]++
	}

	for errSig, count := range errCounts {
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
func ExtractToolCallsFromOpenAIMessages(messages []OpenAIMessage) []ToolCallItem {
	var tools []ToolCallItem

	for _, m := range messages {
		// 1. Direct ToolCalls on assistant message
		for _, tc := range m.ToolCalls {
			tools = append(tools, ToolCallItem{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}

		// 2. Content blocks (Anthropic tool_use or content array)
		if blocks, ok := m.Content.([]any); ok {
			for _, b := range blocks {
				if bm, ok := b.(map[string]any); ok {
					if bm["type"] == "tool_use" {
						name, _ := bm["name"].(string)
						var argsStr string
						if input, exists := bm["input"]; exists {
							if inputBytes, err := json.Marshal(input); err == nil {
								argsStr = string(inputBytes)
							}
						}
						tools = append(tools, ToolCallItem{
							Name:      name,
							Arguments: argsStr,
						})
					}
				}
			}
		}

		// 3. Tool results with errors
		if m.Role == "tool" && len(tools) > 0 {
			contentStr := ""
			switch c := m.Content.(type) {
			case string:
				contentStr = c
			}
			lower := strings.ToLower(contentStr)
			if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "exit code") {
				tools[len(tools)-1].Error = contentStr
			}
		}
	}

	return tools
}
