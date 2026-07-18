package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"

	"9router/proxy/internal/handlerutil"
)

// HandleResponses forwards /v1/responses requests to upstream providers.
func (h *ChatHandler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if len(modelInfo.ComboModels) > 0 {
		h.handleResponsesComboFallback(w, body, modelInfo.ComboModels, reqBody.Stream)
		return
	}

	h.handleResponsesSingleModel(w, body, modelInfo, reqBody.Stream)
}

func (h *ChatHandler) handleResponsesSingleModel(w http.ResponseWriter, body []byte, modelInfo *ModelInfo, isStream bool) {
	conn, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	providerCfg, err := h.getProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	apiKey := extractAPIKey(connData)
	if apiKey == "" {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found")
		return
	}

	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
	responsesURL := strings.TrimRight(providerCfg.BaseURL, "/") + "/responses"

	req, err := http.NewRequest("POST", responsesURL, strings.NewReader(string(finalBody)))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}

	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
	if isStream {
		req.Header.Set(constants.HeaderAccept, constants.ContentTypeEventStream)
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(resp.StatusCode)
		w.Write(errBody)
		return
	}

	if isStream {
		h.handleStreamResponse(w, resp.Body, false, time.Now(), &streamMetrics{})
	} else {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		io.Copy(w, resp.Body)
	}

	h.Repo.UpdateConnectionLastUsed(conn.ID)
}

func (h *ChatHandler) handleResponsesComboFallback(w http.ResponseWriter, body []byte, comboModels []string, isStream bool) {
	for _, entry := range comboModels {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
		}
		conn, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
		if err != nil {
			continue
		}
		providerCfg, err := h.getProviderConfig(modelInfo.Provider, connData)
		if err != nil {
			continue
		}
		apiKey := extractAPIKey(connData)
		if apiKey == "" {
			continue
		}

		finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
		responsesURL := strings.TrimRight(providerCfg.BaseURL, "/") + "/responses"

		req, err := http.NewRequest("POST", responsesURL, strings.NewReader(string(finalBody)))
		if err != nil {
			continue
		}
		req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
		handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
		if isStream {
			req.Header.Set(constants.HeaderAccept, constants.ContentTypeEventStream)
		}

		resp, err := h.Client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		if isStream {
			h.handleStreamResponse(w, resp.Body, false, time.Now(), &streamMetrics{})
		} else {
			w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
			w.WriteHeader(http.StatusOK)
			io.Copy(w, resp.Body)
		}
		resp.Body.Close()
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		return
	}

	handlerutil.WriteJSONError(w, http.StatusBadGateway, "all combo models failed")
}
