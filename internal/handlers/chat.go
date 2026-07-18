package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ChatHandler handles /v1/chat/completions (OpenAI) and /v1/messages (Claude) endpoints.
type ChatHandler struct {
	Repo   *db.Repo
	Client *http.Client
}

// NewChatHandler creates a ChatHandler with the given repository and a streaming-capable HTTP client.
func NewChatHandler(repo *db.Repo) *ChatHandler {
	return &ChatHandler{
		Repo: repo,
		Client: &http.Client{
			Timeout: 0, // no timeout for streaming support
		},
	}
}

// ModelInfo holds the resolved provider and model identifiers.
// ConnectionID, when set, pins a specific connection found during resolution
// so getBestConnection can skip the DB lookup.
// ComboModels, when non-empty, lists all "provider/model" entries from a combo.
// The handler iterates through them on upstream failure.
type ModelInfo struct {
	Provider     string
	Model        string
	ConnectionID string   // optional — set when the resolver already found a connection
	ComboModels  []string // non-empty when resolved from a combo; each entry is "provider/model"
}

// ConnectionData holds parsed fields from the providerConnections.data JSON blob.
type ConnectionData struct {
	APIKey      string `json:"apiKey"`
	AccessToken string `json:"accessToken"`
	BaseURL     string `json:"baseUrl,omitempty"`
}

// ProviderConfig describes how to reach an upstream provider.
type ProviderConfig struct {
	BaseURL    string
	AuthHeader string // "Authorization" or "x-api-key"
	AuthScheme string // "bearer" or "raw"
}

