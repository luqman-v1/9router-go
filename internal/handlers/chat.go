package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
	"9router/proxy/internal/pricing"
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
	conn, _, err := h.getBestConnection(node.ID, "", nil, model)
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
// excludeIDs lists connection IDs to skip during priority-based lookup.
// model is used for health checking; pass "" to skip the health check.
func (h *ChatHandler) getBestConnection(provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	// Check provider health before selecting a connection
	if model != "" && !db.IsProviderHealthy(h.Repo.RawDB(), provider, model) {
		log.Printf("[health] warning: provider %s/%s is unhealthy, proceeding anyway", provider, model)
	}

	var conn *models.ProviderConnection
	var err error

	if connectionID != "" {
		conn, err = h.Repo.GetProviderConnectionByID(connectionID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch connection %s: %w", connectionID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %s not found", connectionID)
		}
	} else {
		connections, queryErr := h.Repo.GetProviderConnections(provider, true)
		if queryErr != nil {
			return nil, nil, fmt.Errorf("failed to query connections for %s: %w", provider, queryErr)
		}
		if len(connections) == 0 {
			return nil, nil, fmt.Errorf("no active connections for provider: %s", provider)
		}

		// Filter out excluded connection IDs
		excludeSet := make(map[string]bool, len(excludeIDs))
		for _, id := range excludeIDs {
			excludeSet[id] = true
		}

		conn = nil
		for _, c := range connections {
			if !excludeSet[c.ID] {
				conn = c
				break
			}
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("no available connections for provider: %s (all excluded)", provider)
		}
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

// UsageLogInfo holds request context needed to log a usage record.
type UsageLogInfo struct {
	Provider     string
	Model        string
	ConnectionID string
	APIKey       string
	Endpoint     string
}

// logUsage persists a usage record and updates connection metadata.
func (h *ChatHandler) logUsage(info *UsageLogInfo, usage *translator.OpenAIUsage) {
	totalTokens := usage.PromptTokens + usage.CompletionTokens
	cost := pricing.EstimateCost(info.Model, usage.PromptTokens, usage.CompletionTokens)
	metaJSON := fmt.Sprintf(`{"provider":"%s","model":"%s","connectionId":"%s"}`, info.Provider, info.Model, info.ConnectionID)

	if err := h.Repo.InsertUsageHistory(info.Provider, info.Model, info.ConnectionID, maskAPIKey(info.APIKey), info.Endpoint, usage.PromptTokens, usage.CompletionTokens, cost, "success", totalTokens, metaJSON); err != nil {
		log.Printf("[usage] failed to insert usage history: %v", err)
	}

	if info.ConnectionID != "" {
		if err := h.Repo.UpdateConnectionLastUsed(info.ConnectionID); err != nil {
			log.Printf("[usage] failed to update connection lastUsed: %v", err)
		}
	}

	h.upsertDailyUsage(info.Provider, info.Model, usage.PromptTokens, usage.CompletionTokens)
}

// upsertDailyUsage reads the existing daily aggregate, merges new tokens, and upserts.
func (h *ChatHandler) upsertDailyUsage(provider, model string, promptTokens, completionTokens int) {
	dateKey := time.Now().UTC().Format("2006-01-02")
	existing, _ := h.Repo.GetUsageDaily(dateKey)

	data := parseDailyData(existing)
	data["totalPromptTokens"] = getJSONInt(data, "totalPromptTokens") + promptTokens
	data["totalCompletionTokens"] = getJSONInt(data, "totalCompletionTokens") + completionTokens
	data["totalRequests"] = getJSONInt(data, "totalRequests") + 1

	providers := getJSONMap(data, "providers")
	pd := getJSONMap(providers, provider)
	pd["promptTokens"] = getJSONInt(pd, "promptTokens") + promptTokens
	pd["completionTokens"] = getJSONInt(pd, "completionTokens") + completionTokens
	pd["requests"] = getJSONInt(pd, "requests") + 1

	models := getJSONMap(pd, "models")
	md := getJSONMap(models, model)
	md["promptTokens"] = getJSONInt(md, "promptTokens") + promptTokens
	md["completionTokens"] = getJSONInt(md, "completionTokens") + completionTokens
	md["requests"] = getJSONInt(md, "requests") + 1
	models[model] = md
	pd["models"] = models
	providers[provider] = pd
	data["providers"] = providers

	dataJSON, err := json.Marshal(data)
	if err != nil {
		log.Printf("[usage] failed to marshal daily usage: %v", err)
		return
	}
	if err := h.Repo.UpsertUsageDaily(dateKey, string(dataJSON)); err != nil {
		log.Printf("[usage] failed to upsert daily usage: %v", err)
	}
}

// parseDailyData parses an existing daily JSON string into a mutable map.
func parseDailyData(raw string) map[string]interface{} {
	if raw == "" {
		return make(map[string]interface{})
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return make(map[string]interface{})
	}
	return m
}

// getJSONInt extracts an int from a JSON map (stored as float64 after unmarshal).
func getJSONInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		if n, ok := v.(float64); ok {
			return int(n)
		}
	}
	return 0
}

// getJSONMap extracts or creates a nested map from a JSON map.
func getJSONMap(m map[string]interface{}, key string) map[string]interface{} {
	if v, ok := m[key]; ok {
		if sub, ok := v.(map[string]interface{}); ok {
			return sub
		}
	}
	return make(map[string]interface{})
}

