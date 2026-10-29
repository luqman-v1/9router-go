package thinking

import "testing"

func TestNormalizeProviderThinkingMode(t *testing.T) {
	tests := []struct {
		mode   string
		want   string
		wantOK bool
	}{
		{"auto", "auto", true},
		{"on", "on", true},
		{"off", "off", true},
		{"minimal", "minimal", true},
		{"low", "low", true},
		{"medium", "medium", true},
		{"high", "high", true},
		{"xhigh", "xhigh", true},
		{"max", "max", true},
		{"HIGH", "high", true},
		{"  off  ", "off", true},
		{"none", "none", true},
		{"banana", "", false},
		{"ultra", "", false},
		{"", "", false},
		{"   ", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got, ok := NormalizeProviderThinkingMode(tt.mode)
			if ok != tt.wantOK {
				t.Fatalf("NormalizeProviderThinkingMode(%q) ok = %v, want %v", tt.mode, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("NormalizeProviderThinkingMode(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestProviderThinkingModesCoverTheLevelVocabulary(t *testing.T) {
	for _, level := range EffortLevels {
		if _, ok := NormalizeProviderThinkingMode(level); !ok {
			t.Errorf("level %q must be a valid providerThinking mode", level)
		}
	}
}
