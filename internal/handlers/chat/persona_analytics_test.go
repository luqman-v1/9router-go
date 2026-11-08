package chat

import (
	"context"
	"testing"
)

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

// TestApplyTokenSaversPersonaStackCountsEachPromptOnce pins the call site, not
// just the helper: with three personas switched on, the measured
// OriginalInputTokens must be the growth the body actually underwent.
//
// The bug this guards against froze the baseline at the pre-persona length, so
// the second persona measured its own prompt *plus* the first persona's bytes.
// The totals are analytics only, but a stacked-persona row then reported an
// input size the client was never sent.
func TestApplyTokenSaversPersonaStackCountsEachPromptOnce(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	stacked, stackedCleanup := setupHandlerForForward(t)
	defer stackedCleanup()
	stacked.TokenSaver.SetCaveman(true)
	stacked.TokenSaver.SetPonytail(true)
	stacked.TokenSaver.SetADHD(true)
	gotStacked, origStacked, savedStacked, _ := stacked.applyTokenSavers(context.Background(), body, false)
	if string(gotStacked) == string(body) {
		t.Fatalf("expected the personas to add prompts to the body")
	}

	// The body grew by the three prompts, so the booked input is that growth on
	// the same 4-bytes-per-token scale the caller measures with. Each injection
	// truncates its own delta, so the total may sit one token under the whole
	// body's growth divided by four.
	wantOrig := (len(gotStacked) - len(body)) / 4
	if origStacked != wantOrig && origStacked != wantOrig-1 {
		t.Errorf("origTokens = %d, want %d (the body grew %d bytes over three personas)",
			origStacked, wantOrig, len(gotStacked)-len(body))
	}
	if savedStacked != 0 {
		t.Errorf("savedTokens = %d, want 0 (a persona prompt compresses nothing)", savedStacked)
	}

	// One persona must book only its own prompt. With the baseline frozen, the
	// stacked total overshoots this and the stacked row over-reports the input.
	single, singleCleanup := setupHandlerForForward(t)
	defer singleCleanup()
	single.TokenSaver.SetCaveman(true)
	gotSingle, origSingle, _, _ := single.applyTokenSavers(context.Background(), body, false)
	if wantSingle := (len(gotSingle) - len(body)) / 4; origSingle != wantSingle && origSingle != wantSingle-1 {
		t.Errorf("single persona origTokens = %d, want %d", origSingle, wantSingle)
	}
	if origSingle >= origStacked {
		t.Errorf("single persona origTokens = %d did not come in under the three-persona total (%d); the baseline is still frozen",
			origSingle, origStacked)
	}
}