// knownProviders maps provider IDs to their upstream configuration.
var knownProviders = map[string]ProviderConfig{
	"openai": {
		BaseURL:    "https://api.openai.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"anthropic": {
		BaseURL:    "https://api.anthropic.com/v1/messages",
		AuthHeader: "x-api-key",
		AuthScheme: "raw",
	},
	"deepseek": {
		BaseURL:    "https://api.deepseek.com/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"groq": {
		BaseURL:    "https://api.groq.com/openai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"nvidia": {
		BaseURL:    "https://integrate.api.nvidia.com/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"openrouter": {
		BaseURL:    "https://openrouter.ai/api/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"cerebras": {
		BaseURL:    "https://api.cerebras.ai/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"together": {
		BaseURL:    "https://api.together.xyz/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
	"fireworks": {
		BaseURL:    "https://api.fireworks.ai/inference/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	},
}

// providerAliasMap maps common short aliases to canonical provider IDs.
var providerAliasMap = map[string]string{
	"ds":       "deepseek",
	"ant":      "anthropic",
	"oa":       "openai",
	"gq":       "groq",
	"nv":       "nvidia",
	"or":       "openrouter",
	"cf":       "cloudflare-ai",
	"cb":       "cerebras",
	"tg":       "together",
	"fw":       "fireworks",
}

// upstreamError captures a non-200 upstream response so the caller can
// retry with a different model (combo fallback) or write it to the client.
type upstreamError struct {
	StatusCode int
	Body       []byte
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("upstream returned %d", e.StatusCode)
}

// resolveModelEntry parses a single "provider/model" string into a ModelInfo
// without combo or alias resolution (used when iterating combo entries).
func (h *ChatHandler) resolveModelEntry(entry string) *ModelInfo {
	if !strings.Contains(entry, "/") {
		return nil
	}
	parts := strings.SplitN(entry, "/", 2)
	provider := resolveProviderAlias(parts[0])
	if _, ok := knownProviders[provider]; !ok {
		if info := h.resolvePrefixProvider(provider, parts[1]); info != nil {
			return info
		}
	}
	return &ModelInfo{Provider: provider, Model: parts[1]}
}

// resolveProviderAlias resolves a provider alias to its canonical ID.
func resolveProviderAlias(alias string) string {
	if canonical, ok := providerAliasMap[alias]; ok {
		return canonical
	}
	return alias
}

// resolveModel resolves a model string through aliases, combos, and provider/model parsing.
// Returns the first concrete ModelInfo found, or an error.
func (h *ChatHandler) resolveModel(modelStr string) (*ModelInfo, error) {
	if modelStr == "" {
		return nil, fmt.Errorf("missing model")
	}

	// 1. Standard format: "provider/model"
	if strings.Contains(modelStr, "/") {
		parts := strings.SplitN(modelStr, "/", 2)
		providerAlias := parts[0]
		model := parts[1]
		provider := resolveProviderAlias(providerAlias)

		// If the resolved provider is not a known hardcoded provider, check if it's a providerNode prefix
		if _, ok := knownProviders[provider]; !ok {
			if info := h.resolvePrefixProvider(provider, model); info != nil {
				return info, nil
			}
		}

		return &ModelInfo{Provider: provider, Model: model}, nil
	}

	// 2. Check if it's a model alias (e.g., "gpt-4o" -> "openai/gpt-4o")
	aliasTarget, err := h.Repo.GetModelAlias(modelStr)
	if err == nil && aliasTarget != "" {
		// Alias target is "provider/model"
		if strings.Contains(aliasTarget, "/") {
			parts := strings.SplitN(aliasTarget, "/", 2)
			provider := resolveProviderAlias(parts[0])
			// Check providerNode prefix for alias targets too
			if _, ok := knownProviders[provider]; !ok {
				if info := h.resolvePrefixProvider(provider, parts[1]); info != nil {
					return info, nil
				}
			}
			return &ModelInfo{
				Provider: provider,
				Model:    parts[1],
			}, nil
		}
	}

	// 3. Check if it's a combo name
	combo, err := h.Repo.GetComboByName(modelStr)
	if err == nil && combo != nil && combo.Models != "" {
		// Parse combo models array - each entry is a "provider/model" string
		var modelStrings []string
		if err := json.Unmarshal([]byte(combo.Models), &modelStrings); err == nil && len(modelStrings) > 0 {
			// Use the first model in the combo as the primary; store all for fallback
			firstModel := modelStrings[0]
			if strings.Contains(firstModel, "/") {
				parts := strings.SplitN(firstModel, "/", 2)
				provider := resolveProviderAlias(parts[0])
				// Check providerNode prefix for combo entries too
				if _, ok := knownProviders[provider]; !ok {
					if info := h.resolvePrefixProvider(provider, parts[1]); info != nil {
						info.ComboModels = modelStrings
						return info, nil
					}
				}
				return &ModelInfo{
					Provider:    provider,
					Model:       parts[1],
					ComboModels: modelStrings,
				}, nil
			}
		}
	}

	// 4. If no provider prefix, check if a connection exists for this as a raw model name
	// Try common providers
	for _, provider := range []string{"openai", "anthropic", "deepseek"} {
		conns, err := h.Repo.GetProviderConnections(provider, true)
		if err == nil && len(conns) > 0 {
			return &ModelInfo{Provider: provider, Model: modelStr}, nil
		}
	}

	return nil, fmt.Errorf("could not resolve model: %s", modelStr)
}

// resolvePrefixProvider checks if a provider name is a providerNode prefix.
// If so, it finds the matching connection and returns a pinned ModelInfo.
// Returns nil when no providerNode matches the prefix.
func (h *ChatHandler) resolvePrefixProvider(prefix string, model string) *ModelInfo {
	node, _, err := h.Repo.GetProviderNodeByPrefix(prefix)
	if err != nil || node == nil {
		return nil
	}

	// Find the best active connection for this providerNode
	conn, _, err := h.getBestConnection(node.ID)
	if err != nil || conn == nil {
		return nil
	}

	return &ModelInfo{
		Provider:     node.ID,
		Model:        model,
		ConnectionID: conn.ID,
	}
}

// getBestConnection retrieves the highest-priority active connection for a provider.
// When connectionID is non-empty, it fetches that specific connection directly,
// skipping the priority-based query.
func (h *ChatHandler) getBestConnection(provider string, connectionID ...string) (*models.ProviderConnection, *ConnectionData, error) {
	var conn *models.ProviderConnection
	var err error

	if len(connectionID) > 0 && connectionID[0] != "" {
		conn, err = h.Repo.GetProviderConnectionByID(connectionID[0])
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch connection %s: %w", connectionID[0], err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %s not found", connectionID[0])
		}
	} else {
		connections, queryErr := h.Repo.GetProviderConnections(provider, true)
		if queryErr != nil {
			return nil, nil, fmt.Errorf("failed to query connections for %s: %w", provider, queryErr)
		}
		if len(connections) == 0 {
			return nil, nil, fmt.Errorf("no active connections for provider: %s", provider)
		}
		conn = connections[0]
	}

	var connData ConnectionData
	if conn.Data != "" {
		if err := json.Unmarshal([]byte(conn.Data), &connData); err != nil {
			return nil, nil, fmt.Errorf("failed to parse connection data: %w", err)
		}
	}

	return conn, &connData, nil
}

