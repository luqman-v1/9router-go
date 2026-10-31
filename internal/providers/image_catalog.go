package providers

import "strings"

// Image-generation model detection for the /v1/models/image surface.
//
// The existing name-based vision tables in vision_patterns.go answer "does this
// model TAKE image input", which is the opposite question. This file answers
// "does this model PRODUCE images", and is kept separate so the two cannot be
// conflated: a vision model (qwen3-vl-plus) takes images and returns text, an
// image model (dall-e-3) returns images.
//
// Markers rather than an explicit list, so a new image model from any provider
// is picked up without a second edit.
var imageModelMarkers = []string{
	"image", "imagen", "dall-e", "flux", "sd3", "stable-diffusion", "stable-image",
	"sdwebui", "sd35", "gen4", "seedance", "recraft", "venice-sd", "nanobanana",
}

// IsImageModelID reports whether a model id generates images rather than chat
// text.
func IsImageModelID(modelID string) bool {
	m := strings.ToLower(strings.TrimSpace(modelID))
	if m == "" {
		return false
	}
	for _, marker := range imageModelMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// DefaultImageModel returns the model id to advertise for a provider whose
// catalogue carries no recognisable image entry, so /v1/models/image never
// hands out an id that resolution rejects.
//
// Returns "" when the provider has no catalogue at all, which lets the caller
// fall back to its own error rather than advertising a model that cannot
// resolve.
func DefaultImageModel(provider string) string {
	if models := ProviderModels[provider]; len(models) > 0 {
		for _, m := range models {
			if IsImageModelID(m) {
				return m
			}
		}
		return models[0]
	}
	return ""
}

// FilterImageModels returns the subset of ids that generate images.
func FilterImageModels(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if IsImageModelID(id) {
			out = append(out, id)
		}
	}
	return out
}
