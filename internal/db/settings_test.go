package db

import (
	"testing"
)

func TestGetSettings_StrictModelAssignment(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	settingsJSON := `{
		"rtkEnabled": true,
		"providerStrategies": {
			"openai": {
				"proxyPoolId": "pool-1",
				"rotateStrategy": "round-robin",
				"strictModelAssignment": true
			},
			"anthropic": {
				"proxyPoolId": "pool-2",
				"rotateStrategy": "none",
				"strictModelAssignment": false
			},
			"gemini": {
				"proxyPoolId": "pool-3"
			}
		}
	}`

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	_, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON)
	if err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}

	if settings == nil {
		t.Fatal("expected non-nil settings")
	}

	stratOpenAI, ok := settings.ProviderStrategies["openai"]
	if !ok {
		t.Fatal("expected provider strategy for openai")
	}
	if !stratOpenAI.StrictModelAssignment {
		t.Errorf("expected StrictModelAssignment true for openai, got false")
	}
	if stratOpenAI.ProxyPoolID != "pool-1" {
		t.Errorf("expected ProxyPoolID pool-1, got %s", stratOpenAI.ProxyPoolID)
	}

	stratAnthropic, ok := settings.ProviderStrategies["anthropic"]
	if !ok {
		t.Fatal("expected provider strategy for anthropic")
	}
	if stratAnthropic.StrictModelAssignment {
		t.Errorf("expected StrictModelAssignment false for anthropic, got true")
	}

	stratGemini, ok := settings.ProviderStrategies["gemini"]
	if !ok {
		t.Fatal("expected provider strategy for gemini")
	}
	if stratGemini.StrictModelAssignment {
		t.Errorf("expected StrictModelAssignment false (default) for gemini, got true")
	}
}

func TestGetSettings_DashboardParityKeys(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	// JSON written by Next.js / Svelte 5 dashboard
	settingsJSON := `{
		"fallbackStrategy": "round-robin",
		"stickyRoundRobinLimit": 3,
		"comboStrategy": "round-robin",
		"comboStickyRoundRobinLimit": 2,
		"comboStrategies": {
			"smart-combo": {
				"fallbackStrategy": "round-robin",
				"stickyRoundRobinLimit": 1,
				"judgeModel": "openai/gpt-4o"
			},
			"fusion-combo": {
				"strategy": "fusion",
				"judgeModel": "anthropic/claude-3-opus"
			}
		},
		"providerStrategies": {
			"deepseek": {
				"fallbackStrategy": "round-robin",
				"stickyRoundRobinLimit": 5,
				"proxyPoolId": "pool-ds"
			},
			"openai": {
				"rotateStrategy": "round-robin",
				"stickyLimit": 3
			}
		}
	}`

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	_, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON)
	if err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}

	// Check global strategies
	if s.FallbackStrategy != "round-robin" || s.StickyRoundRobinLimit != 3 {
		t.Errorf("expected global provider fallbackStrategy 'round-robin' limit 3, got %q limit %d", s.FallbackStrategy, s.StickyRoundRobinLimit)
	}
	if s.ComboStrategy != "round-robin" || s.ComboStickyRoundRobinLimit != 2 {
		t.Errorf("expected global comboStrategy 'round-robin' limit 2, got %q limit %d", s.ComboStrategy, s.ComboStickyRoundRobinLimit)
	}

	// Check comboStrategies
	smartCombo, ok := s.ComboStrategies["smart-combo"]
	if !ok {
		t.Fatal("expected comboStrategy for smart-combo")
	}
	if smartCombo.Strategy != "round-robin" || smartCombo.StickyLimit != 1 || smartCombo.JudgeModel != "openai/gpt-4o" {
		t.Errorf("unexpected smart-combo: %+v", smartCombo)
	}

	fusionCombo, ok := s.ComboStrategies["fusion-combo"]
	if !ok {
		t.Fatal("expected comboStrategy for fusion-combo")
	}
	if fusionCombo.Strategy != "fusion" || fusionCombo.JudgeModel != "anthropic/claude-3-opus" {
		t.Errorf("unexpected fusion-combo: %+v", fusionCombo)
	}

	// Check providerStrategies
	dsStrat, ok := s.ProviderStrategies["deepseek"]
	if !ok {
		t.Fatal("expected providerStrategy for deepseek")
	}
	if dsStrat.RotateStrategy != "round-robin" || dsStrat.StickyLimit != 5 || dsStrat.ProxyPoolID != "pool-ds" {
		t.Errorf("unexpected deepseek strategy: %+v", dsStrat)
	}

	oaiStrat, ok := s.ProviderStrategies["openai"]
	if !ok {
		t.Fatal("expected providerStrategy for openai")
	}
	if oaiStrat.RotateStrategy != "round-robin" || oaiStrat.StickyLimit != 3 {
		t.Errorf("unexpected openai strategy: %+v", oaiStrat)
	}
}

func TestSetComboAndProviderStrategy_PreservesExistingSettings(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	// Seed initial settings with custom fields (e.g. password, rtkEnabled)
	if err := repo.UpdateSettingsRaw(map[string]any{
		"password":   "secret-hashed",
		"rtkEnabled": true,
	}); err != nil {
		t.Fatalf("UpdateSettingsRaw failed: %v", err)
	}

	// Set combo strategy
	if err := repo.SetComboStrategy("my-combo", ComboStrategy{
		Strategy:    "round-robin",
		StickyLimit: 3,
		JudgeModel:  "openai/gpt-4o",
	}); err != nil {
		t.Fatalf("SetComboStrategy failed: %v", err)
	}

	// Set provider strategy
	if err := repo.SetProviderStrategy("anthropic", ProviderStrategy{
		RotateStrategy: "round-robin",
		StickyLimit:    2,
		ProxyPoolID:    "pool-a",
	}); err != nil {
		t.Fatalf("SetProviderStrategy failed: %v", err)
	}

	// Verify raw settings still preserve password and rtkEnabled
	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw failed: %v", err)
	}
	if raw["password"] != "secret-hashed" || raw["rtkEnabled"] != true {
		t.Errorf("settings clobbered: %+v", raw)
	}

	// Verify typed settings reflect the updates
	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if s.ComboStrategies["my-combo"].Strategy != "round-robin" || s.ComboStrategies["my-combo"].StickyLimit != 3 {
		t.Errorf("unexpected combo strategy: %+v", s.ComboStrategies["my-combo"])
	}
	if s.ProviderStrategies["anthropic"].RotateStrategy != "round-robin" || s.ProviderStrategies["anthropic"].StickyLimit != 2 {
		t.Errorf("unexpected provider strategy: %+v", s.ProviderStrategies["anthropic"])
	}
}

func TestSettings_ADHD(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	// DefaultSettings verification
	defaults := DefaultSettings()
	if defaults.ADHDEnabled {
		t.Errorf("expected default ADHDEnabled=false, got true")
	}
	if defaults.ADHDLevel != "full" {
		t.Errorf("expected default ADHDLevel='full', got %q", defaults.ADHDLevel)
	}

	// UpdateSettingsRaw with adhdEnabled and adhdLevel
	if err := repo.UpdateSettingsRaw(map[string]any{
		"adhdEnabled": true,
		"adhdLevel":   "lite",
	}); err != nil {
		t.Fatalf("UpdateSettingsRaw failed: %v", err)
	}

	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if !settings.ADHDEnabled {
		t.Errorf("expected ADHDEnabled=true, got false")
	}
	if settings.ADHDLevel != "lite" {
		t.Errorf("expected ADHDLevel='lite', got %q", settings.ADHDLevel)
	}
}
