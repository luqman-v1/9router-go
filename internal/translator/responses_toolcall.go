package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
)

// emitToolCall streams one tool-call fragment. Some OpenAI-compatible providers
// split the call id and the function name across separate chunks, so the item is
// only announced once both halves are known — deciding earlier would irreversibly
// announce an `exec` call as a plain function call.
func (s *ResponsesState) emitToolCall(out *[]ResponsesEvent, tc OpenAIToolCallStream) {
	tcIdx := 0
	if tc.Index != nil {
		tcIdx = *tc.Index
	}

	var funcName string
	if tc.Function != nil {
		funcName = tc.Function.Name
	}

	if funcName != "" {
		s.FuncNames[tcIdx] = funcName
	}
	if tc.ID != "" {
		s.FuncCallIDs[tcIdx] = tc.ID
	}

	callID := s.FuncCallIDs[tcIdx]
	if !s.FuncItemAdded[tcIdx] && callID != "" && s.FuncNames[tcIdx] != "" {
		s.FuncItemAdded[tcIdx] = true
		custom := s.isCustomTool(s.FuncNames[tcIdx])

		item := map[string]any{
			"id":      s.toolItemID(custom, callID),
			"type":    toolItemType(custom),
			"call_id": callID,
			"name":    s.FuncNames[tcIdx],
		}
		// A custom tool takes freeform input, a function call takes JSON.
		if custom {
			item["input"] = ""
		} else {
			item["arguments"] = ""
		}

		s.emit(out, "response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": tcIdx,
			"item":         item,
		})
	}

	if _, ok := s.FuncArgsBuf[tcIdx]; !ok {
		s.FuncArgsBuf[tcIdx] = ""
	}

	if tc.Function == nil || tc.Function.Arguments == "" {
		return
	}

	refCallID := s.FuncCallIDs[tcIdx]
	if refCallID == "" {
		refCallID = tc.ID
	}
	if s.FuncItemAdded[tcIdx] && refCallID != "" && !s.isCustomTool(s.FuncNames[tcIdx]) {
		s.emit(out, "response.function_call_arguments.delta", map[string]any{
			"type":         "response.function_call_arguments.delta",
			"item_id":      fmt.Sprintf("%s_%s", responsesPrefixFunctionCall, refCallID),
			"output_index": tcIdx,
			"delta":        tc.Function.Arguments,
		})
	}
	// Custom tool input is emitted once at close, after the Chat JSON wrapper can
	// be unwrapped. Streaming the raw fragments would expose {"input":"..."}
	// instead of the freeform program the client expects.
	s.FuncArgsBuf[tcIdx] += tc.Function.Arguments
}

// closeToolCall finalizes one tool call, defaulting to an empty argument object
// so the item is still well-formed when the provider never sent arguments.
//
// A non-empty buffer that does not parse as JSON is a call cut mid-argument:
// the stream ended between two tokens of the argument object. The item is still
// closed with the bytes that arrived — the client can decide what to do with
// them — but the turn is marked incomplete so the terminal event reports the
// truncation instead of a success.
//
// The reason names the cause the upstream gave: a call cut by "length" hit the
// output limit, and that is the max_output_tokens reason the Responses API
// already defines. Anything else — including the usual case, where the stream
// simply ends and there is no finish_reason at all — is a dropped stream.
// Custom tools are exempt: their input is freeform text, not JSON.
func (s *ResponsesState) closeToolCall(out *[]ResponsesEvent, idx int) {
	callID := s.FuncCallIDs[idx]
	if callID == "" || s.FuncItemDone[idx] {
		return
	}

	args := s.FuncArgsBuf[idx]
	if args == "" {
		args = "{}"
	}
	custom := s.isCustomTool(s.FuncNames[idx])
	if !custom && s.Incomplete == "" && !jsontext.Value(args).IsValid() {
		s.Incomplete = responsesIncompleteToolArguments
		if s.FinishReason == "length" {
			s.Incomplete = responsesIncompleteMaxOutputTokens
		}
	}

	if custom {
		input := extractCustomToolInput(args)
		s.emit(out, "response.custom_tool_call_input.delta", map[string]any{
			"type":         "response.custom_tool_call_input.delta",
			"item_id":      fmt.Sprintf("%s_%s", responsesPrefixCustomTool, callID),
			"output_index": idx,
			"delta":        input,
		})
		s.emit(out, "response.custom_tool_call_input.done", map[string]any{
			"type":         "response.custom_tool_call_input.done",
			"item_id":      fmt.Sprintf("%s_%s", responsesPrefixCustomTool, callID),
			"output_index": idx,
			"input":        input,
		})
	} else {
		s.emit(out, "response.function_call_arguments.done", map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      fmt.Sprintf("%s_%s", responsesPrefixFunctionCall, callID),
			"output_index": idx,
			"arguments":    args,
		})
	}

	item := map[string]any{
		"id":      s.toolItemID(custom, callID),
		"type":    toolItemType(custom),
		"call_id": callID,
		"name":    s.FuncNames[idx],
	}
	if custom {
		item["input"] = extractCustomToolInput(args)
	} else {
		item["arguments"] = args
	}

	s.emit(out, "response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": idx,
		"item":         item,
	})
	s.recordCompletedOutputItem(idx, item)

	s.FuncItemDone[idx] = true
	s.FuncArgsDone[idx] = true
}

func (s *ResponsesState) toolItemID(custom bool, callID string) string {
	prefix := responsesPrefixFunctionCall
	if custom {
		prefix = responsesPrefixCustomTool
	}
	return fmt.Sprintf("%s_%s", prefix, callID)
}

func toolItemType(custom bool) string {
	if custom {
		return responsesItemCustomToolCall
	}
	return responsesItemFunctionCall
}

// extractCustomToolInput unwraps the {"input":"..."} envelope that Chat
// Completions uses to carry a custom tool's freeform argument. Anything that
// does not parse, or lacks a string input, is passed through untouched.
func extractCustomToolInput(argumentsText string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(argumentsText), &parsed); err != nil {
		return argumentsText
	}
	if input, ok := parsed["input"].(string); ok {
		return input
	}
	return argumentsText
}

// toResponsesUsage converts an upstream Chat usage block into the Responses
// shape. It is stored on its own field rather than over the stream's own usage
// accounting, which owns the Chat-shaped counts used for logging and cost.
func toResponsesUsage(u *OpenAIUsage) *ResponsesUsage {
	if u == nil {
		return nil
	}

	out := &ResponsesUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.PromptTokens + u.CompletionTokens,
	}
	if cached := u.GetCachedTokens(); cached > 0 {
		out.InputTokensDetails = &ResponsesInputDetails{CachedTokens: cached}
	}
	if reasoning := u.ReasoningTokens(); reasoning > 0 {
		out.OutputTokensDetails = &ResponsesOutputDetails{ReasoningTokens: reasoning}
	}
	return out
}
