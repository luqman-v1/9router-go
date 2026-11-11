package chat

import (
	"context"
	json "encoding/json/v2"
	"reflect"
	"slices"
	"testing"

	"9router/proxy/internal/db"
)

func tallyFirst(h *ChatHandler, models []string, comboName string, stickyLimit int, calls int, weights ...map[string]int) map[string]int {
	counts := make(map[string]int)
	var w map[string]int
	if len(weights) > 0 {
		w = weights[0]
	}
	for i := 0; i < calls; i++ {
		rotated := h.ApplyComboStrategy("round-robin", models, comboName, stickyLimit, w)
		head := rotated[0]
		counts[head]++
	}
	return counts
}

// Combos without explicit weights default to equal distribution.
func TestComboWeighted_DefaultEqualDistribution(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b", "p/c"}

	// 1. Nil weights
	h.ResetComboState("unweighted-nil")
	countsNil := tallyFirst(h, models, "unweighted-nil", 1, 9, nil)
	expected := map[string]int{"p/a": 3, "p/b": 3, "p/c": 3}
	if !reflect.DeepEqual(countsNil, expected) {
		t.Errorf("nil weights: got %v, want %v", countsNil, expected)
	}

	// 2. All weights equal to 1
	h.ResetComboState("unweighted-ones")
	countsOnes := tallyFirst(h, models, "unweighted-ones", 1, 9, map[string]int{"p/a": 1, "p/b": 1, "p/c": 1})
	if !reflect.DeepEqual(countsOnes, expected) {
		t.Errorf("all 1 weights: got %v, want %v", countsOnes, expected)
	}

	// 3. Sequential rotation matches legacy round-robin exactly
	h.ResetComboState("legacy")
	r1 := h.ApplyComboStrategy("round-robin", models, "legacy", 1)
	r2 := h.ApplyComboStrategy("round-robin", models, "legacy", 1)
	r3 := h.ApplyComboStrategy("round-robin", models, "legacy", 1)
	if !slices.Equal(r1, []string{"p/a", "p/b", "p/c"}) {
		t.Errorf("r1 = %v, want [p/a p/b p/c]", r1)
	}
	if !slices.Equal(r2, []string{"p/b", "p/c", "p/a"}) {
		t.Errorf("r2 = %v, want [p/b p/c p/a]", r2)
	}
	if !slices.Equal(r3, []string{"p/c", "p/a", "p/b"}) {
		t.Errorf("r3 = %v, want [p/c p/a p/b]", r3)
	}
}

// Weighted combo produces the expected proportional distribution across sequential requests.
func TestComboWeighted_ProportionalDistribution(t *testing.T) {
	h := NewChatHandler(nil)

	// Case 1: 3 to 1 ratio between two models
	pair := []string{"modelA", "modelB"}
	weightsPair := map[string]int{"modelA": 3, "modelB": 1}

	// 4 requests: exactly 3 to modelA, 1 to modelB
	h.ResetComboState("pair")
	counts4 := tallyFirst(h, pair, "pair", 1, 4, weightsPair)
	if counts4["modelA"] != 3 || counts4["modelB"] != 1 {
		t.Errorf("4 requests: got %v, want modelA: 3, modelB: 1", counts4)
	}

	// 12 requests: exactly 9 to modelA, 3 to modelB
	h.ResetComboState("pair12")
	counts12 := tallyFirst(h, pair, "pair12", 1, 12, weightsPair)
	if counts12["modelA"] != 9 || counts12["modelB"] != 3 {
		t.Errorf("12 requests: got %v, want modelA: 9, modelB: 3", counts12)
	}

	// Case 2: 5:3:1 ratio among three models
	triplet := []string{"p/a", "p/b", "p/c"}
	weightsTriplet := map[string]int{"p/a": 5, "p/b": 3, "p/c": 1}

	h.ResetComboState("triplet")
	counts9 := tallyFirst(h, triplet, "triplet", 1, 9, weightsTriplet)
	want9 := map[string]int{"p/a": 5, "p/b": 3, "p/c": 1}
	if !reflect.DeepEqual(counts9, want9) {
		t.Errorf("9 requests: got %v, want %v", counts9, want9)
	}
}

// In every call, every model is returned exactly once with no duplicates or dropped models.
func TestComboWeighted_ReturnsEveryModelOnce(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b", "p/c"}
	weights := map[string]int{"p/a": 5, "p/b": 3, "p/c": 1}

	expectedSorted := slices.Clone(models)
	slices.Sort(expectedSorted)

	for i := 0; i < 9; i++ {
		rotated := h.ApplyComboStrategy("round-robin", models, "once-test", 1, weights)
		if len(rotated) != len(models) {
			t.Fatalf("call %d: got length %d, want %d", i, len(rotated), len(models))
		}
		sorted := slices.Clone(rotated)
		slices.Sort(sorted)
		if !slices.Equal(sorted, expectedSorted) {
			t.Errorf("call %d: rotated %v does not contain all models %v", i, rotated, expectedSorted)
		}
	}
}

