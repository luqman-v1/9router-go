package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// HandleEmbeddings forwards /v1/embeddings requests to upstream providers.
func (h *MediaHandler) HandleEmbeddings(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "embedding"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	if !h.ChatH.EnforceModelAccess(w, r, reqBody.Model) {
		return
	}
	modelInfo, err := h.ChatH.ResolveModel(reqBody.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
	// Google Gemini embeddings forward dimensions as outputDimensionality (upstream #1366).
	if providers.ResolveAlias(modelInfo.Provider) == "gemini" {
		finalBody = forwardGeminiEmbeddingDimensions(finalBody)
	}
	if isGitHubCopilotProvider(modelInfo.Provider) {
		finalBody = wrapCopilotEmbeddingInput(finalBody)
	}
	// NVIDIA asymmetric embedding models require an input_type hint; symmetric
	// ones (nvclip) and non-embedding models (parakeet-ctc-1.1b-asr) must not
	// receive it. Gate on the exact registry set, not a suffix.
	if providers.ResolveAlias(modelInfo.Provider) == "nvidia" && nvidiaAsymmetricEmbeddingModels[modelInfo.Model] {
		finalBody = forwardNvidiaEmbeddingInputType(finalBody)
	}

	h.forwardJSONMediaLane(w, r, body, finalBody, modelInfo, "/embeddings", true, buildEmbeddingsURL)
}

// HandleRerank forwards /v1/rerank requests to upstream providers.
//
// Rerank is the same shape as embeddings — a model in, a JSON body out, no
// streaming — so it shares forwardJSONMediaLane with HandleEmbeddings and derives
// its /rerank endpoint via buildRerankURL.
func (h *MediaHandler) HandleRerank(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "embedding"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}
	if !h.ChatH.EnforceModelAccess(w, r, reqBody.Model) {
		return
	}
	modelInfo, err := h.ChatH.ResolveModel(reqBody.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
	h.forwardJSONMediaLane(w, r, body, finalBody, modelInfo, "/v1/rerank", false, buildRerankURL)
}

// buildRerankURL derives the rerank lane from a provider's base URL. A base
// that already names a chat or embeddings lane is rewritten to the rerank
// sibling, so a connection saved with either spelling still resolves.
func buildRerankURL(baseURL string) string {
	for _, lane := range []string{"/chat/completions", "/embeddings"} {
		if strings.Contains(baseURL, lane) {
			return strings.Replace(baseURL, lane, "/rerank", 1)
		}
	}
	return strings.TrimRight(baseURL, "/") + "/rerank"
}

// BuildEmbeddingsURL converts a chat completions URL to an embeddings URL.
func BuildEmbeddingsURL(baseURL string) string {
	return buildEmbeddingsURL(baseURL)
}

func buildEmbeddingsURL(baseURL string) string {
	if strings.Contains(baseURL, "/chat/completions") {
		return strings.Replace(baseURL, "/chat/completions", "/embeddings", 1)
	}
	return strings.TrimRight(baseURL, "/") + "/embeddings"
}

// forwardGeminiEmbeddingDimensions maps incoming OpenAI "dimensions" to
// Google Gemini's "outputDimensionality" property (upstream decolua/9router#1366 parity).
func forwardGeminiEmbeddingDimensions(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	dimsRaw, exists := payload["dimensions"]
	if !exists || dimsRaw == nil {
		return body
	}
	dims := 0
	switch v := dimsRaw.(type) {
	case float64:
		dims = int(v)
	case int:
		dims = v
	case int64:
		dims = int(v)
	}
	if dims <= 0 {
		return body
	}
	payload["outputDimensionality"] = dims
	if updated, err := json.Marshal(payload); err == nil {
		return updated
	}
	return body
}

// isGitHubCopilotProvider reports whether the resolved provider is GitHub Copilot.
func isGitHubCopilotProvider(provider string) bool {
	return providers.ResolveAlias(provider) == "github"
}

// wrapCopilotEmbeddingInput wraps a bare "input" string into an array of
// strings ([input]), which Copilot requires. It leaves an existing array
// untouched and leaves bodies without "input" alone.
func wrapCopilotEmbeddingInput(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}
	input, ok := payload["input"]
	if !ok {
		return body
	}
	if _, isArray := input.([]any); isArray {
		return body
	}
	payload["input"] = []any{input}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// nvidiaAsymmetricEmbeddingModels lists NVIDIA embedding models that require an
// input_type hint (asymmetric query/passage). Symmetric models (nvclip) and
// non-embedding models (parakeet-ctc-1.1b-asr) are intentionally excluded.
var nvidiaAsymmetricEmbeddingModels = map[string]bool{
	"nvidia/nemotron-3.5-embed-8b":                   true,
	"nemotron-3.5-embed-8b":                          true,
	"nvidia/nemotron-3.5-embed-1b":                   true,
	"nemotron-3.5-embed-1b":                          true,
	"nvidia/nemotron-3-embed-1b":                     true,
	"nemotron-3-embed-1b":                            true,
	"nvidia/nv-embedqa-mistral-7b-v2":                true,
	"nv-embedqa-mistral-7b-v2":                       true,
	"nvidia/embed-qa-4":                              true,
	"embed-qa-4":                                     true,
	"nvidia/llama-3.2-nemoretriever-1b-vlm-embed-v1": true,
	"llama-3.2-nemoretriever-1b-vlm-embed-v1":        true,
	"nvidia/llama-3.2-nv-embedqa-1b-v1":              true,
	"llama-3.2-nv-embedqa-1b-v1":                     true,
}

// forwardNvidiaEmbeddingInputType injects input_type for NVIDIA's asymmetric
// embedding models: preserve a client-supplied "query"/"passage", otherwise
// default to "query" (upstream decolua/9router@a851a89c4).
func forwardNvidiaEmbeddingInputType(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}
	raw, _ := payload["input_type"].(string)
	val := strings.TrimSpace(strings.ToLower(raw))
	if val != "query" && val != "passage" {
		val = "query"
	}
	payload["input_type"] = val
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}
