package executor

import (
	"crypto/sha256"
	"fmt"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/translator"
)

// The deepseek rewrite chains must serialize deterministically end to end, or
// DeepSeek's byte-prefix prompt cache misses on every request. These tests call
// the real functions on the real lane order; no provider is contacted.

func distinctBodies(t *testing.T, n int, build func() []byte) int {
	t.Helper()
	seen := map[string]struct{}{}
	for i := 0; i < n; i++ {
		seen[fmt.Sprintf("%x", sha256.Sum256(build()))] = struct{}{}
	}
	return len(seen)
}

// Full zen chat lane rewrite order (see forwardZenChat): inject, conceal, then
// stamp stream/model via withJSONFields — the last writer on the lane.
func TestZenChatLaneChainDeterministic(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4-pro","stream":false,"messages":[
		{"role":"system","content":"You are a coding agent."},
		{"role":"user","content":"Read the config file."},
		{"role":"assistant","content":"Reading it now."}]}`)

	n := distinctBodies(t, 200, func() []byte {
		b := InjectReasoningContent(in, "opencode-zen")
		b, _ = translator.ConcealFingerprintTools(b)
		return withJSONFields(b, map[string]any{"stream": true, "model": "deepseek-v4-pro"})
	})
	if n != 1 {
		t.Errorf("zen chat lane chain produced %d distinct bodies over 200 identical requests, want 1", n)
	}
}

// ForwardOpencode (free tier) stamps stream/model after injecting.
func TestOpenCodeLaneChainDeterministic(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4-flash-free","messages":[
		{"role":"user","content":"hello"},
		{"role":"assistant","content":"hi"}]}`)

	n := distinctBodies(t, 200, func() []byte {
		b := InjectReasoningContent(in, "opencode")
		b, _ = translator.ConcealFingerprintTools(b)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		m["stream"] = true
		m["model"] = "deepseek-v4-flash-free"
		out, err := marshalStable(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
	if n != 1 {
		t.Errorf("opencode lane chain produced %d distinct bodies over 200 identical requests, want 1", n)
	}
}
