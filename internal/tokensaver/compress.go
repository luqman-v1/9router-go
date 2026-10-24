package tokensaver

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	MinCompressSize = 500
	SmartTruncHead  = 120
	SmartTruncTail  = 60
	// SmartTruncMin is upstream's SMART_TRUNCATE_MIN_LINES
	// (open-sse/rtk/constants.js). Upstream compares against it with a `<`
	// in both filters that use it — open-sse/rtk/filters/smartTruncate.js
	// and open-sse/rtk/filters/readNumbered.js — so a payload of exactly
	// 250 lines IS truncated. This package keeps the number here, once, and
	// both paths read it from here.
	SmartTruncMin   = 250
	GitDiffMaxLines = 100
	GitLogMaxLines  = 200
	GrepPerFileMax  = 10
	TreeMaxLines    = 200
)

// protectedTools lists the tools whose output must never be rewritten.
//
// The names are matched case-insensitively: the same tool reaches the gateway
// spelled "Read" by one client and "read" by another, and the repo's own
// fixtures use the lowercase spelling (`opencode_mock_e2e_test.go` expects
// "read" and "shell"), while upstream's RTK filters key on "grep". An
// exact-case map protected only the clients that happened to use the
// capitalised spelling, which is the opposite of what the list is for.
var protectedTools = map[string]bool{
	"read":     true,
	"grep":     true,
	"edit":     true,
	"write":    true,
	"patch":    true,
	"anchor":   true,
	"glob":     true,
	"view":     true,
	"list_dir": true,
}

func isProtectedTool(name string) bool {
	return protectedTools[strings.ToLower(strings.TrimSpace(name))]
}

// isErrorToolResult reports whether a tool result carries an error.
//
// The structured flags cover the Anthropic shape, where the error is a field
// on the tool_result block. The OpenAI Chat Completions shape has no such
// field: `{"role":"tool","tool_call_id":…,"content":"…"}` carries the failure
// in the content text, so the only way to see it is to read the text.
//
// The markers are the ones the repo's own RTK filters already treat as an
// error output (`internal/tokensaver/filters/generic-output.json` matches
// "Error:", "Exception:" and "Traceback (most recent call last):"), so the
// rule stays in one place. Matching is anchored at the start of the content:
// a result that merely mentions the word somewhere in the middle is not an
// error, and the cost of a false positive is only that the result is left
// uncompressed, while a false negative rewrites the very text the agent
// needed.
func isErrorToolResult(m map[string]any) bool {
	if m == nil {
		return false
	}
	if b, ok := m["is_error"].(bool); ok && b {
		return true
	}
	if b, ok := m["isError"].(bool); ok && b {
		return true
	}
	if strings.EqualFold(fmt.Sprintf("%v", m["status"]), "error") {
		return true
	}
	// The text of the result, wherever this shape keeps it: the OpenAI
	// Chat Completions tool message puts it in "content", the OpenAI
	// Responses function_call_output in "output", and a flat Anthropic
	// tool_result in "text".
	for _, key := range []string{"content", "output", "text"} {
		if hasErrorToolContent(m[key]) {
			return true
		}
	}
	return false
}

// errorToolContentMarkers are the prefixes that open an error output. They
// mirror the match patterns of the generic-output RTK filter.
var errorToolContentMarkers = []string{
	"Error:",
	"Exception:",
	"Traceback (most recent call last):",
}

// hasErrorToolContent reports whether a tool result's content opens with one
// of the error markers. The OpenAI tool shape has no error flag, so this is
// the only signal it has.
func hasErrorToolContent(content any) bool {
	text, ok := content.(string)
	if !ok {
		return false
	}
	trimmed := strings.TrimSpace(text)
	for _, marker := range errorToolContentMarkers {
		if strings.HasPrefix(trimmed, marker) {
			return true
		}
	}
	return false
}

