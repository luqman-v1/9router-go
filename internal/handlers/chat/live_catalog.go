package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// Live model discovery for /v1/models, ported from the upstream
// LIVE_MODEL_RESOLVERS in src/app/api/v1/models/route.js.
//
// Upstream prefers a provider's live catalog over the static registry when a
// connection exists and no explicit enabledModels are configured, which is why
// `kiro` publishes `auto`/`minimax-m2.1` variants that are not in the registry
// and `grok-cli` publishes only the model its account may use.

const (
	liveCatalogTTL       = 5 * time.Minute
	liveCatalogFastHTTP  = 10 * time.Second
	kiroCatalogHTTP      = 30 * time.Second
	kiroRuntimeSDKVer    = "1.0.0"
	kiroAgentOS          = "windows"
	kiroAgentOSVersion   = "10.0.26200"
	kiroNodeVersion      = "22.21.1"
	kiroIDEVersion       = "0.10.32"
	kiroDefaultRegion    = "us-east-1"
	kiroDefaultContext   = 200_000
	grokCLIBaseURL       = "https://cli-chat-proxy.grok.com/v1"
	grokCLIVersion       = providers.GrokCLIVersion
	grokCLIUserAgent     = providers.GrokCLIUserAgent
	grokCLIIdentifier    = providers.GrokCLIClientIdentifier
	grokCLIDefaultModel  = "grok-build"
	grokCLIDefaultCtxLen = 500_000
	grokCLIDefaultMaxOut = 64_000
)

// Endpoints are variables so tests can point the resolvers at a local server.
var (
	kiroCatalogBaseURL = "https://q.%s.amazonaws.com"
	grokCLICatalogURL  = grokCLIBaseURL + "/models"
)

// LiveCapabilities is the capability block a live resolver can publish
// alongside a model. Kiro does (upstream buildVariants emits
// {thinking, agentic} per synthetic variant); grok-cli does not.
type LiveCapabilities struct {
	Thinking bool `json:"thinking"`
	Agentic  bool `json:"agentic"`
}

// LiveModel is one entry of a provider's live catalog.
type LiveModel struct {
	ID            string
	Name          string
	ContextLength int
	MaxOutput     int
	Capabilities  *LiveCapabilities
}

type liveCatalogEntry struct {
	expiresAt time.Time
	models    []LiveModel
}

var liveCatalogStore = struct {
	mu      sync.RWMutex
	entries map[string]liveCatalogEntry
}{entries: make(map[string]liveCatalogEntry)}

// liveCatalogFlight collapses concurrent discoveries for the same credential
// into one upstream call. Without it, N clients hitting /v1/models while the
// cache is cold would each fan out to the provider API (kiro, grok-cli, custom
// node) before the first result populates the cache.
var liveCatalogFlight singleflight.Group

// resolveLiveCatalog returns the live catalog for a provider connection, or nil
// when the provider has no live resolver or discovery failed. Callers fall back
// to the static registry, exactly like upstream's `live?.models?.length` guard.
func (h *ChatHandler) resolveLiveCatalog(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData, providerID string) []LiveModel {
	if conn == nil || connData == nil {
		return nil
	}
	accessToken := connData.AccessToken
	if accessToken == "" {
		accessToken = connData.APIKey
	}
	if accessToken == "" {
		return nil
	}

	var fetch func(context.Context, string) []LiveModel
	switch {
	case providerID == "kiro":
		fetch = func(ctx context.Context, token string) []LiveModel {
			return h.fetchKiroLiveCatalog(ctx, conn, connData, token)
		}
	case providerID == "grok-cli":
		fetch = func(ctx context.Context, token string) []LiveModel {
			return h.fetchGrokCLILiveCatalog(ctx, conn, connData, token)
		}
	case isCompatibleProviderID(providerID):
		// Upstream fetchCompatibleModelIds: custom nodes publish whatever their
		// own /models endpoint reports.
		fetch = func(ctx context.Context, _ string) []LiveModel {
			return h.fetchCompatibleNodeModels(ctx, conn, connData, providerID)
		}
	default:
		return nil
	}

	cacheKey := liveCatalogKey(providerID, conn, connData)
	if cached, ok := liveCatalogCached(cacheKey); ok {
		return cached
	}

	// One upstream call per credential even when many requests race a cold
	// cache: the losers wait for the winner's result instead of each fanning
	// out to the provider API.
	result, err, _ := liveCatalogFlight.Do(cacheKey, func() (any, error) {
		if cached, ok := liveCatalogCached(cacheKey); ok {
			return cached, nil
		}
		discovered := fetch(ctx, accessToken)
		if len(discovered) == 0 && liveCatalogRefreshToken(conn) != "" {
			// Upstream refreshes the credential on 401/403 and retries once.
			if refreshed, _, rErr := h.forceRefreshOAuthToken(conn.ID); rErr == nil && refreshed != "" {
				discovered = fetch(ctx, refreshed)
			} else if rErr != nil {
				log.Warn("models", "live catalog token refresh failed", "provider", providerID, "conn", conn.ID, "error", rErr)
			}
		}
		if len(discovered) == 0 {
			return nil, nil
		}
		liveCatalogStoreModels(cacheKey, discovered)
		return discovered, nil
	})
	if err != nil || result == nil {
		return nil
	}
	discovered, _ := result.([]LiveModel)
	return discovered
}

