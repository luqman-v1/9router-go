package chat

import "testing"

func TestGenerateMimoFingerprint(t *testing.T) {
	fp1 := generateMimoFingerprint()
	if len(fp1) != 64 {
		t.Errorf("expected 64 hex chars, got %d: %s", len(fp1), fp1)
	}
	// Verify hex
	for _, c := range fp1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex character: %c", c)
		}
	}
	// Deterministic within same process
	fp2 := generateMimoFingerprint()
	if fp1 != fp2 {
		t.Errorf("expected deterministic, got %q vs %q", fp1, fp2)
	}
}