// getProviderConfig returns the upstream configuration for a provider.
func (h *ChatHandler) getProviderConfig(provider string, connData *ConnectionData) (*ProviderConfig, error) {
	// Allow connection-level base URL override
	if connData.BaseURL != "" {
		return &ProviderConfig{
			BaseURL:    connData.BaseURL,
			AuthHeader: "Authorization",
			AuthScheme: "bearer",
		}, nil
	}

	// Check hardcoded known providers first
	if cfg, ok := knownProviders[provider]; ok {
		return &cfg, nil
	}

	// Fall back to providerNodes table — the provider string may be a providerNodes.id
	node, nodeData, err := h.Repo.GetProviderNodeByID(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to look up provider node %s: %w", provider, err)
	}
	if node != nil && nodeData != nil && nodeData.BaseURL != "" {
		baseURL := nodeData.BaseURL
		// Append the chat completions path if not already present
		if !strings.HasSuffix(baseURL, "/chat/completions") {
			if strings.HasSuffix(baseURL, "/v1") || strings.HasSuffix(baseURL, "/v1/") {
				baseURL = strings.TrimRight(baseURL, "/") + "/chat/completions"
			} else {
				baseURL = strings.TrimRight(baseURL, "/") + "/v1/chat/completions"
			}
		}
		return &ProviderConfig{
			BaseURL:    baseURL,
			AuthHeader: "Authorization",
			AuthScheme: "bearer",
		}, nil
	}

	// Last resort — should not normally be reached when connections exist
	return &ProviderConfig{
		BaseURL:    fmt.Sprintf("https://%s.example.com/v1/chat/completions", provider),
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	}, nil
}

// extractAPIKey gets the API key from a connection's data.
func extractAPIKey(connData *ConnectionData) string {
	if connData.APIKey != "" {
		return connData.APIKey
	}
	return connData.AccessToken
}

// HandleChatCompletions handles POST /v1/chat/completions (OpenAI format requests).
func (h *ChatHandler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	// Parse the request to extract model
	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if reqBody.Model == "" {
		writeJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	// Resolve model
	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Combo fallback: iterate through all combo models until one succeeds
	if len(modelInfo.ComboModels) > 0 {
		h.handleComboFallback(w, body, modelInfo.ComboModels, reqBody.Stream, false)
		return
	}

	// Single model path (no combo)
	h.handleSingleModel(w, body, modelInfo, reqBody.Stream, false)
}

// handleSingleModel resolves a single ModelInfo and forwards the request upstream.
// On non-200 upstream responses it writes the error to w.
func (h *ChatHandler) handleSingleModel(w http.ResponseWriter, body []byte, modelInfo *ModelInfo, isStream bool, translateResponse bool) {
	_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID)
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
		writeJSONError(w, http.StatusUnauthorized, "no API key found for connection")
		return
	}

	// Build upstream body with the resolved model name
	var upstreamBody map[string]interface{}
	if err := json.Unmarshal(body, &upstreamBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to parse request body")
		return
	}
	upstreamBody["model"] = modelInfo.Model

	upstreamJSON, err := json.Marshal(upstreamBody)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to marshal upstream request")
		return
	}

	err = h.forwardRequest(w, providerCfg, apiKey, upstreamJSON, isStream, translateResponse)
	if err != nil {
		if ue, ok := err.(*upstreamError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
	}
}