func liveCatalogKey(providerID string, conn *models.ProviderConnection, connData *shared.ConnectionData) string {
	seed := providerID
	if psd := connData.ProviderSpecificData; psd != nil {
		if s, _ := psd["profileArn"].(string); s != "" {
			seed += "|" + s
		} else if s, _ := psd["clientId"].(string); s != "" {
			seed += "|" + s
		}
	}
	if seed == providerID {
		seed += "|" + conn.ID + "|" + liveCatalogRefreshToken(conn) + "|" + connData.AccessToken + "|" + connData.APIKey
	}
	return seed
}

// liveCatalogRefreshToken reads the OAuth refresh token straight from the
// connection blob — ConnectionData only carries the access token.
func liveCatalogRefreshToken(conn *models.ProviderConnection) string {
	if conn == nil || conn.Data == "" {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &raw); err != nil {
		return ""
	}
	value, _ := raw["refreshToken"].(string)
	return value
}

func liveCatalogCached(key string) ([]LiveModel, bool) {
	liveCatalogStore.mu.RLock()
	defer liveCatalogStore.mu.RUnlock()
	entry, ok := liveCatalogStore.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.models, true
}

func liveCatalogStoreModels(key string, discovered []LiveModel) {
	liveCatalogStore.mu.Lock()
	defer liveCatalogStore.mu.Unlock()
	liveCatalogStore.entries[key] = liveCatalogEntry{expiresAt: time.Now().Add(liveCatalogTTL), models: discovered}
}

// ---- Kiro (AWS CodeWhisperer ListAvailableModels) ----

// kiroFingerprintHeaders mirrors upstream buildKiroFingerprintHeaders: the
// upstream rejects requests whose User-Agent does not look like Kiro IDE.
func kiroFingerprintHeaders(seed, accessToken string) map[string]string {
	sum := sha256.Sum256([]byte(seed))
	machineID := hex.EncodeToString(sum[:])
	userAgent := fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/codewhispererruntime#%s m/N,E KiroIDE-%s-%s",
		kiroRuntimeSDKVer, kiroAgentOS, kiroAgentOSVersion, kiroNodeVersion, kiroRuntimeSDKVer, kiroIDEVersion, machineID,
	)
	return map[string]string{
		"User-Agent":                  userAgent,
		"x-amz-user-agent":            fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", kiroRuntimeSDKVer, kiroIDEVersion, machineID),
		"x-amzn-kiro-agent-mode":      "vibe",
		"x-amzn-codewhisperer-optout": "true",
		"amz-sdk-request":             "attempt=1; max=1",
		"amz-sdk-invocation-id":       uuid.NewString(),
		"Accept":                      "application/json",
		"Authorization":               "Bearer " + accessToken,
	}
}

func kiroRegion(profileArn string) string {
	parts := strings.Split(profileArn, ":")
	if len(parts) >= 4 && parts[3] != "" {
		return parts[3]
	}
	return kiroDefaultRegion
}

type kiroRawModel struct {
	ModelID        string  `json:"modelId"`
	ModelName      string  `json:"modelName"`
	RateMultiplier float64 `json:"rateMultiplier"`
	Description    string  `json:"description"`
	TokenLimits    struct {
		MaxInputTokens int `json:"maxInputTokens"`
	} `json:"tokenLimits"`
}

