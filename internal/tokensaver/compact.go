package tokensaver

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

const (
	// DefaultContextBudgetRatio is the default fraction of the model context window
	// after which context compaction triggers (85%).
	DefaultContextBudgetRatio = 0.85

	// DefaultPreserveRecentTurns is the number of most recent conversation turns
	// (user/assistant/tool pairs) guaranteed never to be compacted.
	DefaultPreserveRecentTurns = 2

	// MinCompactionOutputSize is the minimum byte size for a tool output to be considered
	// eligible for compaction into a summary stub.
	MinCompactionOutputSize = 200
)

// CompactContextOptions holds configuration for dynamic context compaction.
type CompactContextOptions struct {
	// ModelContextWindow is the total context window size of the target model in tokens.
	// If BudgetTokens is not explicitly set, BudgetRatio * ModelContextWindow is used.
	ModelContextWindow int

	// BudgetTokens is the explicit token threshold. If 0, BudgetRatio is used.
	BudgetTokens int

	// BudgetRatio is the fraction of context window to trigger compaction (default 0.85).
	BudgetRatio float64

	// PreserveRecentTurns is the number of trailing conversation turns to protect (default 2).
	PreserveRecentTurns int
}

// CompactContextResult summarizes the outcome of dynamic context compaction.
type CompactContextResult struct {
	Compacted        bool
	EstimatedBefore  int
	EstimatedAfter   int
	CompactedOutputs int
	CompactedBytes   int
}

// EstimateTokens calculates a fast, conservative token estimate for request content.
// Consistent with 9router tokensaver standard (~4 chars per token).
func EstimateTokens(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	return (len(b) + 3) / 4
}

// CompactContextByBudget compacts intermediate tool outputs when estimated tokens
// exceed the model context budget threshold.
// Returns the resulting request body and a result summary.
func CompactContextByBudget(body []byte, opts CompactContextOptions) ([]byte, CompactContextResult) {
	result := CompactContextResult{
		EstimatedBefore: EstimateTokens(body),
		EstimatedAfter:  EstimateTokens(body),
	}

	threshold := opts.BudgetTokens
	if threshold <= 0 && opts.ModelContextWindow > 0 {
		ratio := opts.BudgetRatio
		if ratio <= 0 {
			ratio = DefaultContextBudgetRatio
		}
		threshold = int(float64(opts.ModelContextWindow) * ratio)
	}

	// If no budget limit could be resolved or token estimate is below threshold, leave untouched.
	if threshold <= 0 || result.EstimatedBefore <= threshold {
		return body, result
	}

	preserveTurns := opts.PreserveRecentTurns
	if preserveTurns <= 0 {
		preserveTurns = DefaultPreserveRecentTurns
	}

	var rawMap map[string]jsontext.Value
	if err := json.Unmarshal(body, &rawMap); err != nil || rawMap == nil {
		return body, result
	}

	key := "messages"
	itemsBytes, ok := rawMap["messages"]
	if !ok {
		key = "input"
		itemsBytes, ok = rawMap["input"]
	}
	if !ok || len(itemsBytes) == 0 {
		return body, result
	}

	var items []map[string]any
	if err := json.Unmarshal(itemsBytes, &items); err != nil || len(items) == 0 {
		return body, result
	}

	// Identify turn boundaries: count conversation turns from the end.
	// We preserve:
	// 1. System messages / instructions (role == "system" or developer instructions).
	// 2. The initial task objective (first user message).
	// 3. The most recent N conversation turns at the tail.
	cutoffIndex := computeCompactionCutoff(items, preserveTurns)
	if cutoffIndex <= 0 {
		return body, result
	}

	compactedCount := 0
	totalCompactedBytes := 0

	// Walk items before cutoffIndex. Skip index 0 if it is the initial user turn.
	firstUserSeen := false
	for i := 0; i < cutoffIndex; i++ {
		msg := items[i]
		role, _ := msg["role"].(string)

		// Never touch system instructions.
		if role == "system" || role == "developer" {
			continue
		}

		// Never touch the initial user prompt / task objective.
		if role == "user" && !firstUserSeen {
			firstUserSeen = true
			continue
		}

		// Check for error tool results — errors contain vital failure feedback.
		if isErrorToolResult(msg) {
			continue
		}

		// Compact tool outputs in this item
		c, b := compactMessageToolOutputs(msg)
		if c > 0 {
			compactedCount += c
			totalCompactedBytes += b
		}
	}

	if compactedCount == 0 {
		return body, result
	}

	compactedBytes, err := json.Marshal(items, json.Deterministic(true))
	if err != nil {
		return body, result
	}
	rawMap[key] = jsontext.Value(compactedBytes)

	out, err := json.Marshal(rawMap, json.Deterministic(true))
	if err != nil {
		return body, result
	}

	result.Compacted = true
	result.CompactedOutputs = compactedCount
	result.CompactedBytes = totalCompactedBytes
	result.EstimatedAfter = EstimateTokens(out)

	return out, result
}

