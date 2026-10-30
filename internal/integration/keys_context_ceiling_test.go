//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// bigConversation builds a conversation whose prompt is far larger than the
// ceiling under test, so a request that is not held to the ceiling is obvious
// in what the upstream received.
func bigConversation(turns, width int, budget map[string]any) map[string]any {
	msgs := make([]map[string]any, 0, turns)
	for i := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]any{"role": role, "content": strings.Repeat("x", width)})
	}
	body := map[string]any{
		"model":    "deepseek/deepseek-chat",
		"messages": msgs,
	}
	for k, v := range budget {
		body[k] = v
	}
	return body
}

// TestKeyContextCeilingHoldsTheRequest proves a key's context ceiling is
// enforced on the way to the upstream.
//
// The ceiling used to be applied only to requests that named an output budget,
// so a client that let the model pick its own budget sent an unbounded
// conversation straight through. The request the upstream receives must fit
// under the ceiling, whatever the client asked for.
func TestKeyContextCeilingHoldsTheRequest(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "ceiling")

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"contextLimit": 20000}); put.Status != http.StatusOK {
		t.Fatalf("set context limit: status %d: %s", put.Status, truncate(put.Body))
	}

	// No output budget: the shape that used to skip the ceiling entirely.
	res := env.Post(t, "/v1/chat/completions", bigConversation(30, 8000, nil), WithAPIKey(plaintext))
	if res.Status != http.StatusOK {
		t.Fatalf("request = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := upstream.Last(t).Body
	if len(sent) > 20000*4 {
		t.Errorf("upstream received %d bytes, want the request held to 20000 tokens (~80000 chars)", len(sent))
	}
}

// TestKeyContextCeilingClampsTheOutputBudget proves the answer may not push
// the request past the ceiling: the output budget is clamped to the room the
// trimmed input leaves.
func TestKeyContextCeilingClampsTheOutputBudget(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "ceiling-output")

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"contextLimit": 20000}); put.Status != http.StatusOK {
		t.Fatalf("set context limit: status %d: %s", put.Status, truncate(put.Body))
	}

	res := env.Post(t, "/v1/chat/completions",
		bigConversation(30, 8000, map[string]any{"max_tokens": 100000}),
		WithAPIKey(plaintext))
	if res.Status != http.StatusOK {
		t.Fatalf("request = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := string(upstream.Last(t).Body)
	// The budget is clamped to the room the trimmed input leaves, which is
	// under the ceiling rather than equal to it.
	if !strings.Contains(sent, `"max_tokens":`) {
		t.Fatalf("the output budget is gone from the request: %s", truncate([]byte(sent)))
	}
	clamped := budgetIn(t, sent, "max_tokens")
	if clamped <= 0 || clamped > 20000 {
		t.Errorf("max_tokens = %d, want it clamped inside the 20000-token ceiling", clamped)
	}
}

// budgetIn reads a numeric field out of a JSON body.
func budgetIn(t *testing.T, body, field string) int {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	v, _ := decoded[field].(float64)
	return int(v)
}

// TestKeyContextCeilingRefusesAnUntrimmableRequest proves a request that
// cannot be made to fit is refused rather than sent over the ceiling.
//
// The ceiling is the operator's promise about what this key may cost. Sending
// the request anyway breaks it silently, which is worse than a refusal.
func TestKeyContextCeilingRefusesAnUntrimmableRequest(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "ceiling-refuse")

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"contextLimit": 20000}); put.Status != http.StatusOK {
		t.Fatalf("set context limit: status %d: %s", put.Status, truncate(put.Body))
	}

	before := upstream.Count()
	res := env.Post(t, "/v1/chat/completions", bigConversation(1, 400000, nil), WithAPIKey(plaintext))
	if res.Status != http.StatusBadRequest {
		t.Fatalf("request = %d, want 400 (body: %s)", res.Status, truncate(res.Body))
	}
	if upstream.Count() != before {
		t.Error("the refused request still reached the upstream")
	}
}

// TestKeyContextCeilingHoldsOnTheResponsesLane proves the ceiling reaches the
// Responses dialect too.
//
// The lane had no ceiling at all: a client on /v1/responses could send any
// conversation the model would take, whatever the key was sold.
func TestKeyContextCeilingHoldsOnTheResponsesLane(t *testing.T) {
	env, upstream := newProviderEnv(t)
	id, plaintext := limitKey(t, env, "ceiling-responses")

	if put := env.Put(t, "/api/keys/"+id, map[string]any{"contextLimit": 20000}); put.Status != http.StatusOK {
		t.Fatalf("set context limit: status %d: %s", put.Status, truncate(put.Body))
	}

	var input []map[string]any
	for i := range 30 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		input = append(input, map[string]any{"role": role, "content": strings.Repeat("x", 8000)})
	}
	res := env.Post(t, "/v1/responses", map[string]any{
		"model": "deepseek/deepseek-chat",
		"input": input,
	}, WithAPIKey(plaintext))
	if res.Status != http.StatusOK {
		t.Fatalf("request = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := upstream.Last(t).Body
	if len(sent) > 20000*4 {
		t.Errorf("upstream received %d bytes, want the request held to 20000 tokens (~80000 chars)", len(sent))
	}
}