func (h *ChatHandler) fetchKiroLiveCatalog(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData, accessToken string) []LiveModel {
	profileArn, _ := connData.ProviderSpecificData["profileArn"].(string)
	seed, _ := connData.ProviderSpecificData["clientId"].(string)
	if seed == "" {
		seed = liveCatalogRefreshToken(conn)
	}
	if seed == "" {
		seed = profileArn
	}
	if seed == "" {
		seed = accessToken
	}
	if seed == "" {
		seed = "kiro-anonymous"
	}

	query := url.Values{"origin": []string{"AI_EDITOR"}}
	if profileArn != "" {
		query.Set("profileArn", profileArn)
	}
	endpoint := fmt.Sprintf(kiroCatalogBaseURL, kiroRegion(profileArn)) + "/ListAvailableModels?" + query.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, kiroCatalogHTTP)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		log.Warn("models", "kiro catalog request failed", "conn", conn.ID, "error", err)
		return nil
	}
	for key, value := range kiroFingerprintHeaders(seed, accessToken) {
		req.Header.Set(key, value)
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		log.Warn("models", "kiro catalog fetch failed", "conn", conn.ID, "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warn("models", "kiro catalog rejected", "conn", conn.ID, "status", resp.StatusCode)
		return nil
	}

	var payload struct {
		Models []kiroRawModel `json:"models"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || json.Unmarshal(body, &payload) != nil {
		log.Warn("models", "kiro catalog parse failed", "conn", conn.ID, "error", err)
		return nil
	}

	var out []LiveModel
	for _, raw := range payload.Models {
		upstreamID := raw.ModelID
		if upstreamID == "" {
			continue
		}
		display := kiroDisplayName(raw.ModelName, upstreamID, raw.RateMultiplier)
		ctxLen := raw.TokenLimits.MaxInputTokens
		if ctxLen <= 0 {
			ctxLen = kiroDefaultContext
		}
		out = append(out, expandKiroVariants(upstreamID, display, ctxLen)...)
	}
	return out
}

func kiroDisplayName(modelName, modelID string, rateMultiplier float64) string {
	base := strings.TrimSpace(modelName)
	if base == "" {
		base = strings.TrimSpace(modelID)
	}
	if base == "" {
		base = "Kiro"
	}
	if rateMultiplier <= 0 || rateMultiplier == 1 {
		return "Kiro " + base
	}
	return fmt.Sprintf("Kiro %s (%.1fx credit)", base, rateMultiplier)
}

// expandKiroVariants builds the synthetic variant set upstream exposes: the
// base model, -thinking, and (except for `auto`) -agentic / -thinking-agentic.
// The suffixes do not exist upstream; the Kiro translator strips them.
func expandKiroVariants(upstreamID, display string, ctxLen int) []LiveModel {
	base := strings.TrimSuffix(upstreamID, "-agentic")
	base = strings.TrimSuffix(base, "-thinking")
	variants := []LiveModel{
		{ID: base, Name: display, ContextLength: ctxLen, Capabilities: &LiveCapabilities{}},
		{ID: base + "-thinking", Name: display + " (Thinking)", ContextLength: ctxLen, Capabilities: &LiveCapabilities{Thinking: true}},
	}
	if base != "auto" {
		variants = append(variants,
			LiveModel{ID: base + "-agentic", Name: display + " (Agentic)", ContextLength: ctxLen, Capabilities: &LiveCapabilities{Agentic: true}},
			LiveModel{ID: base + "-thinking-agentic", Name: display + " (Thinking + Agentic)", ContextLength: ctxLen, Capabilities: &LiveCapabilities{Thinking: true, Agentic: true}},
		)
	}
	return variants
}

// ---- Grok CLI ----

func grokCLIHeaders(accessToken string, psd map[string]any) map[string]string {
	headers := map[string]string{
		"Authorization":            "Bearer " + accessToken,
		"Accept":                   "application/json",
		"User-Agent":               grokCLIUserAgent,
		"x-xai-token-auth":         "xai-grok-cli",
		"x-grok-client-version":    grokCLIVersion,
		"x-grok-client-identifier": grokCLIIdentifier,
		"x-grok-client-mode":       "headless",
	}
	if psd != nil {
		if email, _ := psd["email"].(string); email != "" {
			headers["x-email"] = email
		}
		userID, _ := psd["userId"].(string)
		if userID == "" {
			userID, _ = psd["principalId"].(string)
		}
		if userID != "" {
			headers["x-userid"] = userID
		}
	}
	return headers
}

func (h *ChatHandler) fetchGrokCLILiveCatalog(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData, accessToken string) []LiveModel {
	reqCtx, cancel := context.WithTimeout(ctx, liveCatalogFastHTTP)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, grokCLICatalogURL, nil)
	if err != nil {
		return nil
	}
	for key, value := range grokCLIHeaders(accessToken, connData.ProviderSpecificData) {
		req.Header.Set(key, value)
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		log.Warn("models", "grok-cli catalog fetch failed", "conn", conn.ID, "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warn("models", "grok-cli catalog rejected", "conn", conn.ID, "status", resp.StatusCode)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	return parseGrokCLILiveModels(body)
}

// ---- OpenAI/Anthropic-compatible nodes ----

// fetchCompatibleNodeModels ports upstream fetchCompatibleModelIds: a custom
// node without configured models is asked directly (`GET <baseUrl>/models`),
// which is how a node can publish models its custom-model rows do not cover.
func (h *ChatHandler) fetchCompatibleNodeModels(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData, providerID string) []LiveModel {
	apiKey := connData.APIKey
	if apiKey == "" {
		apiKey = connData.AccessToken
	}
	baseURL, _ := connData.ProviderSpecificData["baseUrl"].(string)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if apiKey == "" || baseURL == "" {
		return nil
	}

	headers := map[string]string{"Content-Type": "application/json"}
	switch {
	case strings.HasPrefix(providerID, "openai-compatible-"):
		headers["Authorization"] = "Bearer " + apiKey
	case strings.HasPrefix(providerID, "anthropic-compatible-"):
		baseURL = strings.TrimSuffix(baseURL, "/messages/models")
		if !strings.HasSuffix(baseURL, "/messages") {
			baseURL += "/models"
		} else {
			baseURL = strings.TrimSuffix(baseURL, "/messages") + "/models"
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		headers["Authorization"] = "Bearer " + apiKey
	default:
		return nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, liveCatalogFastHTTP)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL, nil)
	if err != nil {
		return nil
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		log.Warn("models", "compatible node catalog fetch failed", "conn", conn.ID, "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	return parseOpenAIStyleModelIDs(body)
}

// parseOpenAIStyleModelIDs reads the `data`/`models` array every OpenAI-style
// upstream returns, keeping the first occurrence of each id.
func parseOpenAIStyleModelIDs(body []byte) []LiveModel {
	var payload struct {
		Data   []map[string]any `json:"data"`
		Models []map[string]any `json:"models"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	entries := payload.Data
	if len(entries) == 0 {
		entries = payload.Models
	}

	seen := make(map[string]bool, len(entries))
	out := make([]LiveModel, 0, len(entries))
	for _, entry := range entries {
		id := firstNonEmptyString(entry, "id", "name", "model")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, LiveModel{ID: id})
	}
	return out
}

