package chat

import (
	"bytes"
	"testing"
)

// TestPrepareInboundThinkingDeterministic pins the byte-stability of the
// inbound body: the same logical request must serialize identically on every
// call, or a prefix-caching upstream misses the cache it would otherwise hit.
func TestPrepareInboundThinkingDeterministic(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"m","reasoning_effort":"high","max_tokens":128,"temperature":0.5,` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	first, _, _ := h.prepareInboundThinking("openai", "m", body)
	for i := 0; i < 100; i++ {
		got, _, _ := h.prepareInboundThinking("openai", "m", body)
		if !bytes.Equal(got, first) {
			t.Fatalf("the inbound body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}