// maskAPIKey returns a masked version of an API key for storage.
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
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
// On 401/429 upstream responses it locks the model and retries with the next available account.
// On other non-200 responses it writes the error to w.
func (h *ChatHandler) handleSingleModel(w http.ResponseWriter, body []byte, modelInfo *ModelInfo, isStream bool, translateResponse bool) {
	// Build upstream body once
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

	result := h.handleAccountFallback(w, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, upstreamJSON, isStream, translateResponse)
	if result != nil {
		// Non-retryable error — write to client
		if ue, ok := result.(*upstreamError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
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

		_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
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
// On 401/429 upstream responses it locks the model and retries with the next available account.
func (h *ChatHandler) handleMessagesSingleModel(w http.ResponseWriter, translatedReq map[string]interface{}, modelInfo *ModelInfo, isStream bool) {
	translatedReq["model"] = modelInfo.Model
	finalBody, err := json.Marshal(translatedReq)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to marshal translated request")
		return
	}

	result := h.handleAccountFallback(w, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, finalBody, isStream, true)
	if result != nil {
		if ue, ok := result.(*upstreamError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
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

		_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
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

// retryableStatusCodes are HTTP status codes that trigger account fallback.
var retryableStatusCodes = map[int]bool{
	http.StatusUnauthorized:     true, // 401
	http.StatusTooManyRequests:  true, // 429
}

// handleAccountFallback attempts to forward a request with automatic account fallback.
// On 401/429 upstream errors, it locks the provider/model and retries with the next available connection.
// Returns nil on success (response already written to w), or the last error on failure.
func (h *ChatHandler) handleAccountFallback(
	w http.ResponseWriter,
	provider string,
	model string,
	pinnedConnectionID string,
	body []byte,
	isStream bool,
	translateResponse bool,
) error {
	// If a specific connection is pinned, try it once without fallback
	if pinnedConnectionID != "" {
		_, connData, err := h.getBestConnection(provider, pinnedConnectionID, nil, model)
		if err != nil {
			return err
		}
		return h.tryForwardWithConnection(w, provider, model, pinnedConnectionID, connData, body, isStream, translateResponse)
	}

	// Check if provider/model is healthy
	if !db.IsProviderHealthy(h.Repo.RawDB(), provider, model) {
		log.Printf("[health] provider %s/%s is unhealthy (consecutive errors >= 5), skipping", provider, model)
	}

	// Check if model is already locked
	locked, _ := h.Repo.IsModelLocked(provider, model)

	// Collect all active connections for this provider
	allConns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(allConns) == 0 {
		return fmt.Errorf("no active connections for provider: %s", provider)
	}

	var excludeIDs []string
	var lastErr error

	for _, conn := range allConns {
		// Skip excluded connections
		if containsID(excludeIDs, conn.ID) {
			continue
		}

		connObj, connData, err := h.getBestConnection(provider, conn.ID, nil, model)
		if err != nil || connObj == nil {
			continue
		}

		apiKey := extractAPIKey(connData)
		if apiKey == "" {
			continue
		}

		_ = apiKey // used by tryForwardWithConnection via connData

		// If the model is locked, skip this connection (it likely caused the lock)
		if locked {
			// Still try other connections if available, but the locked provider/model
			// means at least one account failed — continue to the next
		}

		lastErr = h.tryForwardWithConnection(w, provider, model, connObj.ID, connData, body, isStream, translateResponse)
		if lastErr == nil {
			return nil // success
		}

		// On retryable errors (401/429), lock the model and try next account
		if ue, ok := lastErr.(*upstreamError); ok && retryableStatusCodes[ue.StatusCode] {
			durationSec := 60
			if ue.StatusCode == http.StatusUnauthorized {
				durationSec = 120
			}
			errMsg := fmt.Sprintf("%d upstream error", ue.StatusCode)
			h.Repo.LockModel(provider, model, durationSec, errMsg, ue.StatusCode)
			excludeIDs = append(excludeIDs, conn.ID)
			continue
		}

		// Non-retryable error — return immediately
		return lastErr
	}

	// All connections exhausted
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no available connections for provider: %s", provider)
}

// tryForwardWithConnection attempts a single upstream request using the given connection data.
// It records provider health (status code + latency) after every attempt.
// Returns nil on success (response written to w), or an error.
func (h *ChatHandler) tryForwardWithConnection(
	w http.ResponseWriter,
	provider string,
	model string,
	connectionID string,
	connData *ConnectionData,
	body []byte,
	isStream bool,
	translateResponse bool,
) error {
	providerCfg, err := h.getProviderConfig(provider, connData)
	if err != nil {
		return err
	}

	apiKey := extractAPIKey(connData)
	if apiKey == "" {
		return &upstreamError{StatusCode: http.StatusUnauthorized, Body: []byte(`{"error":{"message":"no API key found","type":"auth_error","code":401}}`)}
	}

	start := time.Now()
	fwdErr := h.forwardRequest(w, providerCfg, apiKey, body, isStream, translateResponse)
	latencyMs := time.Since(start).Milliseconds()

	// Record provider health
	statusCode := http.StatusOK
	if fwdErr != nil {
		if ue, ok := fwdErr.(*upstreamError); ok {
			statusCode = ue.StatusCode
		} else {
			statusCode = http.StatusBadGateway
		}
	}
	if healthErr := db.RecordProviderHealth(h.Repo.RawDB(), provider, model, statusCode, latencyMs); healthErr != nil {
		log.Printf("[health] failed to record health for %s/%s: %v", provider, model, healthErr)
	}

	// Log usage on success
	if fwdErr == nil {
		usage := translator.GetAndClearLastUsage()
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:     provider,
			Model:        model,
			ConnectionID: connectionID,
			APIKey:       apiKey,
			Endpoint:     "chat",
		}
		h.logUsage(logInfo, usage)
	}

	return fwdErr
}

// containsID checks if a string slice contains a given ID.
func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
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
	r.Post("/embeddings", handler.HandleEmbeddings)
	r.Post("/responses", handler.HandleResponses)
}

// updateModelInBody replaces the "model" field in a JSON body.
