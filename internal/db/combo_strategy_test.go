package db

import (
	"testing"

	"9router/proxy/internal/models"
)

func TestNormalizeComboStrategy(t *testing.T) {
	cases := map[string]string{
		"round-robin":           "round-robin",
		"round_robin":           "round-robin",
		"roundrobin":            "round-robin",
		"sticky":                "round-robin",
		"load_balance":          "round-robin",
		"smart":                 "round-robin",
		"weighted":              "round-robin",
		"weighted_distribution": "round-robin",
		"race":                  "round-robin",
		"parallel":              "round-robin",
		"fallback":              "fallback",
		"failover":              "fallback",
		"first-model":           "fallback",
		"capacity":              "fallback",
		"fusion":                "fusion",
		"ensemble":              "fusion",
		"":                      "round-robin",
		"bogus":                 "round-robin",
		" Round-Robin ":         "round-robin",
	}
	for in, want := range cases {
		if got := NormalizeComboStrategy(in); got != want {
			t.Errorf("NormalizeComboStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveComboStrategy(t *testing.T) {
	t.Run("no settings resolves to round-robin", func(t *testing.T) {
		combo := &models.Combo{Name: "c1"}
		resolveComboStrategy(combo, nil)
		if combo.Strategy != "round-robin" {
			t.Errorf("strategy = %q, want round-robin", combo.Strategy)
		}
	})

	t.Run("legacy per-combo values fold to canonical", func(t *testing.T) {
		combo := &models.Combo{Name: "c1"}
		s := &SettingsData{ComboStrategies: map[string]ComboStrategy{
			"c1": {Strategy: "sticky"},
		}}
		resolveComboStrategy(combo, s)
		if combo.Strategy != "round-robin" {
			t.Errorf("strategy = %q, want round-robin (sticky folds)", combo.Strategy)
		}
	})

	t.Run("global first-model resolves to fallback", func(t *testing.T) {
		combo := &models.Combo{Name: "c1"}
		s := &SettingsData{ComboStrategy: "first-model"}
		resolveComboStrategy(combo, s)
		if combo.Strategy != "fallback" {
			t.Errorf("strategy = %q, want fallback", combo.Strategy)
		}
	})

	t.Run("per-combo fusion wins over global", func(t *testing.T) {
		combo := &models.Combo{Name: "c1"}
		s := &SettingsData{
			ComboStrategy: "round-robin",
			ComboStrategies: map[string]ComboStrategy{
				"c1": {Strategy: "ensemble"},
			},
		}
		resolveComboStrategy(combo, s)
		if combo.Strategy != "fusion" {
			t.Errorf("strategy = %q, want fusion (ensemble folds)", combo.Strategy)
		}
	})
}
