package executor

import (
	"encoding/json"
	"testing"
)

// TestAutoclawBodyNonStreamKeepsClientFlag pins the non-stream half of the
// wire contract (PR #1506 review item 6): autoclawBody must force stream:true
// only when the client asked for a stream, so a stream:false request never
// comes back as SSE bytes labelled application/json. The stream:true half is
// covered by TestAutoclawBodyForcesStream in autoclaw_test.go.
func TestAutoclawBodyNonStreamKeepsClientFlag(t *testing.T) {
	body := []byte(`{"model":"zai_glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)

	out, err := autoclawBody(body, false)
	if err != nil {
		t.Fatalf("autoclawBody: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// stream must stay unset: the client's flag is preserved verbatim.
	if _, present := m["stream"]; present {
		t.Errorf("stream = %v, want the key absent for a non-stream request", m["stream"])
	}

	// The model field must survive the round-trip untouched: upstream reads
	// it from X-Request-Model, so the body copy is only a fallback.
	if m["model"] != "zai_glm-5.3-flash" {
		t.Errorf("model = %v, want preserved", m["model"])
	}
}
