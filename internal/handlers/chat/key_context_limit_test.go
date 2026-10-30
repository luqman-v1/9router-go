package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
)

// ctxWithKeyLimit returns a context carrying an API key with a context
// ceiling, the way the auth middleware leaves it.
func ctxWithKeyLimit(limit int) context.Context {
	return context.WithValue(context.Background(), middleware.ApiKeyContextKey, &models.APIKey{
		ID:           "k",
		Key:          "sk-test",
		ContextLimit: &limit,
	})
}

// TestKeyContextLimitReadsTheKeyColumn proves the ceiling comes from the
// authenticated key, and that a key without one reports 0 (no ceiling).
func TestKeyContextLimitReadsTheKeyColumn(t *testing.T) {
	if got := keyContextLimit(ctxWithKeyLimit(200_000)); got != 200_000 {
		t.Errorf("keyContextLimit = %d, want 200000", got)
	}
	if got := keyContextLimit(ctxWithKeyLimit(0)); got != 0 {
		t.Errorf("keyContextLimit for an unset ceiling = %d, want 0", got)
	}
	noKey := context.Background()
	if got := keyContextLimit(noKey); got != 0 {
		t.Errorf("keyContextLimit without a key = %d, want 0", got)
	}
}

// TestApplyKeyContextLimitTrimsALongHistory proves a request over the ceiling
// is trimmed rather than refused, and that the output budget is capped to
// what is left.
func TestApplyKeyContextLimitTrimsALongHistory(t *testing.T) {
	// A long conversation: alternating turns, well over a 2000 token ceiling.
	// The turns alternate because the trim keeps the oldest few and the
	// current one, and drops what is between them — a body of nothing but user
	// turns has no middle to drop.
	messages := longConversation(40, 400)
	body, err := json.Marshal(map[string]any{
		"model":      "test/model",
		"messages":   messages,
		"max_tokens": 4000,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	trimmed := applyKeyContextLimit(body, 2000)
	if len(trimmed) >= len(body) {
		t.Fatalf("trimmed body is not shorter: %d -> %d", len(body), len(trimmed))
	}

	var out struct {
		Messages  []map[string]any `json:"messages"`
		MaxTokens int              `json:"max_tokens"`
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		t.Fatalf("unmarshal trimmed: %v", err)
	}
	if len(out.Messages) >= len(messages) {
		t.Errorf("history was not trimmed: %d turns kept of %d", len(out.Messages), len(messages))
	}
	// The output budget must fit in what the ceiling leaves over.
	if out.MaxTokens <= 0 || out.MaxTokens >= 4000 {
		t.Errorf("max_tokens = %d, want a positive cap below the requested 4000", out.MaxTokens)
	}
	if out.MaxTokens > 2000 {
		t.Errorf("max_tokens = %d, want no more than the 2000-token ceiling", out.MaxTokens)
	}
}

// longConversation builds an alternating user/assistant conversation whose
// turns are the given character length.
func longConversation(turns, width int) []map[string]any {
	out := make([]map[string]any, 0, turns)
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": strings.Repeat("x", width),
		})
	}
	return out
}

// TestApplyKeyContextLimitLeavesAShortRequestAlone proves a request that fits
// is passed through byte for byte: a key with a ceiling must not reshape the
// traffic it was already allowed to send.
func TestApplyKeyContextLimitLeavesAShortRequestAlone(t *testing.T) {
	body := []byte(`{"model":"test/model","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`)
	if got := applyKeyContextLimit(body, 200_000); string(got) != string(body) {
		t.Errorf("body was changed:\n%s\nwant:\n%s", got, body)
	}
}

// TestApplyKeyContextLimitWithoutABudgetLeavesTheBody proves a request that
// names no output budget is not touched: there is nothing to cap against, and
// the model's own window is the one that applies.
func TestApplyKeyContextLimitWithoutABudgetLeavesTheBody(t *testing.T) {
	body := []byte(`{"model":"test/model","messages":[{"role":"user","content":"hi"}]}`)
	if got := applyKeyContextLimit(body, 200_000); string(got) != string(body) {
		t.Errorf("body was changed:\n%s\nwant:\n%s", got, body)
	}
}

// TestApplyKeyContextLimitWithoutACeilingLeavesTheBody proves a key without a
// ceiling changes nothing.
func TestApplyKeyContextLimitWithoutACeilingLeavesTheBody(t *testing.T) {
	body := []byte(`{"model":"test/model","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`)
	if got := applyKeyContextLimit(body, 0); string(got) != string(body) {
		t.Errorf("body was changed:\n%s\nwant:\n%s", got, body)
	}
}

// TestApplyKeyContextLimitCapsMaxCompletionTokens proves the OpenAI spelling
// of the output budget is capped too: an o-series model only accepts
// max_completion_tokens, so a ceiling that ignored it would not bind at all.
func TestApplyKeyContextLimitCapsMaxCompletionTokens(t *testing.T) {
	messages := longConversation(40, 400)
	body, err := json.Marshal(map[string]any{
		"model":                 "test/model",
		"messages":              messages,
		"max_completion_tokens": 4000,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	trimmed := applyKeyContextLimit(body, 2000)
	var out struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		t.Fatalf("unmarshal trimmed: %v", err)
	}
	if out.MaxCompletionTokens <= 0 || out.MaxCompletionTokens >= 4000 {
		t.Errorf("max_completion_tokens = %d, want a positive cap below 4000", out.MaxCompletionTokens)
	}
}

// TestTrimDoesNotOrphanAToolResult proves a result whose call was trimmed
// away is dropped with it: upstream answers a 400 for a result whose call is
// missing from the request.
func TestTrimDoesNotOrphanAToolResult(t *testing.T) {
	// The shape a trim leaves behind: the assistant turn that made the call is
	// gone, the result that names it is not.
	body := []byte(`{"messages":[
		{"role":"user","content":"first"},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]},
		{"role":"user","content":"second"}
	]}`)

	repaired := repairToolPairing(body)
	if strings.Contains(string(repaired), "tool_result") {
		t.Errorf("an orphaned tool_result survived:\n%s", repaired)
	}
}

// TestRepairToolPairingKeepsACompletePair proves the repair does not throw
// away a result whose call is still in the conversation.
func TestRepairToolPairingKeepsACompletePair(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}
	]}`)

	repaired := repairToolPairing(body)
	if !strings.Contains(string(repaired), "tool_result") {
		t.Errorf("a paired tool_result was dropped:\n%s", repaired)
	}
}
