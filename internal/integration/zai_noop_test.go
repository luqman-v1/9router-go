//go:build integration

package integration

import (
	"net/http"
	"testing"

	json "encoding/json/v2"
)

// TestZaiLaneLeavesABodyWithoutThinkingAlone pins the property the retired
// Z.ai transform owned: a body that asks for no reasoning reaches the upstream
// without a thinking field. The transform itself now lives in internal/thinking
// and runs at the dispatch choke point, so the executor's own copy is gone —
// the no-op case is asserted on the wire instead.
func TestZaiLaneLeavesABodyWithoutThinkingAlone(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "glm-1", "glm", "glm", up, "test-key")

	res := env.Post(t, "/v1/chat/completions", ChatBody("glm/glm-5.2", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	for _, k := range []string{"thinking", "reasoning_effort", "enable_thinking", "reasoning"} {
		if _, has := wire[k]; has {
			t.Errorf("reasoning field %q reached the upstream: %s", k, truncate(sent.Body))
		}
	}
}

// TestZaiLaneRejectsAnUnparsableBody pins the other half of the retired
// transform: a body the gateway cannot read is the client's error, not a
// mis-routed request. The transform used to swallow a parse error and forward
// the body unchanged; the dispatch path rejects it instead.
func TestZaiLaneRejectsAnUnparsableBody(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "glm-1", "glm", "glm", up, "test-key")

	res := env.Post(t, "/v1/chat/completions", "not a request object")
	if res.Status == http.StatusOK {
		t.Fatalf("a body that is not a request object must not be forwarded, got 200")
	}
	if up.Count() != 0 {
		t.Errorf("the upstream must not see an unparsable request, got %d requests", up.Count())
	}
}
