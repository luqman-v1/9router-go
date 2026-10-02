package translator

import "testing"

// TestCompletionPendingReportsADeferredTerminalEvent pins the flag the stream
// producer reads to decide the client is owed a terminal event. Getting it
// wrong in either direction is costly: false and every slow provider gets cut
// off, true and a stalled one keeps a client waiting forever.
func TestCompletionPendingReportsADeferredTerminalEvent(t *testing.T) {
	stop := "stop"
	cases := []struct {
		name          string
		flushReaches  bool
		flush         bool
		chunks        []*OpenAIChunk
		wantAfterLast bool
		wantCompleted bool
	}{
		{
			name:         "finish reason without usage defers the completion",
			flushReaches: true,
			chunks: []*OpenAIChunk{
				{Choices: []OpenAIChoice{{Index: 0, Delta: OpenAIDelta{Content: "hi"}}}},
				{Choices: []OpenAIChoice{{Index: 0, FinishReason: &stop}}},
			},
			wantAfterLast: true,
		},
		{
			name:         "a later usage trailer still leaves the completion deferred",
			flushReaches: true,
			chunks: []*OpenAIChunk{
				{Choices: []OpenAIChoice{{Index: 0, Delta: OpenAIDelta{Content: "hi"}}}},
				{Choices: []OpenAIChoice{{Index: 0, FinishReason: &stop}}},
				{Usage: &OpenAIUsage{PromptTokens: 3, CompletionTokens: 1}},
			},
			wantAfterLast: true,
		},
		{
			name:          "the flush delivers the deferred completion",
			flushReaches:  true,
			chunks:        []*OpenAIChunk{{Choices: []OpenAIChoice{{Index: 0, FinishReason: &stop}}}},
			wantAfterLast: true,
			flush:         true,
			wantCompleted: true,
		},
		{
			name:         "usage on the finish chunk completes immediately",
			flushReaches: true,
			chunks: []*OpenAIChunk{
				{Choices: []OpenAIChoice{{Index: 0, FinishReason: &stop}}, Usage: &OpenAIUsage{PromptTokens: 3}},
			},
			wantAfterLast: false,
			wantCompleted: true,
		},
		{
			name:         "a second-hop pivot completes instead of deferring",
			flushReaches: false,
			chunks: []*OpenAIChunk{
				{Choices: []OpenAIChoice{{Index: 0, FinishReason: &stop}}},
			},
			wantAfterLast: false,
			wantCompleted: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := InitResponsesState("gpt-x", tt.flushReaches)
			for _, chunk := range tt.chunks {
				TranslateOpenAIToResponses(chunk, s)
			}
			// The flag is what the producer reads between upstream events, so
			// it is asserted before the optional flush rather than after.
			if got := s.CompletionPending && !s.Completed; got != tt.wantAfterLast {
				t.Errorf("CompletionPending = %v (completed=%v), want %v", got, s.Completed, tt.wantAfterLast)
			}
			if tt.flush {
				FlushResponses(s)
			}
			if s.Completed != tt.wantCompleted {
				t.Errorf("Completed = %v, want %v", s.Completed, tt.wantCompleted)
			}
		})
	}
}

// TestFlushClearsAPendingCompletion guards the watchdog's own exit: once the
// deferred event has been written there is nothing left to report, so a
// producer polling the flag does not keep believing it still owes one.
func TestFlushClearsAPendingCompletion(t *testing.T) {
	stop := "stop"
	s := InitResponsesState("gpt-x", true)
	TranslateOpenAIToResponses(&OpenAIChunk{Choices: []OpenAIChoice{{Index: 0, FinishReason: &stop}}}, s)
	if !s.CompletionPending {
		t.Fatal("the completion must be reported as pending before the flush")
	}

	FlushResponses(s)
	if s.CompletionPending && !s.Completed {
		t.Error("a flushed stream owes the client nothing, but the completion is still reported as pending")
	}
	if !s.Completed {
		t.Error("the flush must mark the stream completed")
	}
}