// Handles zero-weight models safely: excluded from head, placed at tail as last-resort fallback.
func TestComboWeighted_ZeroWeightSafeHandling(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b", "p/c"}
	weights := map[string]int{"p/a": 4, "p/b": 0, "p/c": 1}

	h.ResetComboState("zero-test")
	for i := 0; i < 10; i++ {
		rotated := h.ApplyComboStrategy("round-robin", models, "zero-test", 1, weights)
		// Zero-weight model p/b must never be the first candidate (head)
		if rotated[0] == "p/b" {
			t.Fatalf("call %d: zero-weight model p/b was selected as head: %v", i, rotated)
		}
		// Zero-weight model p/b must be kept at the end of the fallback chain
		if rotated[len(rotated)-1] != "p/b" {
			t.Fatalf("call %d: zero-weight model p/b not at tail: %v", i, rotated)
		}
	}
}

// When all weights are zero, falls back to plain round-robin rotation without crashing.
func TestComboWeighted_AllZeroFallback(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b", "p/c"}
	weights := map[string]int{"p/a": 0, "p/b": 0, "p/c": 0}

	h.ResetComboState("all-zero")
	r1 := h.ApplyComboStrategy("round-robin", models, "all-zero", 1, weights)
	r2 := h.ApplyComboStrategy("round-robin", models, "all-zero", 1, weights)

	if !slices.Equal(r1, models) {
		t.Errorf("r1 = %v, want %v", r1, models)
	}
	if !slices.Equal(r2, []string{"p/b", "p/c", "p/a"}) {
		t.Errorf("r2 = %v, want [p/b p/c p/a]", r2)
	}
}

// Sticky limit: model stays as head for stickyLimit turns before rotating.
func TestComboWeighted_StickyLimit(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b"}
	weights := map[string]int{"p/a": 2, "p/b": 1}

	h.ResetComboState("sticky-weighted")
	var heads []string
	for i := 0; i < 6; i++ {
		rotated := h.ApplyComboStrategy("round-robin", models, "sticky-weighted", 2, weights)
		heads = append(heads, rotated[0])
	}

	// With weights 2:1 and stickyLimit=2:
	// A: 2 ticks (calls 1, 2)
	// B: 2 ticks (calls 3, 4)
	// A: 2 ticks (calls 5, 6)
	wantHeads := []string{"p/a", "p/a", "p/b", "p/b", "p/a", "p/a"}
	if !slices.Equal(heads, wantHeads) {
		t.Errorf("sticky heads: got %v, want %v", heads, wantHeads)
	}
}

// Mid-turn requests (newTurn=false) reuse the serving model.
func TestComboWeighted_MidTurnReuse(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b"}
	weights := map[string]int{"p/a": 3, "p/b": 1}

	h.ResetComboState("turn-test")
	// Turn 1 start
	r1 := h.applyComboStrategy("round-robin", models, "turn-test", 1, true, weights)
	// Turn 1 mid-turn (e.g. tool execution)
	r1Mid := h.applyComboStrategy("round-robin", models, "turn-test", 1, false, weights)
	if !slices.Equal(r1, r1Mid) {
		t.Errorf("mid-turn should reuse turn head: turn=%v, mid=%v", r1, r1Mid)
	}
}

// ResetComboState clears weighted state cleanly.
func TestComboWeighted_Reset(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b"}
	weights := map[string]int{"p/a": 3, "p/b": 1}

	// Advance
	h.ApplyComboStrategy("round-robin", models, "reset-c", 1, weights)
	h.ApplyComboStrategy("round-robin", models, "reset-c", 1, weights)

	// Reset
	h.ResetComboState("reset-c")

	// Fresh call should start at initial state
	fresh := h.ApplyComboStrategy("round-robin", models, "reset-c", 1, weights)
	if fresh[0] != "p/a" {
		t.Errorf("after reset: expected first model 'p/a', got %q", fresh[0])
	}
}