// handleComboFallback iterates through combo model entries, trying each one.
// On success (200), the response is written to w and iteration stops.
// On failure, the next model is tried. If all fail, the last error is written to w.
func (h *ChatHandler) handleComboFallback(w http.ResponseWriter, body []byte, comboModels []string, isStream bool, translateResponse bool) {
	var lastErr *upstreamError

	for _, entry := range comboModels {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
		}

		_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID)
		if err != nil {
			continue // no connection for this provider, try next
		}

		providerCfg, err := h.getProviderConfig(modelInfo.Provider, connData)
		if err != nil {
			continue
		}

		apiKey := extractAPIKey(connData)
		if apiKey == "" {
			continue
		}

		// Build upstream body with the resolved model name
		var upstreamBody map[string]interface{}
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to parse request body")
			return
		}
		upstreamBody["model"] = modelInfo.Model

		upstreamJSON, err := json.Marshal(upstreamBody)
		if err != nil {
			continue
		}

		err = h.forwardRequest(w, providerCfg, apiKey, upstreamJSON, isStream, translateResponse)
		if err != nil {
			if ue, ok := err.(*upstreamError); ok {
				lastErr = ue
				continue // try next model
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, err))}
			continue
		}
		return // success — response already written to w
	}

	// All models failed
	if lastErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastErr.StatusCode)
		w.Write(lastErr.Body)
		return
	}
	writeJSONError(w, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// HandleMessages handles POST /v1/messages (Claude format requests).
func (h *ChatHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	// Parse Claude request to extract model
	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if reqBody.Model == "" {
		writeJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	// Resolve model
	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Translate Claude request to OpenAI format (done once, before combo loop)
	openaiBody, err := translator.TranslateClaudeToOpenAI(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("translation error: %v", err))
		return
	}

	var translatedReq map[string]interface{}
	if err := json.Unmarshal(openaiBody, &translatedReq); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to parse translated request")
		return
	}
	translatedReq["stream"] = reqBody.Stream

	// Combo fallback: iterate through all combo models
	if len(modelInfo.ComboModels) > 0 {
		h.handleMessagesComboFallback(w, translatedReq, modelInfo.ComboModels, reqBody.Stream)
		return
	}

	// Single model path
	h.handleMessagesSingleModel(w, translatedReq, modelInfo, reqBody.Stream)
}

// handleMessagesSingleModel forwards a translated Claude request for a single model.
func (h *ChatHandler) handleMessagesSingleModel(w http.ResponseWriter, translatedReq map[string]interface{}, modelInfo *ModelInfo, isStream bool) {
	_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID)
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
		writeJSONError(w, http.StatusUnauthorized, "no API key found for connection")
		return
	}

	translatedReq["model"] = modelInfo.Model
	finalBody, err := json.Marshal(translatedReq)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to marshal translated request")
		return
	}

	err = h.forwardRequest(w, providerCfg, apiKey, finalBody, isStream, true)
	if err != nil {
		if ue, ok := err.(*upstreamError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
	}
}

