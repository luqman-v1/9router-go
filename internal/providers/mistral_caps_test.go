package providers

import "testing"

func TestMistralLarge4Caps(t *testing.T) {
	InvalidateCapabilitiesCache()
	if got := GetCapabilitiesForModel("mistral", "mistral-large-latest").ContextWindow; got != 1048576 {
		t.Errorf("mistral-large-latest ContextWindow = %d, want 1048576", got)
	}
	InvalidateCapabilitiesCache()
	if got := GetCapabilitiesForModel("mistral", "mistral-large-2411").ContextWindow; got != 256000 {
		t.Errorf("mistral-large-2411 ContextWindow = %d, want 256000", got)
	}
	InvalidateCapabilitiesCache()
	if got := GetCapabilitiesForModel("other-provider", "mistral-large-latest").ContextWindow; got != 256000 {
		t.Errorf("other-provider/mistral-large-latest ContextWindow = %d, want 256000", got)
	}
}
