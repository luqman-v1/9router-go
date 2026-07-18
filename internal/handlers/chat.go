package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/translator"
)

// HandleChatCompletions handles POST /v1/chat/completions (OpenAI format requests).
func (h *ChatHandler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
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
		h.handleComboFallback(w, body, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream, false)
		return
	}

	h.handleSingleModel(w, body, modelInfo, reqBody.Stream, false)
}

// handleSingleModel resolves a single ModelInfo and forwards the request upstream.
func (h *ChatHandler) handleSingleModel(w http.ResponseWriter, body []byte, modelInfo *ModelInfo, isStream bool, translateResponse bool) {
	var upstreamBody map[string]any
	if err := json.Unmarshal(body, &upstreamBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse request body")
		return
	}
	upstreamBody["model"] = modelInfo.Model

	upstreamJSON, err := json.Marshal(upstreamBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal upstream request")
		return
	}

	result := h.handleAccountFallback(w, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, upstreamJSON, isStream, translateResponse, "/v1/chat/completions")
	if result != nil {
		if ue, ok := result.(*upstreamError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
	}
}

// HandleMessages handles POST /v1/messages (Claude format requests).
func (h *ChatHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[error] component=messages err=\"read body: %v\"", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		log.Printf("[error] component=messages err=\"parse JSON: %v\"", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		log.Printf("[error] component=messages err=\"resolve model: %v\" model=%s", err, reqBody.Model)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	openaiBody, err := translator.TranslateClaudeToOpenAI(body)
	if err != nil {
		log.Printf("[error] component=messages err=\"translate: %v\"", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("translation error: %v", err))
		return
	}

	var translatedReq map[string]any
	if err := json.Unmarshal(openaiBody, &translatedReq); err != nil {
		log.Printf("[error] component=messages err=\"parse translated: %v\"", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to parse translated request")
		return
	}
	translatedReq["stream"] = reqBody.Stream

	if len(modelInfo.ComboModels) > 0 {
		h.handleMessagesComboFallback(w, translatedReq, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream)
		return
	}

	h.handleMessagesSingleModel(w, translatedReq, modelInfo, reqBody.Stream)
}

// handleMessagesSingleModel forwards a translated Claude request for a single model.
func (h *ChatHandler) handleMessagesSingleModel(w http.ResponseWriter, translatedReq map[string]any, modelInfo *ModelInfo, isStream bool) {
	translatedReq["model"] = modelInfo.Model
	finalBody, err := json.Marshal(translatedReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal translated request")
		return
	}

	result := h.handleAccountFallback(w, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, finalBody, isStream, true, "/v1/v1/messages")
	if result != nil {
		if ue, ok := result.(*upstreamError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
	}
}

// SetupRoutes mounts the chat handler routes on the provided chi router.
func SetupRoutes(r interface {
	Post(pattern string, handlerFn http.HandlerFunc)
}, repo *db.Repo, ts *TokenSaverConfig) {
	handler := NewChatHandler(repo, ts)

	r.Post("/chat/completions", handler.HandleChatCompletions)
	r.Post("/messages", handler.HandleMessages)
	r.Post("/embeddings", handler.HandleEmbeddings)
	r.Post("/responses", handler.HandleResponses)
}
