package translator

import (
	"strings"
	"testing"

	json "encoding/json/v2"
)

// Gemini validates functionCall id uniqueness over the WHOLE history and answers
// 400 INVALID_ARGUMENT when one repeats. An OpenAI tool_call_id is only unique
// within its own assistant turn, so a long agent session can legitimately reuse
// one. These cases are the shapes that reach Gemini in practice.

// functionCallIDs and functionResponseIDs collect the ids the translator emitted,
// in document order, so a case can assert the pairing rather than just the set.
func geminiCallIDs(t *testing.T, body []byte) (calls, responses []string) {
	t.Helper()
	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal translated request: %v", err)
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				calls = append(calls, p.FunctionCall.ID)
			}
			if p.FunctionResponse != nil {
				responses = append(responses, p.FunctionResponse.ID)
			}
		}
	}
	return calls, responses
}

func assertUnique(t *testing.T, ids []string) {
	t.Helper()
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate functionCall id %q; Gemini rejects the whole request with 400 INVALID_ARGUMENT", id)
		}
		seen[id] = true
	}
}

// TestTranslateOpenAIToGemini_UniquifiesReplayedToolCallID is the #4532 case: an
// agent session replaying one id two turns later.
func TestTranslateOpenAIToGemini_UniquifiesReplayedToolCallID(t *testing.T) {
	openaiJSON := []byte(`{
		"model": "gemini-3.8-flash",
		"messages": [
			{"role": "user", "content": "read a.go"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_51859", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"a.go\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_51859", "content": "package a"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_51859", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"b.go\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_51859", "content": "package b"}
		]
	}`)

	body, err := TranslateOpenAIToGemini(openaiJSON)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}
	calls, responses := geminiCallIDs(t, body)
	assertUnique(t, calls)

	// Two calls sharing an id must get two DIFFERENT emitted ids, and each
	// result must answer the call it belongs to — otherwise the second
	// functionResponse is paired with the first call and the agent loop
	// silently cross-wires its own tools.
	if len(calls) != 2 {
		t.Fatalf("got %d functionCall parts, want 2", len(calls))
	}
	if calls[0] == calls[1] {
		t.Fatalf("both calls kept id %q; per-occurrence uniqueness is what the wire needs", calls[0])
	}
	if calls[0] != "call_51859" {
		t.Errorf("first call id = %q, want the original untouched", calls[0])
	}
	if len(responses) != 2 {
		t.Fatalf("got %d functionResponse parts, want 2", len(responses))
	}
	if responses[0] != calls[0] {
		t.Errorf("first response id = %q, want %q (the id its call was emitted with)", responses[0], calls[0])
	}
	if responses[1] != calls[1] {
		t.Errorf("second response id = %q, want %q", responses[1], calls[1])
	}
}

// TestTranslateOpenAIToGemini_UniqueIDsPassThroughUnchanged pins that a valid
// conversation is byte-identical: the rewrite may only touch an id that actually
// repeats.
func TestTranslateOpenAIToGemini_UniqueIDsPassThroughUnchanged(t *testing.T) {
	openaiJSON := []byte(`{
		"model": "gemini-3.8-flash",
		"messages": [
			{"role": "user", "content": "run both"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_a", "type": "function", "function": {"name": "f", "arguments": "{}"}},
				{"id": "call_b", "type": "function", "function": {"name": "g", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_a", "content": "1"},
			{"role": "tool", "tool_call_id": "call_b", "content": "2"}
		]
	}`)

	body, err := TranslateOpenAIToGemini(openaiJSON)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}
	calls, responses := geminiCallIDs(t, body)
	if len(calls) != 2 || calls[0] != "call_a" || calls[1] != "call_b" {
		t.Errorf("calls = %v, want [call_a call_b] unchanged", calls)
	}
	if len(responses) != 2 || responses[0] != "call_a" || responses[1] != "call_b" {
		t.Errorf("responses = %v, want [call_a call_b] unchanged", responses)
	}
}

// TestTranslateOpenAIToGemini_UniquifierKeepsCounting pins that an id already
// ending in "-2" does not collide with a generated one. The naive
// implementation emits call_x-2 for the first duplicate and then call_x-2 again
// when the client itself used that id, which Gemini rejects just as hard.
func TestTranslateOpenAIToGemini_UniquifierKeepsCounting(t *testing.T) {
	u := newToolCallIDUniquifier()
	got := []string{u.next("call_x"), u.next("call_x-2"), u.next("call_x"), u.next("call_x")}
	assertUnique(t, got)
	if got[0] != "call_x" {
		t.Errorf("first = %q, want call_x", got[0])
	}
	if got[2] == got[1] {
		t.Errorf("third = %q, collides with the client-supplied %q", got[2], got[1])
	}
}

// TestTranslateOpenAIToGemini_UniquifiedIDKeepsNoSuffix asserts the private
// thought-signature transport never reaches the wire through the new path. The
// uniquifier runs on the CLEAN id precisely so a "-2" is never appended to a
// "__ts__" id.
func TestTranslateOpenAIToGemini_UniquifiedIDKeepsNoSuffix(t *testing.T) {
	openaiJSON := []byte(`{
		"model": "gemini-3.8-flash",
		"messages": [
			{"role": "user", "content": "twice"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_x__ts__SIG1", "type": "function", "function": {"name": "f", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_x__ts__SIG1", "content": "1"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_x__ts__SIG2", "type": "function", "function": {"name": "f", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_x__ts__SIG2", "content": "2"}
		]
	}`)

	body, err := TranslateOpenAIToGemini(openaiJSON)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}
	if strings.Contains(string(body), "__ts__") {
		t.Fatalf("thought-signature transport suffix leaked: %s", body)
	}
	calls, responses := geminiCallIDs(t, body)
	assertUnique(t, calls)
	if len(calls) != 2 || len(responses) != 2 {
		t.Fatalf("got %d calls and %d responses, want 2 and 2", len(calls), len(responses))
	}
	if responses[0] != calls[0] || responses[1] != calls[1] {
		t.Errorf("pairing broken: calls %v responses %v", calls, responses)
	}
}
