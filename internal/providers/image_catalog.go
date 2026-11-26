package providers

import "strings"

// Image model discovery for GET /v1/models/image.
//
// The listing used to answer one entry per image-capable PROVIDER, with the
// provider id as the model id ("xai", "fal-ai"). That id is not addressable:
// POST /v1/images/generations resolves "{provider}/{model}" and answers 400
// "could not resolve model: xai" for the bare alias, so the discovery output
// could not be fed back into the endpoint it was advertising. Publishing the
// provider's actual image models costs one catalog read and makes the listing
// round-trippable.
//
// Detection is by marker rather than an explicit allowlist on purpose: a new
// image model from any provider is picked up without a second edit here. The
// markers are ordered longest-first for readability only; matches are
// substring, so there is no precedence to get wrong.

// imageModelMarkers identify catalog entries that generate images rather than
// chat text. Kept as markers rather than an explicit list so a new image model
// from any provider is picked up without a second edit.
var imageModelMarkers = []string{
	"imagen", "image", "dall-e", "gpt-image", "flux", "stable-diffusion",
	"stable-image", "sd3", "sd35", "seedance", "seedream", "recraft",
	"gen4_image", "gen4-image", "kling", "venice-sd", "sdwebui",
}

// IsImageModelID reports whether a model id from the catalog generates images.
//
// Matching is substring after lowercasing, which is what makes a new family
// land without an edit here: "gemini-3.1-flash-image-preview",
// "fal-ai/flux-pro/v1.1" and "bytedance-seedance-2.0" all match without being
// named. The cost of that breadth is that an id merely containing a marker
// ("image" inside an unrelated id) also matches; no such id exists in the
// shipped catalog, and a false positive only adds an entry to a discovery
// listing rather than routing anything.
func IsImageModelID(modelID string) bool {
	if modelID == "" {
		return false
	}
	lower := strings.ToLower(modelID)
	for _, marker := range imageModelMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ImageModelsForProvider returns the provider's catalog image models in
// catalog order, resolving the id through its alias when the id itself is not
// a ProviderModels key (both spellings exist: "xai" and its alias).
//
// A provider whose catalogue carries no recognisable image entry but which
// declares an ImageURL still has an addressable default, resolved by
// DefaultImageModel.
func ImageModelsForProvider(providerID string) []string {
	catalog := GetProviderModels(providerID)
	if len(catalog) == 0 {
		catalog = GetProviderModels(GetProviderAlias(providerID))
	}
	if len(catalog) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	for _, modelID := range catalog {
		if IsImageModelID(modelID) {
			out = append(out, modelID)
		}
	}
	return out
}

// DefaultImageModel returns the model id to advertise for a provider whose
// catalogue carries no recognisable image entry, so /v1/models/image never
// hands out an id that resolution rejects.
//
// The first catalog entry is a deliberate fallback rather than "default": a
// provider that declares an ImageURL but ships no obvious image model (a
// user-defined node, a relay) is still reachable, and naming its first model
// is the honest guess. The literal "default" is the last resort so the entry
// is never empty.
func DefaultImageModel(providerID string) string {
	if models := ImageModelsForProvider(providerID); len(models) > 0 {
		return models[0]
	}
	catalog := GetProviderModels(providerID)
	if len(catalog) == 0 {
		catalog = GetProviderModels(GetProviderAlias(providerID))
	}
	if len(catalog) > 0 {
		return catalog[0]
	}
	return "default"
}
