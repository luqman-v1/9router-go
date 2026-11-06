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
	// and they send a preflight before listing. The same three headers answer
	// that preflight, which is registered separately.
	handlerutil.WriteCORSHeaders(w)

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

// ollamaFamily names the model family the way Ollama clients display it. Ollama
// derives this from the model's architecture, and its own families carry the
// version (llama, qwen3, gemma3), so nothing here may cut at the first digit:
// an earlier version did, and rendered kimi-k2.5 as "kimi-k".
//
// Two things are stripped instead, both of which are registry decoration rather
// than family: a leading provider prefix and a trailing :tag.
func ollamaFamily(id string) string {
	fam := id
	if i := strings.IndexByte(fam, ':'); i >= 0 {
		fam = fam[:i]
	}
	if i := strings.IndexByte(fam, '/'); i >= 0 {
		fam = fam[i+1:]
	}
	if fam == "" {
		return id
	}
	return fam
}