// computeCompactionCutoff determines the item index before which items are eligible
// for compaction. Everything from cutoffIndex to len(items)-1 belongs to the
// protected recent turns and is never compacted.
//
// Turn counting is message-level: each assistant or user message going backwards
// from the end counts as one boundary. With preserveTurns = 2, the two most recent
// assistant/user messages mark the protected window — everything at and after the
// earlier of those two is preserved. This is not a count of complete user→assistant
// turn pairs; it is a count of message boundaries. Callers that want to protect N
// full user/assistant pairs should pass PreserveRecentTurns = 2*N.
func computeCompactionCutoff(items []map[string]any, preserveTurns int) int {
	n := len(items)
	if n <= 1 {
		return 0
	}

	// Count turns backwards from the end.
	// A turn boundary is marked by an assistant or user message.
	turnsFound := 0
	cutoff := n

	for i := n - 1; i >= 0; i-- {
		msg := items[i]
		role, _ := msg["role"].(string)
		msgType, _ := msg["type"].(string)

		// An assistant response or user message represents a turn boundary
		if role == "assistant" || role == "user" || msgType == "message" {
			turnsFound++
			cutoff = i
			if turnsFound >= preserveTurns {
				break
			}
		}
	}

	return cutoff
}

// compactMessageToolOutputs compacts large tool results in a message item.
// Handles OpenAI tool role, Responses function_call_output, and Claude tool_result.
func compactMessageToolOutputs(msg map[string]any) (int, int) {
	compacted := 0
	compactedBytes := 0

	// 1. OpenAI Responses: function_call_output
	if msg["type"] == "function_call_output" {
		if outStr, ok := msg["output"].(string); ok && len(outStr) >= MinCompactionOutputSize {
			msg["output"] = formatCompactedSummary(outStr)
			compacted++
			compactedBytes += len(outStr) - len(msg["output"].(string))
		} else if outArr, ok := msg["output"].([]any); ok {
			for _, part := range outArr {
				if partMap, ok := part.(map[string]any); ok {
					if t, ok := partMap["type"].(string); ok && (t == "input_text" || t == "text") {
						if text, ok := partMap["text"].(string); ok && len(text) >= MinCompactionOutputSize {
							partMap["text"] = formatCompactedSummary(text)
							compacted++
							compactedBytes += len(text) - len(partMap["text"].(string))
						}
					}
				}
			}
		}
		return compacted, compactedBytes
	}

	// 2. OpenAI Chat Completions: role == "tool"
	if msg["role"] == "tool" {
		if content, ok := msg["content"].(string); ok && len(content) >= MinCompactionOutputSize {
			msg["content"] = formatCompactedSummary(content)
			compacted++
			compactedBytes += len(content) - len(msg["content"].(string))
		} else if contentArr, ok := msg["content"].([]any); ok {
			for _, part := range contentArr {
				if partMap, ok := part.(map[string]any); ok {
					if t, ok := partMap["type"].(string); ok && t == "text" {
						if text, ok := partMap["text"].(string); ok && len(text) >= MinCompactionOutputSize {
							partMap["text"] = formatCompactedSummary(text)
							compacted++
							compactedBytes += len(text) - len(partMap["text"].(string))
						}
					}
				}
			}
		}
		return compacted, compactedBytes
	}

	// 3. Claude tool_result inside content array
	if contentArr, ok := msg["content"].([]any); ok {
		for _, part := range contentArr {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "tool_result" {
				if isErrorToolResult(block) {
					continue
				}
				if text, ok := block["text"].(string); ok && len(text) >= MinCompactionOutputSize {
					block["text"] = formatCompactedSummary(text)
					compacted++
					compactedBytes += len(text) - len(block["text"].(string))
				}
				if content, ok := block["content"].(string); ok && len(content) >= MinCompactionOutputSize {
					block["content"] = formatCompactedSummary(content)
					compacted++
					compactedBytes += len(content) - len(block["content"].(string))
				} else if innerArr, ok := block["content"].([]any); ok {
					for _, innerPart := range innerArr {
						if innerBlock, ok := innerPart.(map[string]any); ok {
							if innerBlock["type"] == "text" {
								if text, ok := innerBlock["text"].(string); ok && len(text) >= MinCompactionOutputSize {
									innerBlock["text"] = formatCompactedSummary(text)
									compacted++
									compactedBytes += len(text) - len(innerBlock["text"].(string))
								}
							}
						}
					}
				}
			}
		}
	}

	return compacted, compactedBytes
}

// formatCompactedSummary creates a structured truncated summary retaining leading/trailing context.
func formatCompactedSummary(text string) string {
	originalBytes := len(text)
	lines := strings.Split(text, "\n")
	lineCount := len(lines)

	// Keep a short preview of head and tail if lines exist
	headPreviewLines := 3
	tailPreviewLines := 2

	if lineCount <= headPreviewLines+tailPreviewLines {
		// Short line count but large bytes (e.g., minified JSON or single long line)
		headChars := 80
		tailChars := 40
		if originalBytes > headChars+tailChars {
			// Slice by rune boundaries, not byte offsets, so multibyte UTF-8
			// (CJK, emoji) is never split mid-sequence. A byte-offset slice could
			// produce invalid UTF-8, which json.Marshal with json.Deterministic(true)
			// rejects on Go 1.27.
			runes := []rune(text)
			head := string(runes[:headChars])
			tail := string(runes[len(runes)-tailChars:])
			return fmt.Sprintf("%s\n[output compacted: %d bytes, %d lines omitted]\n%s", head, originalBytes, lineCount, tail)
		}
		return fmt.Sprintf("[output compacted: %d bytes]", originalBytes)
	}

	head := strings.Join(lines[:headPreviewLines], "\n")
	tail := strings.Join(lines[lineCount-tailPreviewLines:], "\n")

	return fmt.Sprintf("%s\n[output compacted: %d bytes, %d lines omitted]\n%s", head, originalBytes, lineCount-headPreviewLines-tailPreviewLines, tail)
}
