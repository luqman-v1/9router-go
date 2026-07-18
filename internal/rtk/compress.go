package rtk

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

const (
	MinCompressSize = 500
	SmartTruncHead  = 120
	SmartTruncTail  = 60
	SmartTruncMin   = 250
	GitDiffMaxLines = 100
	GitLogMaxLines  = 200
	GrepPerFileMax  = 10
	TreeMaxLines    = 200
)

// CompressMessages compresses tool_result content in LLM request bodies in-place.
// Returns modified body and true if any compression was applied.
func CompressMessages(body []byte) ([]byte, bool) {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body, false
	}

	itemsRaw, ok := m["messages"]
	if !ok {
		itemsRaw, ok = m["input"]
	}
	if !ok {
		return body, false
	}

	items, ok := itemsRaw.([]interface{})
	if !ok || len(items) == 0 {
		return body, false
	}

	compressed := false
	for _, item := range items {
		msg, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		// OpenAI Responses: function_call_output
		if msg["type"] == "function_call_output" {
			if output, ok := msg["output"].(string); ok && len(output) > MinCompressSize {
				msg["output"] = CompressText(output)
				compressed = true
			}
			continue
		}

		// OpenAI tool message
		if msg["role"] == "tool" {
			if content, ok := msg["content"].(string); ok && len(content) > MinCompressSize {
				msg["content"] = CompressText(content)
				compressed = true
			}
			continue
		}

		// Claude tool_result in content array
		contentArr, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, part := range contentArr {
			block, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			if block["type"] == "tool_result" {
				if text, ok := block["text"].(string); ok && len(text) > MinCompressSize {
					block["text"] = CompressText(text)
					compressed = true
				}
			}
			if block["type"] == "text" {
				if text, ok := block["text"].(string); ok && len(text) > MinCompressSize {
					block["text"] = CompressText(text)
					compressed = true
				}
			}
		}
	}

	if !compressed {
		return body, false
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// CompressText applies content-aware compression.
func CompressText(text string) string {
	if len(text) < MinCompressSize {
		return text
	}
	trimmed := strings.TrimSpace(text)

	if isGitDiff(trimmed) {
		return compressGitDiff(trimmed)
	}
	if isGitLog(trimmed) {
		return compressGitLog(trimmed)
	}
	if isGrepOutput(trimmed) {
		return compressGrep(trimmed)
	}
	if isTreeOutput(trimmed) {
		return compressTree(trimmed)
	}
	return smartTruncate(trimmed)
}

func isGitDiff(s string) bool {
	return strings.HasPrefix(s, "diff --git") || strings.HasPrefix(s, "--- a/")
}
func isGitLog(s string) bool {
	lines := strings.SplitN(s, "\n", 2)
	if len(lines) == 0 {
		return false
	}
	matched, _ := regexp.MatchString(`^([a-f0-9]{7,40}\s|commit\s[a-f0-9]{7,})`, lines[0])
	return matched
}
func isGrepOutput(s string) bool {
	lines := strings.SplitN(s, "\n", 4)
	if len(lines) < 2 {
		return false
	}
	count := 0
	for _, l := range lines[:3] {
		if len(strings.SplitN(l, ":", 3)) >= 2 {
			count++
		}
	}
	return count >= 2
}
func isTreeOutput(s string) bool {
	return strings.Contains(s, "├──") || strings.Contains(s, "└──")
}

func compressGitDiff(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= GitDiffMaxLines {
		return s
	}
	return strings.Join(lines[:GitDiffMaxLines], "\n") + "\n... (" + itoa(len(lines)-GitDiffMaxLines) + " more lines)"
}

func compressGitLog(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= GitLogMaxLines {
		return s
	}
	return strings.Join(lines[:GitLogMaxLines], "\n") + "\n... (" + itoa(len(lines)-GitLogMaxLines) + " more commits)"
}

func compressGrep(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= GrepPerFileMax*20 {
		return s
	}
	fileCount := make(map[string]int)
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		file := parts[0]
		if fileCount[file] >= GrepPerFileMax {
			continue
		}
		fileCount[file]++
		result = append(result, line)
	}
	if len(result) < len(lines) {
		result = append(result, "... ("+itoa(len(lines)-len(result))+" matches filtered)")
	}
	return strings.Join(result, "\n")
}

func compressTree(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= TreeMaxLines {
		return s
	}
	return strings.Join(lines[:TreeMaxLines], "\n") + "\n... (" + itoa(len(lines)-TreeMaxLines) + " more entries)"
}

func smartTruncate(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= SmartTruncMin {
		return s
	}
	head := lines[:SmartTruncHead]
	tail := lines[len(lines)-SmartTruncTail:]
	result := append(head, "... ("+itoa(len(lines)-SmartTruncHead-SmartTruncTail)+" lines truncated)")
	return strings.Join(append(result, tail...), "\n")
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
