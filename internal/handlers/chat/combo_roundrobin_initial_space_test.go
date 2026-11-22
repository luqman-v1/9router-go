package chat

import (
	"reflect"
	"testing"
)

// A seat dropped by capability or affinity filtering for one turn must keep its
// rotation slot: with three configured targets and p/b filtered on turn 3, the
// pointer still walks all three, so turn 4 serves p/c rather than p/a twice.
func TestApplyComboStrategy_RoundRobinInitialSpace(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetComboState("rr-initial-space")

	all := []string{"p/a", "p/b", "p/c"}
	var own []string
	heads := make([]string, 0, 6)
	for turn := range 6 {
		models := all
		if turn == 0 {
			// Capability filter drops p/b for the first turn.
			models = []string{"p/a", "p/c"}
			own = all
		}
		rotated := h.applyComboStrategyWithSeats("round-robin", models, "rr-initial-space", 1, true, own)
		heads = append(heads, rotated[0])
	}

	want := []string{"p/a", "p/b", "p/c", "p/a", "p/b", "p/c"}
	if !reflect.DeepEqual(heads, want) {
		t.Errorf("heads: got %v, want %v", heads, want)
	}
}

// The same filtering on every turn: the pointer still advances through the full
// target space, so the two seats that remain share the traffic instead of one
// of them pinning the rotation at index 0.
func TestApplyComboStrategy_RoundRobinInitialSpaceFilteredEveryTurn(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetComboState("rr-initial-space-filtered")

	all := []string{"p/a", "p/b", "p/c"}
	filtered := []string{"p/a", "p/c"}

	counts := map[string]int{}
	for range 6 {
		rotated := h.applyComboStrategyWithSeats("round-robin", filtered, "rr-initial-space-filtered", 1, true, all)
		counts[rotated[0]]++
	}

	// The pointer walks all three configured seats; when p/b is filtered, the
	// turn that would serve p/b instead serves p/c (the next seat). Over 6
	// turns: p/a, p/c, p/c, p/a, p/c, p/c — two full cycles with p/b skipped.
	want := map[string]int{"p/a": 2, "p/c": 4}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("heads: got %v, want %v", counts, want)
	}
}

// Without the pointer-space advance, filtering p/b out of every turn pins the
// rotation on p/a: the second and later turns all schedule a filtered seat.
func TestApplyComboStrategy_RoundRobinInitialSpacePinnedWithoutFix(t *testing.T) {
	h := NewChatHandler(nil)
	h.ResetComboState("rr-initial-space-pinned")

	all := []string{"p/a", "p/b", "p/c"}
	filtered := []string{"p/a", "p/c"}

	counts := map[string]int{}
	for range 6 {
		rotated := h.applyComboStrategyWithSeats("round-robin", filtered, "rr-initial-space-pinned", 1, true, all)
		counts[rotated[0]]++
	}
	if counts["p/c"] == 0 {
		t.Errorf("p/c never served, rotation stuck on %v", counts)
	}
}