// parseGrokCLILiveModels mirrors upstream parseGrokCliModels: the catalog may
// arrive as an array, {data|models|results: [...]}, or an id → entry object.
func parseGrokCLILiveModels(body []byte) []LiveModel {
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}

	var entries []any
	var keyedEntry string
	switch value := payload.(type) {
	case []any:
		entries = value
	case map[string]any:
		for _, key := range []string{"data", "models", "results"} {
			if nested, ok := value[key]; ok {
				switch typed := nested.(type) {
				case []any:
					entries = typed
				case map[string]any:
					entries, keyedEntry = objectEntries(typed)
				}
				break
			}
		}
		if entries == nil {
			entries, keyedEntry = objectEntries(value)
		}
	}

	seen := make(map[string]bool, len(entries))
	var out []LiveModel
	for _, raw := range entries {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := firstNonEmptyString(item, "id", "model_id", "modelId", "model", "slug")
		if id == "" {
			id = firstNonEmptyString(item, "name")
		}
		if id == "" {
			id = keyedEntry
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		model := LiveModel{ID: id}
		model.ContextLength = firstPositiveInt(item, "context_length", "contextLength", "context_window", "contextWindow")
		model.MaxOutput = firstPositiveInt(item, "max_output_tokens", "maxOutputTokens")
		if id == grokCLIDefaultModel {
			if model.ContextLength == 0 {
				model.ContextLength = grokCLIDefaultCtxLen
			}
			if model.MaxOutput == 0 {
				model.MaxOutput = grokCLIDefaultMaxOut
			}
		}
		out = append(out, model)
	}
	return out
}

func objectEntries(value map[string]any) ([]any, string) {
	entries := make([]any, 0, len(value))
	onlyKey := ""
	for key, item := range value {
		entries = append(entries, item)
		onlyKey = key
	}
	return entries, onlyKey
}

func firstNonEmptyString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositiveInt(item map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := item[key].(type) {
		case float64:
			if value > 0 {
				return int(value)
			}
		case int:
			if value > 0 {
				return value
			}
		}
	}
	return 0
}