// handleMessagesComboFallback iterates through combo models for the Claude endpoint.
func (h *ChatHandler) handleMessagesComboFallback(w http.ResponseWriter, translatedReq map[string]interface{}, comboModels []string, isStream bool) {
	var lastErr *upstreamError

	for _, entry := range comboModels {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
		}

		_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID)
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

		// Set model for this combo entry
		entryReq := make(map[string]interface{}, len(translatedReq))
		for k, v := range translatedReq {
			entryReq[k] = v
		}
		entryReq["model"] = modelInfo.Model

		upstreamJSON, err := json.Marshal(entryReq)
		if err != nil {
			continue
		}

		err = h.forwardRequest(w, providerCfg, apiKey, upstreamJSON, isStream, true)
		if err != nil {
			if ue, ok := err.(*upstreamError); ok {
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, err))}
			continue
		}
		return // success
	}

	if lastErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastErr.StatusCode)
		w.Write(lastErr.Body)
		return
	}
	writeJSONError(w, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// forwardRequest sends the request to the upstream provider and streams/pipes the response.
// If translateResponse is true, OpenAI SSE chunks are translated to Claude SSE format.
func (h *ChatHandler) forwardRequest(
	w http.ResponseWriter,
	cfg *ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
) error {
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create upstream request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Set auth header based on provider config
	switch cfg.AuthScheme {
	case "bearer":
		req.Header.Set(cfg.AuthHeader, "Bearer "+apiKey)
	case "raw":
		req.Header.Set(cfg.AuthHeader, apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// Set streaming headers for SSE
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if isStream {
		return h.handleStreamResponse(w, resp.Body, translateResponse)
	}
	return h.handleJSONResponse(w, resp.Body, translateResponse)
}

// handleStreamResponse pipes SSE chunks from upstream to the client.
// If translateResponse is true, it translates OpenAI chunks to Claude format.
func (h *ChatHandler) handleStreamResponse(w http.ResponseWriter, upstream io.Reader, translate bool) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	if !translate {
		// Direct pipe - no translation needed
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := upstream.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		return nil
	}

	// Translate OpenAI SSE to Claude SSE
	flusher, _ := w.(http.Flusher)
	return proxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStream(chunk)
		if err != nil || translated == nil {
			return
		}
		// translated already has full SSE format (event: X\ndata: Y\n\n)
		w.Write(translated)
		if flusher != nil {
			flusher.Flush()
		}
	})
}

// handleJSONResponse forwards a non-streaming JSON response.
// If translateResponse is true, translates the OpenAI JSON response to Claude format.
func (h *ChatHandler) handleJSONResponse(w http.ResponseWriter, upstream io.Reader, translate bool) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("failed to read upstream response: %w", err)
	}

	if !translate {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	// For non-streaming Claude translation, wrap as a single SSE chunk then translate
	// The TranslateOpenAIToClaudeStream expects raw JSON or SSE-prefixed data
	translated, err := translator.TranslateOpenAIToClaudeStream(body)
	if err != nil || translated == nil {
		// Translation failure — return a proper error to the Claude-format client
		errMsg := "failed to translate upstream response to Claude format"
		if err != nil {
			errMsg = errMsg + ": " + err.Error()
		}
		writeJSONError(w, http.StatusBadGateway, errMsg)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(translated)
	return nil
}

// writeJSONError writes a standardized JSON error response.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	errResp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "invalid_request_error",
			"code":    status,
		},
	}
	jsonBytes, err := json.Marshal(errResp)
	if err != nil {
		// Last-resort fallback — should never happen with simple maps
		w.Write([]byte(`{"error":{"message":"internal error","type":"invalid_request_error","code":500}}`))
		return
	}
	w.Write(jsonBytes)
}

// SetupRoutes mounts the chat handler routes on the provided chi router.
// Requires a db.Repo instance for API key middleware and handler initialization.
func SetupRoutes(r interface {
	Post(pattern string, handlerFn http.HandlerFunc)
}, repo *db.Repo) {
	handler := NewChatHandler(repo)

	r.Post("/chat/completions", handler.HandleChatCompletions)
	r.Post("/messages", handler.HandleMessages)
}