// Junk and clamped weight values survive safely.
func TestComboWeighted_JunkAndClampedValues(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"p/a", "p/b", "p/c"}

	// Negative weight clamped to 0, huge clamped to 1000
	weights := map[string]int{"p/a": -10, "p/b": 99999, "p/c": 1}
	norm := NormalizeComboWeights(models, weights)
	if norm == nil {
		t.Fatal("expected normalized weights, got nil")
	}
	if norm["p/a"] != 0 {
		t.Errorf("negative weight clamped: got %d, want 0", norm["p/a"])
	}
	if norm["p/b"] != 1000 {
		t.Errorf("huge weight clamped: got %d, want 1000", norm["p/b"])
	}
	if norm["p/c"] != 1 {
		t.Errorf("normal weight: got %d, want 1", norm["p/c"])
	}

	order := h.ApplyComboStrategy("round-robin", models, "junk-test", 1, weights)
	if len(order) != 3 {
		t.Errorf("expected 3 models in order, got %d", len(order))
	}
}

// Capacity adapter models prepended to the combo do not corrupt the weighted cycle.
func TestComboWeighted_CapacityAdapterPrepended(t *testing.T) {
	h := NewChatHandler(nil)
	comboModels := []string{"p/a", "p/b", "p/c"}
	weights := map[string]int{"p/a": 5, "p/b": 3, "p/c": 2}
	withAdapter := []string{"adapter/vision", "p/a", "p/b", "p/c"}

	h.ResetComboState("adapted")
	counts := make(map[string]int)
	for i := 0; i < 20; i++ {
		list := comboModels
		var own []string
		if i%2 == 1 {
			list = withAdapter
			own = comboModels
		}
		rotated := h.applyComboStrategyWithSeats("round-robin", list, "adapted", 1, true, own, weights)
		counts[rotated[0]]++
	}

	expected := map[string]int{"p/a": 10, "p/b": 6, "p/c": 4}
	if !reflect.DeepEqual(counts, expected) {
		t.Errorf("counts with adapter prepended: got %v, want %v", counts, expected)
	}
}

// Tests combo with per-model weights stored in database combo.Models JSON.
func TestComboWeighted_FromDatabaseComboModels(t *testing.T) {
	repo, cleanup := setupComboFlagRepo(t)
	defer cleanup()

	h := NewChatHandler(repo)

	// Create a combo with weights in models JSON: modelA has weight 3, modelB has weight 1
	comboModelsJSON := `[{"model":"fast/modelA","weight":3},{"model":"slow/modelB","weight":1}]`
	err := repo.CreateCombo("c-weighted", "my-weighted-combo", "chat", comboModelsJSON, "round-robin")
	if err != nil {
		t.Fatalf("failed to create combo: %v", err)
	}

	// Verify db.ParseComboModels directly
	directModels, err := db.ParseComboModels(comboModelsJSON)
	if err != nil || len(directModels) != 2 || directModels[0].Weight != 3 || directModels[1].Weight != 1 {
		t.Fatalf("ParseComboModels failed: %v, models=%+v", err, directModels)
	}

	// Verify GetComboModels on repo
	cms, err := repo.GetComboModels("my-weighted-combo")
	if err != nil {
		t.Fatalf("GetComboModels failed: %v", err)
	}
	if len(cms) != 2 || cms[0].Weight != 3 || cms[1].Weight != 1 {
		t.Fatalf("unexpected parsed models: %+v", cms)
	}

	// Run 4 requests through ApplyComboStrategy using the combo name
	models := []string{"fast/modelA", "slow/modelB"}
	counts := tallyFirst(h, models, "my-weighted-combo", 1, 4)
	if counts["fast/modelA"] != 3 || counts["slow/modelB"] != 1 {
		t.Errorf("DB combo 4 requests: got %v, want fast/modelA: 3, slow/modelB: 1", counts)
	}
}

// Tests combo with weights stored in SettingsData.ComboStrategies.
func TestComboWeighted_FromSettings(t *testing.T) {
	repo, cleanup := setupComboFlagRepo(t)
	defer cleanup()

	h := NewChatHandler(repo)

	comboModelsJSON := `["modelX", "modelY"]`
	err := repo.CreateCombo("c-settings", "settings-combo", "chat", comboModelsJSON, "round-robin")
	if err != nil {
		t.Fatalf("failed to create combo: %v", err)
	}

	// Save weights in settings
	settingsJSON := map[string]any{
		"comboStrategies": map[string]any{
			"settings-combo": map[string]any{
				"strategy": "round-robin",
				"weights": map[string]any{
					"modelX": float64(3),
					"modelY": float64(1),
				},
			},
		},
	}
	encoded, _ := json.Marshal(settingsJSON)
	_, err = repo.RawDB().ExecContext(context.Background(), `INSERT OR REPLACE INTO settings (id, data) VALUES (1, ?)`, string(encoded))
	if err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	models := []string{"modelX", "modelY"}
	counts := tallyFirst(h, models, "settings-combo", 1, 4)
	if counts["modelX"] != 3 || counts["modelY"] != 1 {
		t.Errorf("settings combo 4 requests: got %v, want modelX: 3, modelY: 1", counts)
	}
}
