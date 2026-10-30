package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"9router/proxy/internal/middleware"
)

// longRequest builds a conversation of turns alternating user and assistant,
// each `width` characters wide.
func longRequest(turns, width int, budget map[string]any) []byte {
	msgs := make([]map[string]any, 0, turns)
	for i := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]any{"role": role, "content": strings.Repeat("x", width)})
	}
	body := map[string]any{"model": "test/model", "messages": msgs}
	for k, v := range budget {
		body[k] = v
	}
	out, _ := json.Marshal(body)
	return out
}

// tokensOf measures a decoded body the way the ceiling measures it.
func tokensOf(t *testing.T, body []byte) int {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return middlewareTokens(req)
}

// TestContextLimitTrimsABudgetlessRequest proves a request that names no
// output budget is still trimmed.
//
// This was the hole: the ceiling only applied to requests that declared
// max_tokens, so a client that let the model pick its own output budget sent
// an unbounded conversation straight through.
func TestContextLimitTrimsABudgetlessRequest(t *testing.T) {
	body := longRequest(30, 8000, nil)
	out, err := applyKeyContextLimit(body, 20000, 200000, 4096)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	got := tokensOf(t, out)
	if got > 20000 {
		t.Errorf("trimmed request is %d tokens, want at most 20000", got)
	}
	if len(out) >= len(body) {
		t.Errorf("request was not trimmed: %d bytes -> %d bytes", len(body), len(out))
	}
}

// TestContextLimitTrimsARequestWithAMaxTokens proves the classic case still
// works: the history is trimmed and the output budget is clamped to the room
// that is left.
func TestContextLimitTrimsARequestWithAMaxTokens(t *testing.T) {
	body := longRequest(30, 8000, map[string]any{"max_tokens": 4096})
	out, err := applyKeyContextLimit(body, 20000, 200000, 4096)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	got := tokensOf(t, out)
	if got > 20000 {
		t.Errorf("trimmed request is %d tokens, want at most 20000", got)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	maxTokens, _ := req["max_tokens"].(float64)
	if int(maxTokens) > 4096 {
		t.Errorf("max_tokens = %v, want it clamped to the room left", maxTokens)
	}
}

// TestContextLimitLeavesASmallRequestAlone proves a request that already fits
// is passed through byte for byte.
func TestContextLimitLeavesASmallRequestAlone(t *testing.T) {
	body := longRequest(4, 100, map[string]any{"max_tokens": 1024})
	out, err := applyKeyContextLimit(body, 20000, 200000, 4096)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("body was changed:\n%s\nwant:\n%s", out, body)
	}
}

// TestContextLimitRefusesARequestThatCannotFit proves a request whose own turn
// is over the ceiling is refused rather than sent over it.
//
// The ceiling is the operator's promise about what this key may cost. A
// request that cannot be trimmed into it breaks that promise silently, which is
// worse than a refusal.
func TestContextLimitRefusesARequestThatCannotFit(t *testing.T) {
	// One user message far wider than the whole ceiling: there is no history
	// to drop, so no trim can bring it under.
	body := longRequest(1, 400000, nil)
	if _, err := applyKeyContextLimit(body, 20000, 200000, 4096); err == nil {
		t.Fatal("an untrimmable request over the ceiling was accepted")
	}
}

// TestContextLimitClampsTheOutputWhenItEatsTheWindow proves a ceiling smaller
// than the model's output allowance still serves the request.
//
// The output budget is the client's wish; the ceiling is the operator's rule.
// When the wish alone would fill the whole window, the output is clamped to
// the room the input leaves rather than the request being refused — a ceiling
// that rejects everything would make the key useless.
func TestContextLimitClampsTheOutputWhenItEatsTheWindow(t *testing.T) {
	body := longRequest(4, 4000, map[string]any{"max_tokens": 128000})
	out, err := applyKeyContextLimit(body, 1024, 200000, 128000)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	maxTokens, ok := req["max_tokens"].(float64)
	if !ok {
		t.Fatal("max_tokens is gone from the request")
	}
	if int(maxTokens) > 1024 {
		t.Errorf("max_tokens = %v, want it clamped inside the 1024-token ceiling", int(maxTokens))
	}
}

