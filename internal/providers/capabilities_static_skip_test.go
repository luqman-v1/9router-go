package providers

import "testing"

// TestSetCustomModelCapsRefusesStaticCatalogIDs pins the publication invariant:
// a model id the provider's static registry catalog already lists cannot have
// its caps replaced by a custom row, whichever publisher offers it. The
// custom-caps map is process-wide and every capability read consults it before
// the registry, so a publication accepted here changes what the gateway
// advertises for a known model until the process exits.
func TestSetCustomModelCapsRefusesStaticCatalogIDs(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		model      string
		wantStored bool
	}{
		{
			name:       "static catalog id is refused",
			provider:   "opencode-zen",
			model:      "mimo-v2.6-flash-free",
			wantStored: false,
		},
		{
			name:       "static catalog id through the provider alias is refused",
			provider:   "ocz",
			model:      "mimo-v2.6-flash-free",
			wantStored: false,
		},
		{
			name:       "provider-qualified static catalog id is refused",
			provider:   "opencode-zen",
			model:      "opencode-zen/mimo-v2.6-flash-free",
			wantStored: false,
		},
		{
			name:       "id the catalog does not own is stored",
			provider:   "opencode-zen",
			model:      "zen-custom-model",
			wantStored: true,
		},
		{
			name:       "provider node prefix has no static catalog to collide with",
			provider:   "node-nara",
			model:      "mimo-v2.6-flash-free",
			wantStored: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ClearCustomModelCaps()
			t.Cleanup(ClearCustomModelCaps)

			SetCustomModelCaps(tt.provider, tt.model, Capabilities{ContextWindow: 4096, MaxOutput: 1024})

			_, stored := GetCustomModelCaps(tt.provider, tt.model)
			if stored != tt.wantStored {
				t.Errorf("GetCustomModelCaps(%q, %q) stored = %v, want %v",
					tt.provider, tt.model, stored, tt.wantStored)
			}
		})
	}
}

// TestStaticCatalogIDKeepsItsLimitsAfterAPublishAttempt pins the observable half
// of the same rule: a refused publication leaves the read path answering the
// catalog's numbers, not the custom row's.
func TestStaticCatalogIDKeepsItsLimitsAfterAPublishAttempt(t *testing.T) {
	ClearCustomModelCaps()
	t.Cleanup(ClearCustomModelCaps)

	const (
		provider = "opencode-zen"
		model    = "mimo-v2.6-flash-free"
	)
	before := GetCapabilitiesDetailForModel(provider, model)
	if before.ContextWindow == 4096 {
		t.Fatalf("fixture is not a collision: the catalog already answers 4096 for %s", model)
	}

	SetCustomModelCaps(provider, model, Capabilities{ContextWindow: 4096, MaxOutput: 1024})

	after := GetCapabilitiesDetailForModel(provider, model)
	if after.ContextWindow != before.ContextWindow || after.MaxOutput != before.MaxOutput {
		t.Errorf("caps after a refused publication = (%d, %d), want the catalog's (%d, %d)",
			after.ContextWindow, after.MaxOutput, before.ContextWindow, before.MaxOutput)
	}
}
