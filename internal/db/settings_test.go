package db

import (
	"fmt"
	"sync"
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

func TestGetSettings_ProviderThinkingModeVocabulary(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	settingsJSON := `{
		"providerThinking": {
			"cline": {"mode": "HIGH"},
			"openai": {"mode": "banana"},
			"gemini": {"mode": "off"}
		}
	}`

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if settings == nil {
		t.Fatal("expected non-nil settings")
	}

	if got := settings.ProviderThinking["cline"].Mode; got != "high" {
		t.Errorf("cline mode = %q, want high (a mode is normalized)", got)
	}
	if got := settings.ProviderThinking["gemini"].Mode; got != "off" {
		t.Errorf("gemini mode = %q, want off", got)
	}
	if _, ok := settings.ProviderThinking["openai"]; ok {
		t.Errorf("a mode outside the vocabulary must be dropped, got %+v", settings.ProviderThinking["openai"])
	}
}

// TestGetSettingsReflectsAWrite pins the cache contract: the parsed row is
// reused only while the row content is unchanged, so a write is visible on
// the next read whatever wrote it.
func TestGetSettingsReflectsAWrite(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"providerThinking":{"deepseek":{"mode":"off"}}}`,
	); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	first, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got := first.ProviderThinking["deepseek"].Mode; got != "off" {
		t.Fatalf("providerThinking = %+v, want the seeded value", first.ProviderThinking)
	}

	// A write through the Repo.
	updated := map[string]any{"providerThinking": map[string]any{"deepseek": map[string]any{"mode": "high"}}}
	if err := repo.UpdateSettingsRaw(updated); err != nil {
		t.Fatalf("UpdateSettingsRaw: %v", err)
	}
	second, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after write: %v", err)
	}
	if got := second.ProviderThinking["deepseek"].Mode; got != "high" {
		t.Errorf("providerThinking = %+v, want the written value", second.ProviderThinking)
	}

	// A write that bypasses the Repo.
	if _, err := database.Exec(`UPDATE settings SET data = ? WHERE id = 1`,
		`{"providerThinking":{"deepseek":{"mode":"auto"}}}`); err != nil {
		t.Fatalf("failed to update settings: %v", err)
	}
	third, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after direct write: %v", err)
	}
	if got := third.ProviderThinking["deepseek"].Mode; got != "auto" {
		t.Errorf("providerThinking = %+v, want the directly written value", third.ProviderThinking)
	}
}

// TestGetSettingsCloneDoesNotShareMaps pins the cache contract: the parsed
// row the caller receives is its own copy, so a caller that writes into a map
// it was handed cannot change the answer the next reader sees.
func TestGetSettingsCloneDoesNotShareMaps(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"providerThinking":{"deepseek":{"mode":"off"}}}`,
	); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	repo := NewRepo(database)
	first, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	first.ProviderThinking["deepseek"] = ProviderThinking{Mode: "on"}

	second, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got := second.ProviderThinking["deepseek"].Mode; got != "off" {
		t.Errorf("providerThinking = %q, want the stored value: the caller mutated the cached row", got)
	}
}

func TestGetSettingsCloneDoesNotShareProviderOverrideHeaders(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"providerOverrides":{"deepseek":{"headers":{"X-Test":"one"}}}}`,
	); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	repo := NewRepo(database)
	first, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	first.ProviderOverrides["deepseek"].Headers["X-Test"] = "poisoned"

	second, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got := second.ProviderOverrides["deepseek"].Headers["X-Test"]; got != "one" {
		t.Errorf("providerOverrides headers = %q, want the stored value: the caller mutated the cached row", got)
	}
}

// TestUpdateSettingsRawRefusesAnUnreadableRow pins the write path: a row
// that cannot be read is not replaced. Writing back only the update would
// drop every other setting the operator had set, the dashboard password
// among them.
func TestUpdateSettingsRawRefusesAnUnreadableRow(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}
	broken := `{"password":"hash",`
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		broken,
	); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	repo := NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{"rtkEnabled": true}); err == nil {
		t.Fatal("a write against an unreadable row must be refused")
	}
	var stored string
	if err := database.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read settings row: %v", err)
	}
	if stored != broken {
		t.Errorf("the stored row = %q, want it untouched", stored)
	}
}

// TestConcurrentSettingsWritesDoNotLoseKeys pins the write lock: the
// settings row is one JSON blob, and two writers that interleave lose one
// another's keys.
func TestConcurrentSettingsWritesDoNotLoseKeys(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	repo := NewRepo(database)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := repo.UpdateSettingsRaw(map[string]any{fmt.Sprintf("key%d", i): i}); err != nil {
				t.Errorf("UpdateSettingsRaw: %v", err)
			}
		}(i)
	}
	wg.Wait()

	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, ok := raw[fmt.Sprintf("key%d", i)]; !ok {
			t.Errorf("key%d was lost by a concurrent write", i)
		}
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

func TestGetSettings_ObservabilityAndRetention(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	settingsJSON := `{
		"enableObservability": true,
		"observabilityMode": "errors_only",
		"retentionDays": 14,
		"maxRequestRecords": 2500
	}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if !s.EnableObservability {
		t.Errorf("EnableObservability = false, want true")
	}
	if s.ObservabilityMode != "errors_only" {
		t.Errorf("ObservabilityMode = %q, want 'errors_only'", s.ObservabilityMode)
	}
	if s.RetentionDays != 14 {
		t.Errorf("RetentionDays = %d, want 14", s.RetentionDays)
	}
	if s.MaxRequestRecords != 2500 {
		t.Errorf("MaxRequestRecords = %d, want 2500", s.MaxRequestRecords)
	}
}