// TestContextLimitClampsEveryOutputSpelling proves the ceiling holds on the
// field that actually reaches the wire.
//
// A body declaring both max_tokens and max_completion_tokens keeps the latter
// — the normalizer drops the former — so a ceiling that clamped only
// max_tokens would be discarded before the request left.
func TestContextLimitClampsEveryOutputSpelling(t *testing.T) {
	body := longRequest(30, 8000, map[string]any{
		"max_tokens":            64000,
		"max_completion_tokens": 64000,
	})
	out, err := applyKeyContextLimit(body, 20000, 200000, 4096)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		v, ok := req[field].(float64)
		if !ok {
			continue
		}
		if int(v) > 20000 {
			t.Errorf("%s = %v, want it clamped inside the ceiling", field, int(v))
		}
	}
}

// TestContextLimitClampsTheResponsesSpelling proves the Responses dialect's
// max_output_tokens is the field the ceiling clamps.
//
// A native Responses upstream keeps that spelling all the way to the wire, so
// a ceiling that ignored it would not bound the request at all.
func TestContextLimitClampsTheResponsesSpelling(t *testing.T) {
	// The Responses dialect carries its turns under "input", alternating
	// between a user turn and the assistant's reply.
	var input []map[string]any
	for i := range 30 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		input = append(input, map[string]any{"role": role, "content": strings.Repeat("x", 8000)})
	}
	body, _ := json.Marshal(map[string]any{
		"model":             "test/model",
		"input":             input,
		"max_output_tokens": 64000,
	})
	out, err := applyKeyContextLimit(body, 20000, 200000, 4096)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, ok := req["max_output_tokens"].(float64)
	if !ok {
		t.Fatal("max_output_tokens is gone from the request")
	}
	if int(v) > 20000 {
		t.Errorf("max_output_tokens = %v, want it clamped inside the ceiling", int(v))
	}
}

// TestContextLimitKeepsTheToolPairingIntact proves a trimmed conversation
// still names the tool call its result belongs to.
//
// Dropping the middle can take the assistant turn that made a call with it,
// leaving the result orphaned — which upstream answers with a 400.
func TestContextLimitKeepsTheToolPairingIntact(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"model": "test/model",
		"messages": []map[string]any{
			{"role": "user", "content": strings.Repeat("a", 40000)},
			{"role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "call_1", "name": "lookup", "input": map[string]any{}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "call_1", "content": "done"},
			}},
			{"role": "user", "content": strings.Repeat("b", 40000)},
		},
		"max_tokens": 4096,
	})
	out, err := applyKeyContextLimit(body, 20000, 200000, 4096)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	if strings.Contains(string(out), "tool_result") {
		var req map[string]any
		_ = json.Unmarshal(out, &req)
		msgs, _ := req["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if blocks, ok := mm["content"].([]any); ok {
				for _, b := range blocks {
					bm, _ := b.(map[string]any)
					if bm["type"] == "tool_result" {
						t.Error("an orphaned tool_result survived the trim")
					}
				}
			}
		}
	}
}

// TestContextLimitWithoutACeilingLeavesTheBodyAlone proves a key without a
// ceiling sends exactly what it sent.
func TestContextLimitWithoutACeilingLeavesTheBodyAlone(t *testing.T) {
	body := longRequest(30, 8000, nil)
	out, err := applyKeyContextLimit(body, 0, 200000, 4096)
	if err != nil {
		t.Fatalf("applyKeyContextLimit: %v", err)
	}
	if string(out) != string(body) {
		t.Error("body was changed for a key without a ceiling")
	}
}

// middlewareTokens measures a decoded body with the limiter's own estimator.
func middlewareTokens(req map[string]any) int {
	return middleware.EstimatePromptTokensFromMap(req)
}
