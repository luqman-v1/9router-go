package handlers

import (
	"reflect"
	"testing"
)

func TestApplyComboStrategy_capacity(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4", "claude-3", "gemini-pro"}
	got := h.applyComboStrategy("capacity", models)
	if !reflect.DeepEqual(got, models) {
		t.Errorf("capacity: got %v, want %v", got, models)
	}
	if len(got) > 0 && &got[0] == &models[0] {
		t.Error("capacity: returned same backing array, not a copy")
	}
}

func TestApplyComboStrategy_roundRobin(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b", "c"}
	first := h.applyComboStrategy("round-robin", models)
	if !reflect.DeepEqual(first, models) {
		t.Errorf("first call: got %v, want %v", first, models)
	}
	second := h.applyComboStrategy("round-robin", models)
	// rotated by 1: ["b", "c", "a"]
	want := []string{"b", "c", "a"}
	if !reflect.DeepEqual(second, want) {
		t.Errorf("second call: got %v, want %v", second, want)
	}
}

func TestApplyComboStrategy_fallback(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4", "claude-3", "gemini-pro"}
	got := h.applyComboStrategy("fallback", models)
	if !reflect.DeepEqual(got, models) {
		t.Errorf("fallback: got %v, want %v", got, models)
	}
	if len(got) > 0 && &got[0] == &models[0] {
		t.Error("fallback: returned same backing array, not a copy")
	}
}

func TestApplyComboStrategy_singleModel(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4"}
	t.Run("capacity", func(t *testing.T) {
		got := h.applyComboStrategy("capacity", models)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
	t.Run("round-robin", func(t *testing.T) {
		got := h.applyComboStrategy("round-robin", models)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		got := h.applyComboStrategy("fallback", models)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
}

func TestApplyComboStrategy_empty(t *testing.T) {
	h := NewChatHandler(nil)
	t.Run("capacity", func(t *testing.T) {
		got := h.applyComboStrategy("capacity", nil)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("round-robin", func(t *testing.T) {
		got := h.applyComboStrategy("round-robin", nil)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}
