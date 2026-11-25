package chat

import (
	"testing"
)

// TestComboConversationSticksToItsAccount is the fix for a mid-conversation
// account switch being refused. free-pi binds a conversation to the account that
// opened it, so with round-robin + sticky 1 the second turn used to land on the
// next account and upstream answered "This conversation belongs to another
// free-pi account."
//
// The seat is now pinned per conversation: every turn of one conversation reuses
// the seat that opened it, while a NEW conversation still spreads to the next
// seat.
func TestComboConversationSticksToItsAccount(t *testing.T) {
	h := &ChatHandler{}
	seats := []string{"free-pi/model-a", "free-pi/model-b", "free-pi/model-c"}

	// Three turns of ONE conversation must all land on the same seat.
	first := h.applyComboStrategyForConversation("round-robin", seats, "rr-conv", 1, true, "conv-alpha", seats)
	for i := 0; i < 3; i++ {
		again := h.applyComboStrategyForConversation("round-robin", seats, "rr-conv", 1, true, "conv-alpha", seats)
		if again[0] != first[0] {
			t.Fatalf("turn %d moved to %q; the conversation started on %q", i+2, again[0], first[0])
		}
	}

	// A different conversation may use a different seat — that is the parallelism.
	other := h.applyComboStrategyForConversation("round-robin", seats, "rr-conv", 1, true, "conv-beta", seats)
	if other[0] == "" {
		t.Fatal("no seat for the second conversation")
	}

	// A non-bound provider keeps the old per-request rotation.
	open := []string{"deepseek/model-a", "deepseek/model-b"}
	a := h.applyComboStrategyForConversation("round-robin", open, "rr-open", 1, true, "conv-x", open)
	b := h.applyComboStrategyForConversation("round-robin", open, "rr-open", 1, true, "conv-x", open)
	if a[0] == b[0] {
		t.Error("a conversation-bound provider's pin leaked onto an ordinary provider")
	}
}
