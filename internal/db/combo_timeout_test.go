package db

import "testing"

func TestComboResponseTimeoutSettings(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{
		"enabled":    map[string]any{"firstTokenTimeoutMs": 1500, "strategy": "smart", "judgeModel": "judge", "smartPolicy": "strict"},
		"disabled":   map[string]any{"firstTokenTimeoutMs": 0},
		"negative":   map[string]any{"firstTokenTimeoutMs": -1},
		"fractional": map[string]any{"firstTokenTimeoutMs": 1.5},
		"overflow":   map[string]any{"firstTokenTimeoutMs": 9223372036855},
		"absent":     map[string]any{"strategy": "fallback"},
	}}); err != nil {
		t.Fatal(err)
	}
	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.ComboStrategies["enabled"].FirstTokenTimeoutMs != 1500 {
		t.Fatal("timeout not parsed")
	}
	for _, name := range []string{"disabled", "negative", "fractional", "overflow", "absent"} {
		if settings.ComboStrategies[name].FirstTokenTimeoutMs != 0 {
			t.Errorf("invalid timeout enabled for %s", name)
		}
	}
	if err := repo.SetComboStrategy("enabled", ComboStrategy{Strategy: "fallback"}); err != nil {
		t.Fatal(err)
	}
	settings, err = repo.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	entry := settings.ComboStrategies["enabled"]
	if entry.FirstTokenTimeoutMs != 1500 || entry.JudgeModel != "judge" || entry.SmartPolicy != "strict" {
		t.Fatalf("strategy update clobbered combo fields: %+v", entry)
	}
	if settings.ComboStrategies["absent"].FirstTokenTimeoutMs != 0 {
		t.Fatal("other combo changed")
	}
}

func TestComboTimeoutGraceSettings(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{
		"custom":     map[string]any{"firstTokenTimeoutMs": 1000, "firstTokenGraceMs": 90000},
		"zero":       map[string]any{"firstTokenTimeoutMs": 1000, "firstTokenGraceMs": 0},
		"absent":     map[string]any{"firstTokenTimeoutMs": 1000},
		"negative":   map[string]any{"firstTokenTimeoutMs": 1000, "firstTokenGraceMs": -5},
		"fractional": map[string]any{"firstTokenTimeoutMs": 1000, "firstTokenGraceMs": 1.5},
	}}); err != nil {
		t.Fatal(err)
	}
	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"custom": 90000, "zero": 0, "absent": DefaultFirstTokenGraceMs, "negative": DefaultFirstTokenGraceMs, "fractional": DefaultFirstTokenGraceMs}
	for name, ms := range want {
		if got := settings.ComboStrategies[name].FirstTokenGrace(); got != ms {
			t.Errorf("%s grace = %d, want %d", name, got, ms)
		}
	}
	if DefaultFirstTokenGraceMs != 300000 {
		t.Fatal("default grace must be five minutes")
	}
}
