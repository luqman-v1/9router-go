package translator

import "strings"

// TranslateOpenAIToResponses replays one upstream Chat Completions chunk as a
// slice of Responses API events. A nil chunk flushes whatever is still open,
// which is how the terminal event gets emitted once the upstream stream ends.
func TranslateOpenAIToResponses(chunk *OpenAIChunk, s *ResponsesState) []ResponsesEvent {
	if chunk == nil {
		return FlushResponses(s)
	}

	// Usage is captured before the choices guard: the last OpenAI chunk carries
	// usage together with an empty choices array, and dropping it would leave
	// the client without a token count.
	if chunk.Usage != nil {
		s.Usage = toResponsesUsage(chunk.Usage)
	}

	if len(chunk.Choices) == 0 {
		return nil
	}

	var out []ResponsesEvent

	if !s.Started {
		s.Started = true
		if chunk.ID != "" {
			s.ResponseID = "resp_" + chunk.ID
		}

		s.emit(&out, "response.created", map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id":         s.ResponseID,
				"object":     "response",
				"created_at": s.Created,
				"status":     "in_progress",
				"background": false,
				"error":      nil,
				"output":     []any{},
			},
		})

		s.emit(&out, "response.in_progress", map[string]any{
			"type": "response.in_progress",
			"response": map[string]any{
				"id":         s.ResponseID,
				"object":     "response",
				"created_at": s.Created,
				"status":     "in_progress",
			},
		})
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	if reasoningText := extractReasoningText(delta); reasoningText != "" {
		s.startReasoning(&out, choice.Index)
		s.emitReasoningDelta(&out, reasoningText)
	}

	s.translateContent(&out, choice, delta)

	// Tool calls close the answer first: they always follow the text.
	if len(delta.ToolCalls) > 0 {
		s.closeReasoning(&out)
		s.closeMessage(&out, choice.Index)
		for _, tc := range delta.ToolCalls {
			s.emitToolCall(&out, tc)
		}
	}

	if choice.FinishReason != nil && *choice.FinishReason != "" {
		for _, idx := range sortedIntKeys(s.MsgItemAdded) {
			s.closeMessage(&out, idx)
		}
		s.closeReasoning(&out)
		for _, idx := range sortedIntKeys(s.FuncCallIDs) {
			s.closeToolCall(&out, idx)
		}
		// Upstreams report usage either on the finish chunk itself or on a
		// trailing chunk whose choices array is empty (OpenAI does the latter).
		// Completing here would freeze the payload before that trailing chunk is
		// parsed, so when usage is still unknown the completion is deferred to
		// the flush, which runs once the stream has ended and has seen everything.
		//
		// That only holds when the terminal chunk reaches us. When this runs as
		// the second hop of a pivot, translateResponse already dropped it, so the
		// flush never happens and deferring would swallow response.completed.
		if s.Usage != nil || !s.FlushReachesUs {
			s.sendCompleted(&out)
		}
		s.CompletionPending = !s.Completed
	}

	return out
}

// translateContent turns the answer text of one chunk into events, splitting any
// inline <think> block out as reasoning on the way through.
func (s *ResponsesState) translateContent(out *[]ResponsesEvent, choice OpenAIChoice, delta OpenAIDelta) {
	content := delta.Content
	if content == "" {
		return
	}

	if strings.Contains(content, "<think>") {
		s.InThinking = true
		content = strings.Replace(content, "<think>", "", 1)
		s.startReasoning(out, choice.Index)
	}

	if strings.Contains(content, "</think>") {
		parts := strings.Split(content, "</think>")
		if thinkPart := parts[0]; thinkPart != "" {
			s.emitReasoningDelta(out, thinkPart)
		}
		s.closeReasoning(out)
		s.InThinking = false
		content = strings.Join(parts[1:], "</think>")
	}

	if s.InThinking && content != "" {
		s.emitReasoningDelta(out, content)
		return
	}

	if content != "" {
		// The answer starts, so thinking is over. Upstreams that report
		// reasoning via reasoning_content never emit "</think>", so it is closed
		// here rather than waiting for the finish frame.
		s.closeReasoning(out)
		s.emitTextContent(out, choice.Index, content)
	}
}

// FlushResponses closes every open item and emits the terminal
// response.completed. It is a no-op once the stream has completed.
func FlushResponses(s *ResponsesState) []ResponsesEvent {
	if s.Completed {
		return nil
	}

	var out []ResponsesEvent
	for _, idx := range sortedIntKeys(s.MsgItemAdded) {
		s.closeMessage(&out, idx)
	}
	s.closeReasoning(&out)
	for _, idx := range sortedIntKeys(s.FuncCallIDs) {
		s.closeToolCall(&out, idx)
	}
	s.sendCompleted(&out)
	return out
}

// sendCompleted emits the terminal event carrying the finished Response object.
func (s *ResponsesState) sendCompleted(out *[]ResponsesEvent) {
	if s.Completed {
		return
	}
	s.Completed = true

	response := map[string]any{
		"id":         s.ResponseID,
		"object":     "response",
		"created_at": s.Created,
		"status":     "completed",
		"background": false,
		"error":      nil,
		"output":     s.collectCompletedOutputItems(),
	}
	if s.Usage != nil {
		response["usage"] = s.Usage
	}

	s.emit(out, "response.completed", map[string]any{
		"type":     "response.completed",
		"response": response,
	})
}
