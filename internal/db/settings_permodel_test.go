package db

import "testing"

// The per-model cap block is stored as raw JSON on the settings row, so it has
// to survive the same parse path every other setting does.
func TestParseSettingsPerModelRateLimit(t *testing.T) {
	raw := `{
		"perModelRateLimit": {
			"enabled": true,
			"limit": 30,
			"windowMs": 60000,
			"overrides": {
				"openai-compatible-a": {"limit": 10},
				"openai-compatible-a/gpt-4o-mini": {"limit": 2, "windowMs": 5000}
			}
		}
	}`

	s, ok := parseSettings(raw)
	if !ok {
		t.Fatal("settings did not parse")
	}
	cfg := s.PerModelRateLimit
	if cfg == nil {
		t.Fatal("perModelRateLimit was dropped")
	}
	if cfg.Enabled == nil || !*cfg.Enabled {
		t.Errorf("enabled = %v, want true", cfg.Enabled)
	}
	if cfg.Limit != 30 || cfg.WindowMs != 60000 {
		t.Errorf("block = %+v, want limit 30 / window 60000", cfg)
	}
	if got := cfg.Overrides["openai-compatible-a"]; got == nil || got.Limit != 10 {
		t.Errorf("provider override = %+v, want limit 10", got)
	}
	if got := cfg.Overrides["openai-compatible-a/gpt-4o-mini"]; got == nil || got.Limit != 2 || got.WindowMs != 5000 {
		t.Errorf("model override = %+v, want limit 2 / window 5000", got)
	}
}

func TestParseSettingsWithoutPerModelRateLimit(t *testing.T) {
	s, ok := parseSettings(`{}`)
	if !ok {
		t.Fatal("settings did not parse")
	}
	if s.PerModelRateLimit != nil {
		t.Error("an absent block must stay nil, so the resolver applies its own default")
	}
}