// buildToolCallMap indexes every tool call in the conversation by the id its
// result will reference, and records the command it ran.
//
// The name is what the protected-tool skip list matches. The command is what
// identifies the output: a filter's rules are written for the output of one
// command, so the command is what decides which filter may apply to it.
// Without it the only signal left is the content, and content matches text
// that merely mentions what the filter keys on.
func buildToolCallMap(items []map[string]any) (map[string]string, map[string]string) {
	toolMap := make(map[string]string)
	cmdMap := make(map[string]string)
	for _, item := range items {
		// Chat assistant tool_calls
		if tcList, ok := item["tool_calls"].([]any); ok {
			for _, tc := range tcList {
				if tcMap, ok := tc.(map[string]any); ok {
					id, _ := tcMap["id"].(string)
					if fn, ok := tcMap["function"].(map[string]any); ok {
						name, _ := fn["name"].(string)
						if name != "" && id != "" {
							toolMap[id] = name
						}
						if id != "" {
							if cmd := commandFromToolArgs(fn["arguments"]); cmd != "" {
								cmdMap[id] = cmd
							}
						}
					}
				}
			}
		}
		// Responses function_call
		if t, ok := item["type"].(string); ok && t == "function_call" {
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			if name != "" && callID != "" {
				toolMap[callID] = name
			}
			if callID != "" {
				if cmd := commandFromToolArgs(item["arguments"]); cmd != "" {
					cmdMap[callID] = cmd
				}
			}
		}
		// Claude tool_use in content array
		if contentArr, ok := item["content"].([]any); ok {
			for _, part := range contentArr {
				if block, ok := part.(map[string]any); ok {
					if bt, ok := block["type"].(string); ok && bt == "tool_use" {
						id, _ := block["id"].(string)
						name, _ := block["name"].(string)
						if name != "" && id != "" {
							toolMap[id] = name
						}
						if id != "" {
							if cmd := commandFromToolArgs(block["input"]); cmd != "" {
								cmdMap[id] = cmd
							}
						}
					}
				}
			}
		}
	}
	return toolMap, cmdMap
}

// commandFromToolArgs reads the command out of a tool call's arguments.
//
// The arguments arrive in three shapes: a JSON string (the Chat Completions
// and Responses shapes), a decoded object (the Claude tool_use shape), or
// absent. The keys are the ones the translator's own bash-argument
// sanitiser accepts, so the same spellings resolve here.
func commandFromToolArgs(args any) string {
	switch a := args.(type) {
	case string:
		if a == "" {
			return ""
		}
		// Cheap guard before the decode: every key this reads is one of
		// "command", "cmd" or "CommandLine", so a string without any of
		// them cannot carry one, and the decode is skipped. The arguments
		// are decoded once per tool call in the conversation, on a request
		// path that has to stay cheap.
		if !strings.Contains(a, "command") && !strings.Contains(a, "Command") &&
			!strings.Contains(a, "cmd") && !strings.Contains(a, "Cmd") {
			return ""
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(a), &decoded); err != nil {
			return ""
		}
		return commandFromDecodedArgs(decoded)
	case map[string]any:
		return commandFromDecodedArgs(a)
	}
	return ""
}

