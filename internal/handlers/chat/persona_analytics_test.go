package chat

import "testing"

// TestRecordPersonaInjectionCoversTokenTotals pins how a persona prompt is
// booked into the analytics totals.
//
// A style prompt compresses nothing — it adds a system prompt the provider is
// about to be billed for. The request still went through a saver, so the
// usage page has to show it; the totals therefore move by the prompt's own
// size, OriginalInputTokens up and SavedTokens down.
func TestRecordPersonaInjectionCoversTokenTotals(t *testing.T) {
	tests := []struct {
		name        string
		origTokens  int
		savedTokens int
		beforeLen   int
		afterLen    int
		wantOrig    int
		wantSaved   int
	}{
		{
			name:        "injection adds tokens and costs the saving",
			origTokens:  100,
			savedTokens: 40,
			beforeLen:   400,
			afterLen:    800, // +400 bytes = +100 tokens
			wantOrig:    200,
			wantSaved:   0, // 40 - 100 clamps at zero
		},
		{
			name:        "a body that only grew is booked as growth",
			origTokens:  0,
			savedTokens: 0,
			beforeLen:   0,
			afterLen:    1200, // +300 tokens
			wantOrig:    300,
			wantSaved:   0,
		},
		{
			name:        "a smaller body is not a persona injection",
			origTokens:  100,
			savedTokens: 40,
			beforeLen:   800,
			afterLen:    400,
			wantOrig:    100,
			wantSaved:   40,
		},
		{
			name:        "a sub-token change is not booked",
			origTokens:  100,
			savedTokens: 40,
			beforeLen:   400,
			afterLen:    403, // +3 bytes, under one token
			wantOrig:    100,
			wantSaved:   40,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig, saved := tt.origTokens, tt.savedTokens
			recordPersonaInjection(&orig, &saved, tt.beforeLen, tt.afterLen)
			if orig != tt.wantOrig {
				t.Errorf("origTokens = %d, want %d", orig, tt.wantOrig)
			}
			if saved != tt.wantSaved {
				t.Errorf("savedTokens = %d, want %d", saved, tt.wantSaved)
			}
			if saved < 0 {
				t.Errorf("savedTokens = %d, must never go negative", saved)
			}
		})
	}
}
