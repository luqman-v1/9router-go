package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HandleEmbeddings forwards /v1/embeddings requests to upstream providers.
func (h *ChatHandler) HandleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reqBody.Model == "" {
		writeJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	conn, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	providerCfg, err := h.getProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	apiKey := extractAPIKey(connData)
	if apiKey == "" {
		writeJSONError(w, http.StatusUnauthorized, "no API key found")
		return
	}

	embeddingsURL := buildEmbeddingsURL(providerCfg.BaseURL)
	finalBody := updateModelInBody(body, modelInfo.Model)

	req, err := http.NewRequest("POST", embeddingsURL, strings.NewReader(string(finalBody)))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}

	req.Header.Set("Content-Type", "application/json")
	setAuthHeader(req, apiKey, providerCfg)

	resp, err := h.Client.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	if resp.StatusCode == http.StatusOK {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
}

func buildEmbeddingsURL(baseURL string) string {
	if strings.Contains(baseURL, "/chat/completions") {
		return strings.Replace(baseURL, "/chat/completions", "/embeddings", 1)
	}
	return strings.TrimRight(baseURL, "/") + "/embeddings"
}