func commandFromDecodedArgs(args map[string]any) string {
	for _, key := range []string{"command", "cmd", "CommandLine"} {
		if s, ok := args[key].(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// CompressMessages compresses tool_result content in LLM request bodies in-place.
// Returns modified body and true if any compression was applied.
func CompressMessages(body []byte) ([]byte, bool) {
	return CompressMessagesWithConfig(body, DefaultRTKConfig())
}

// CompressMessagesWithConfig compresses tool_result content in LLM request bodies in-place using RTKConfig.
// Returns modified body and true if any compression was applied.
func CompressMessagesWithConfig(body []byte, cfg RTKConfig) ([]byte, bool) {
	var rawMap map[string]jsontext.Value
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return body, false
	}
	// Unmarshal into map[string]jsontext.Value succeeds for "null" or "[]",
	// leaving rawMap nil. Marshal(nil map) yields "null", which would corrupt
	// the request body downstream, so bail out on non-object payloads.
	if rawMap == nil {
		return body, false
	}

	key := "messages"
	itemsBytes, ok := rawMap["messages"]
	if !ok {
		key = "input"
		itemsBytes, ok = rawMap["input"]
	}
	if !ok || len(itemsBytes) == 0 {
		return body, false
	}

	var items []map[string]any
	if err := json.Unmarshal(itemsBytes, &items); err != nil || len(items) == 0 {
		return body, false
	}

	toolMap, cmdMap := buildToolCallMap(items)
	compressed := false
	for _, msg := range items {
		// OpenAI Responses: function_call_output
		if msg["type"] == "function_call_output" {
			name, _ := msg["name"].(string)
			command := ""
			if callID, ok := msg["call_id"].(string); ok {
				if name == "" {
					name = toolMap[callID]
				}
				command = cmdMap[callID]
			}
			if isErrorToolResult(msg) {
				continue
			}
			if isProtectedTool(name) {
				// Protected tool output (Read, Grep, Edit, Write, etc.) is never rewritten.
				continue
			}
			if output, ok := msg["output"].(string); ok && len(output) > MinCompressSize {
				msg["output"] = CompressTextWithCommand(output, command, cfg)
				compressed = true
			} else if outputArr, ok := msg["output"].([]any); ok {
				for _, part := range outputArr {
					if partMap, ok := part.(map[string]any); ok {
						if t, ok := partMap["type"].(string); ok && (t == "input_text" || t == "text") {
							if text, ok := partMap["text"].(string); ok && len(text) > MinCompressSize {
								partMap["text"] = CompressTextWithCommand(text, command, cfg)
								compressed = true
							}
						}
					}
				}
			}
			continue
		}

		// OpenAI tool message
		if msg["role"] == "tool" {
			name, _ := msg["name"].(string)
			command := ""
			if callID, ok := msg["tool_call_id"].(string); ok {
				if name == "" {
					name = toolMap[callID]
				}
				command = cmdMap[callID]
			}
			if isErrorToolResult(msg) {
				continue
			}
			if isProtectedTool(name) {
				// Protected tool output (Read, Grep, Edit, Write, etc.) is never rewritten.
				continue
			}
			if content, ok := msg["content"].(string); ok && len(content) > MinCompressSize {
				msg["content"] = CompressTextWithCommand(content, command, cfg)
				compressed = true
			} else if contentArr, ok := msg["content"].([]any); ok {
				for _, part := range contentArr {
					if partMap, ok := part.(map[string]any); ok {
						if t, ok := partMap["type"].(string); ok && t == "text" {
							if text, ok := partMap["text"].(string); ok && len(text) > MinCompressSize {
								partMap["text"] = CompressTextWithCommand(text, command, cfg)
								compressed = true
							}
						}
					}
				}
			}
			continue
		}

		// Claude tool_result in content array
		contentArr, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range contentArr {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "tool_result" {
				name, _ := block["name"].(string)
				command := ""
				if toolUseID, ok := block["tool_use_id"].(string); ok {
					if name == "" {
						name = toolMap[toolUseID]
					}
					command = cmdMap[toolUseID]
				}
				if isErrorToolResult(block) {
					continue
				}
				if isProtectedTool(name) {
					// Protected tool output (Read, Grep, Edit, Write, etc.) is never rewritten.
					continue
				}

				if text, ok := block["text"].(string); ok && len(text) > MinCompressSize {
					block["text"] = CompressTextWithCommand(text, command, cfg)
					compressed = true
				}
				if content, ok := block["content"].(string); ok && len(content) > MinCompressSize {
					block["content"] = CompressTextWithCommand(content, command, cfg)
					compressed = true
				} else if innerArr, ok := block["content"].([]any); ok {
					for _, innerPart := range innerArr {
						if innerBlock, ok := innerPart.(map[string]any); ok {
							if innerBlock["type"] == "text" {
								if text, ok := innerBlock["text"].(string); ok && len(text) > MinCompressSize {
									innerBlock["text"] = CompressTextWithCommand(text, command, cfg)
									compressed = true
								}
							}
						}
					}
				}
			}
		}
	}

	if !compressed {
		return body, false
	}

	compressedBytes, err := json.Marshal(items)
	if err != nil {
		return body, false
	}
	rawMap[key] = jsontext.Value(compressedBytes)

	out, err := json.Marshal(rawMap)
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
	// `<` is upstream's comparison (open-sse/rtk/filters/smartTruncate.js):
	// a payload of exactly SMART_TRUNCATE_MIN_LINES lines is truncated.
	if len(lines) < SmartTruncMin {
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
