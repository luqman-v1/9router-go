package db

import "testing"

func TestComboMemberAttemptCapFor(t *testing.T) {
	off := 0
	five := 5
	huge := 99
	negative := -4

	tests := []struct {
		name     string
		settings *SettingsData
		provider string
		want     int
	}{
		{name: "nil settings is off", settings: nil, provider: "groq", want: 0},
		{name: "unset global is off", settings: &SettingsData{}, provider: "groq", want: 0},
		{name: "global applies", settings: &SettingsData{ComboMemberAttemptCap: 4}, provider: "groq", want: 4},
		{
			name:     "provider override wins over global",
			settings: &SettingsData{ComboMemberAttemptCap: 4, ProviderOverrides: map[string]ProviderOverrides{"groq": {ComboMemberAttemptCap: &five}}},
			provider: "groq",
			want:     5,
		},
		{
			name:     "provider override zero disables even when a global cap exists",
			settings: &SettingsData{ComboMemberAttemptCap: 4, ProviderOverrides: map[string]ProviderOverrides{"groq": {ComboMemberAttemptCap: &off}}},
			provider: "groq",
			want:     0,
		},
		{
			name:     "override for another provider does not leak",
			settings: &SettingsData{ComboMemberAttemptCap: 4, ProviderOverrides: map[string]ProviderOverrides{"deepseek": {ComboMemberAttemptCap: &five}}},
			provider: "groq",
			want:     4,
		},
		{
			name:     "override without a cap field inherits the global",
			settings: &SettingsData{ComboMemberAttemptCap: 4, ProviderOverrides: map[string]ProviderOverrides{"groq": {SeatBudgetMs: 40}}},
			provider: "groq",
			want:     4,
		},
		{
			name:     "above the ceiling clamps",
			settings: &SettingsData{ComboMemberAttemptCap: 11},
			provider: "groq",
			want:     MaxComboMemberAttemptCap,
		},
		{
			name:     "override above the ceiling clamps",
			settings: &SettingsData{ProviderOverrides: map[string]ProviderOverrides{"groq": {ComboMemberAttemptCap: &huge}}},
			provider: "groq",
			want:     MaxComboMemberAttemptCap,
		},
		{
			name:     "negative override disables instead of sending nothing",
			settings: &SettingsData{ProviderOverrides: map[string]ProviderOverrides{"groq": {ComboMemberAttemptCap: &negative}}},
			provider: "groq",
			want:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComboMemberAttemptCapFor(tt.settings, tt.provider); got != tt.want {
				t.Errorf("ComboMemberAttemptCapFor() = %d, want %d", got, tt.want)
			}
		})
	}
}

// The stored blob is the only path an operator's value takes to the loops, so
// the parse is part of the contract: a value that never parses is a setting the
// dashboard shows and the gateway ignores.
func TestParseSettingsReadsComboMemberAttemptCap(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "global", raw: `{"comboMemberAttemptCap":3}`, want: 3},
		{name: "absent is off", raw: `{}`, want: 0},
		{name: "negative is off", raw: `{"comboMemberAttemptCap":-2}`, want: 0},
		{
			name: "per-provider override",
			raw:  `{"comboMemberAttemptCap":3,"providerOverrides":{"groq":{"comboMemberAttemptCap":0}}}`,
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ok := parseSettings(tt.raw)
			if !ok {
				t.Fatal("parseSettings rejected a readable blob")
			}
			if s.ComboMemberAttemptCap != tt.want {
				t.Errorf("ComboMemberAttemptCap = %d, want %d", s.ComboMemberAttemptCap, tt.want)
			}
		})
	}

	s, ok := parseSettings(`{"providerOverrides":{"groq":{"comboMemberAttemptCap":0}}}`)
	if !ok {
		t.Fatal("parseSettings rejected a readable blob")
	}
	got := s.ProviderOverrides["groq"].ComboMemberAttemptCap
	if got == nil {
		t.Fatal("explicit zero override parsed as absent")
	}
	if *got != 0 {
		t.Fatalf("override = %d, want 0", *got)
	}
}
