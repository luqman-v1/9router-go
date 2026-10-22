package chat

import (
	"bytes"
	"context"
	"testing"
)

// TestPrepareInboundThinkingDeterministic pins the byte-stability of the
// inbound body: the same logical request must serialize identically on every
// call, or a prefix-caching upstream misses the cache it would otherwise hit.
//
// The fixture must be one the pass actually rewrites: a body the pass leaves
// alone is returned byte for byte, so the assertion would pass through the
// passthrough and never reach the deterministic marshal. A thinking block on
// a request whose last turn is not a user turn is dropped by the pass, so
// that is the rewrite this fixture pins.
func TestPrepareInboundThinkingDeterministic(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"m","reasoning_effort":"high","max_tokens":128,"temperature":0.5,` +
		`"thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`)
	first, _, _ := h.prepareInboundThinking(context.Background(), "openai", "m", body, false)
	if bytes.Contains(first, []byte(`"thinking"`)) {
		t.Fatalf("the fixture must be rewritten by the pass, otherwise the determinism assertion is vacuous: %s", first)
	}
	for i := 0; i < 100; i++ {
		got, _, _ := h.prepareInboundThinking(context.Background(), "openai", "m", body, false)
		if !bytes.Equal(got, first) {
			t.Fatalf("the inbound body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}
