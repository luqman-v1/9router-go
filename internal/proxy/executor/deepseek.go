package executor

import "encoding/json"

// deepSeekThinkingDisabled reports whether the request explicitly turns
// DeepSeek's thinking mode off. V4 models think by default, so on the Chat
// Completions wire the only ways to disable it are a top-level `thinking.type`
// of "disabled" or a reasoning_effort of "none".
func deepSeekThinkingDisabled(reqMap map[string]any) bool {
	if t, ok := reqMap["thinking"].(map[string]any); ok {
		if typ, _ := t["type"].(string); typ == "disabled" {
			return true
		}
	}
	if effort, _ := reqMap["reasoning_effort"].(string); effort == "none" {
		return true
	}
	return false
}

// NormalizeDeepSeekThinkingToolChoice relaxes a forced tool_choice on requests
// where DeepSeek's thinking mode stays active. The Chat Completions API rejects
// every choice but "auto"/"none" while thinking is on: required and named
// choices answer 400 "Thinking mode does not support this tool_choice"
// (api-docs.deepseek.com). V4 models think by default, so a client that pins a
// tool gets a hard failure unless the gateway downgrades the pin to "auto" —
// the relaxation the official client applies (Pydantic AI sends
// tool_choice="auto" on V4 requests for the same reason).
//
// A request that explicitly disabled thinking keeps its forced choice.
func NormalizeDeepSeekThinkingToolChoice(body []byte) []byte {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return body
	}
	tc, ok := reqMap["tool_choice"]
	if !ok || tc == nil {
		return body
	}
	if s, isStr := tc.(string); isStr && (s == "auto" || s == "none") {
		return body
	}
	if _, hasTools := reqMap["tools"]; !hasTools {
		// A forced choice with no tools declared cannot be pinned to anything,
		// and upstream accepts it (it reads as "use whatever exists", which is
		// nothing) — leave the body untouched.
		return body
	}
	if deepSeekThinkingDisabled(reqMap) {
		return body
	}
	reqMap["tool_choice"] = "auto"
	out, err := marshalStable(reqMap)
	if err != nil {
		return body
	}
	return out
}
