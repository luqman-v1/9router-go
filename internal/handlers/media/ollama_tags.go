package media

import (
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// Ollama client model discovery: GET /api/tags.
//
// An Ollama client lists models from its base URL before it will talk to it, so
// a gateway without this route looks like an Ollama server with nothing
// installed — `ollama list` prints an empty table and the client never offers a
// model to pull from or chat with.
//
// Upstream answers with a hardcoded two-model fixture. This asks the registry
// instead, so the list is the models the gateway can actually route and stays
// correct when the catalogue changes.

// OllamaTag is one entry of Ollama's /api/tags response.
type OllamaTag struct {
	Name       string        `json:"name"`
	Model      string        `json:"model"`
	ModifiedAt string        `json:"modified_at"`
	Size       int64         `json:"size"`
	Digest     string        `json:"digest"`
	Details    OllamaDetails `json:"details"`
}

// OllamaDetails is the shape Ollama clients read for a model's family and size.
type OllamaDetails struct {
	Format            string `json:"format"`
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
}

// HandleOllamaTags serves GET /api/tags and /tags.
func (h *MediaHandler) HandleOllamaTags(w http.ResponseWriter, r *http.Request) {
	// Ollama clients are frequently browser-based or run from another origin,
	// and they send a preflight before listing.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	models := providers.GetProviderModels("ollama")
	tags := make([]OllamaTag, 0, len(models))
	// A fixed stamp keeps the response stable: Ollama clients use modified_at
	// only for display and for ordering, and a value that changes every request
	// would make `ollama list` re-sort and any cache look stale.
	modified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for _, name := range models {
		tags = append(tags, OllamaTag{
			Name:       name,
			Model:      name,
			ModifiedAt: modified,
			Details: OllamaDetails{
				Format:            "gguf",
				Family:            ollamaFamily(name),
				ParameterSize:     "cloud",
				QuantizationLevel: "cloud",
			},
		})
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"models": tags})
}

// ollamaFamily names the model family the way Ollama clients display it. The
// registry ids carry a provider prefix or a :cloud suffix; neither is part of
// the family, and a UI showing "deepseek-v4.1-flash:cloud" as the family reads
// as a bug.
func ollamaFamily(id string) string {
	fam := id
	if i := strings.IndexByte(fam, ':'); i >= 0 {
		fam = fam[:i]
	}
	if i := strings.IndexByte(fam, '/'); i >= 0 {
		fam = fam[i+1:]
	}
	// The family stops at the first digit or separator that introduces a
	// version: gpt-oss:120b -> gpt-oss, kimi-k2.5 -> kimi-k, glm-5 -> glm,
	// minimax-m2.5 -> minimax-m. A trailing separator is dropped, because
	// "glm-" reads as a truncation rather than a family.
	end := 0
	for end < len(fam) {
		c := fam[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' {
			end++
			continue
		}
		break
	}
	for end > 0 && fam[end-1] == '-' {
		end--
	}
	if end == 0 {
		return fam
	}
	return fam[:end]
}
