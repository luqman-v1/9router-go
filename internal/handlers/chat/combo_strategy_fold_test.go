package chat

import (
	"reflect"
	"testing"
)

func TestApplyComboStrategy_unknownFoldsToRoundRobin(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b", "c"}
	first := h.ApplyComboStrategy("nonsense", models, "combo-fold", 1)
	if !reflect.DeepEqual(first, models) {
		t.Errorf("first call: got %v, want %v", first, models)
	}
	second := h.ApplyComboStrategy("nonsense", models, "combo-fold", 1)
	want := []string{"b", "c", "a"}
	if !reflect.DeepEqual(second, want) {
		t.Errorf("second call: got %v, want rotated %v (unknown strategies resolve to round-robin)", second, want)
	}
}

func TestApplyComboStrategy_fusionKeepsPanelOrder(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b", "c"}
	for i := 0; i < 3; i++ {
		got := h.ApplyComboStrategy("fusion", models, "combo-fusion", 1)
		if !reflect.DeepEqual(got, models) {
			t.Fatalf("call %d: got %v, want the configured panel order %v", i, got, models)
		}
	}
}
