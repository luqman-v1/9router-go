package providers

import (
	"slices"
	"testing"
)

func TestIsImageModelID(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		want    bool
	}{
		{"dall-e", "dall-e-3", true},
		{"gpt image", "gpt-image", true},
		{"imagen", "google/imagen-3.0-generate-002", true},
		{"flux path", "fal-ai/flux-pro/v1.1", true},
		{"gemini image preview", "gemini-3.1-flash-image-preview", true},
		{"seedance", "bytedance-seedance-2.0", true},
		{"seedream", "deeprelay/seedream-4.0", true},
		{"recraft", "fal-ai/recraft-v3", true},
		{"stable diffusion", "fal-ai/stable-diffusion-v35-large", true},
		{"case insensitive", "DALL-E-2", true},
		// Non-image lanes that share vocabulary with the markers.
		{"tts is not image", "gpt-4o-mini-tts", false},
		{"embedding is not image", "gemini-embedding-001", false},
		{"video is not image", "google/veo-3.1", false},
		{"chat model", "claude-sonnet-4-6", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsImageModelID(tt.modelID); got != tt.want {
				t.Errorf("IsImageModelID(%q) = %v, want %v", tt.modelID, got, tt.want)
			}
		})
	}
}

// TestImageModelsForProvider_MatchesShippedCatalog pins the property the
// /v1/models/image listing depends on: for a provider that declares an
// ImageURL, resolution of each emitted "{provider}/{model}" must succeed at
// the provider level, which is what the generation endpoint does first.
func TestImageModelsForProvider_MatchesShippedCatalog(t *testing.T) {
	tests := []struct {
		provider string
		wantID   string // an id the provider must include, "" to skip
	}{
		{provider: "fal-ai", wantID: "fal-ai/flux-pro/v1.1"},
		{provider: "openai", wantID: "dall-e-3"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			models := ImageModelsForProvider(tt.provider)
			if len(models) == 0 {
				t.Fatalf("ImageModelsForProvider(%q) returned no models", tt.provider)
			}
			for _, m := range models {
				if !IsImageModelID(m) {
					t.Errorf("emitted non-image model %q for %q", m, tt.provider)
				}
			}
			if tt.wantID != "" && !slices.Contains(models, tt.wantID) {
				t.Errorf("ImageModelsForProvider(%q) = %v, want it to contain %q",
					tt.provider, models, tt.wantID)
			}
		})
	}
}

// A provider with an ImageURL but no recognisable catalog image entry still
// needs an addressable id, and it must never be the bare alias.
func TestDefaultImageModel_NeverReturnsBareAlias(t *testing.T) {
	for _, providerID := range []string{"xai", "openrouter", "stability-ai"} {
		got := DefaultImageModel(providerID)
		if got == "" {
			t.Errorf("DefaultImageModel(%q) returned empty", providerID)
			continue
		}
		if got == providerID {
			t.Errorf("DefaultImageModel(%q) returned the bare alias, which the "+
				"generation endpoint rejects", providerID)
		}
	}
}

// An unknown provider has no catalog at all: the fallback is explicit so the
// listing never emits an empty model segment.
func TestDefaultImageModel_UnknownProviderFallsBack(t *testing.T) {
	if got := DefaultImageModel("no-such-provider-xyz"); got != "default" {
		t.Errorf("DefaultImageModel(unknown) = %q, want %q", got, "default")
	}
	if got := ImageModelsForProvider("no-such-provider-xyz"); got != nil {
		t.Errorf("ImageModelsForProvider(unknown) = %v, want nil", got)
	}
}
