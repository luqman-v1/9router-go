package chat

import (
	"slices"
	"testing"
)

// TestOrderModelsByCascadeCheapestFirst pins the ranking the strategy exists
// for: the escalation chain starts at the least expensive member, so a combo
// only spends on a capable model when the cheap one cannot serve the turn.
func TestOrderModelsByCascadeCheapestFirst(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "expensive first in storage, cheapest first out",
			input: []string{"openai/gpt-4o", "deepseek/deepseek-chat"},
			want:  []string{"deepseek/deepseek-chat", "openai/gpt-4o"},
		},
		{
			name:  "already cheap-first order is preserved",
			input: []string{"deepseek/deepseek-chat", "openai/gpt-4o"},
			want:  []string{"deepseek/deepseek-chat", "openai/gpt-4o"},
		},
		{
			name:  "three members ranked by blended rate",
			input: []string{"google/gemini-2.5-pro", "openai/gpt-4o-mini", "deepseek/deepseek-chat"},
			// gpt-4o-mini 0.75 < deepseek-chat 0.42? no: 0.14+0.28=0.42 is the
			// cheapest, gpt-4o-mini 0.15+0.6=0.75 next, gemini-2.5-pro last.
			want: []string{"deepseek/deepseek-chat", "openai/gpt-4o-mini", "google/gemini-2.5-pro"},
		},
		{
			// An unpriced member is a last resort, not a dropped one: the
			// combo keeps it rather than silently losing the candidate.
			name:  "unpriced model sorts after every priced model",
			input: []string{"mynode/some-new-model", "deepseek/deepseek-chat", "openai/gpt-4o"},
			want:  []string{"deepseek/deepseek-chat", "openai/gpt-4o", "mynode/some-new-model"},
		},
		{
			name:  "equal prices keep stored order",
			input: []string{"openai/gpt-4o-mini", "openai/gpt-4o-mini"},
			want:  []string{"openai/gpt-4o-mini", "openai/gpt-4o-mini"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OrderModelsByCascade(tt.input)
			if !slices.Equal(got, tt.want) {
				t.Errorf("OrderModelsByCascade() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOrderModelsByCascadeSingleMember pins that a one-member combo is
// returned untouched: there is no chain to order, and cloning would only add
// an allocation on the hot path.
func TestOrderModelsByCascadeSingleMember(t *testing.T) {
	one := []string{"openai/gpt-4o"}
	got := OrderModelsByCascade(one)
	if !slices.Equal(got, one) {
		t.Fatalf("single member changed: %v", got)
	}
}

// TestOrderModelsByCascadeDoesNotMutateInput pins that the caller's slice is
// left alone. applyComboStrategy's callers keep using the stored combo list
// after the strategy runs (the failure loop indexes it), so a strategy that
// sorted in place would corrupt the combo for the rest of the request.
func TestOrderModelsByCascadeDoesNotMutateInput(t *testing.T) {
	input := []string{"openai/gpt-4o", "deepseek/deepseek-chat"}
	before := slices.Clone(input)
	_ = OrderModelsByCascade(input)
	if !slices.Equal(input, before) {
		t.Fatalf("input slice mutated: got %v, want %v", input, before)
	}
}

// TestApplyComboStrategyCascade pins that the strategy name reaches the
// cascade ordering through applyComboStrategy, which is the path the combo
// dispatch takes when the operator has not set respectStoredOrder.
func TestApplyComboStrategyCascade(t *testing.T) {
	h := &ChatHandler{}
	input := []string{"openai/gpt-4o", "deepseek/deepseek-chat"}
	got := h.applyComboStrategy("cascade", input, "c", 1, true)
	want := []string{"deepseek/deepseek-chat", "openai/gpt-4o"}
	if !slices.Equal(got, want) {
		t.Fatalf("applyComboStrategy(cascade) = %v, want %v", got, want)
	}
}
