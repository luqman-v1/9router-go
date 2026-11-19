package chat

// Live catalogue resolution for providers that publish a plain OpenAI-style
// /v1/models feed carrying per-model token limits (nous, openrouter,
// tokenharbor, sumopod, nvidia, …).
//
// The provider's own feed is the authoritative source for a context window:
// nous declares context_length 1000000 for qwen/qwen4-max while any static
// table can only guess from the model family. The dashboard's "Import from
// /models" already reads this feed (writeConnectionModels); this resolver
// brings the same numbers to /v1/models and the combo seat walk, so a client
// never budgets against a guess when the provider already stated the window.
//
// The feed REPLACES the static catalog (upstream LIVE_MODEL_RESOLVERS
// semantics): it lists exactly what the credential may run, including models
// newer than the registry snapshot. On any failure the resolver returns nil
// and the caller keeps the static registry — the same fallback every other
// live resolver has.

import (
	"context"
	"io"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// modelsFeedCatalogURL resolves the catalogue endpoint for a connection: the
// connection's own baseUrl first (a custom endpoint states its own limits),
// then the registry's models-list URL. Deriving from the chat route keeps the
// catalogue on the same prefix the operator configured, exactly like the
// compatible-node path.
func modelsFeedCatalogURL(connData *shared.ConnectionData, providerID string) string {
	baseURL := strings.TrimSpace(connData.BaseURL)
	if baseURL == "" {
		baseURL, _ = connData.ProviderSpecificData["baseUrl"].(string)
	}
	if baseURL != "" {
		return CompatibleNodeModelsURL(baseURL)
	}
	if u := providers.ModelsListURL(providerID); u != "" {
		return u
	}
	// The registry's modelsFetcher is the same feed the dashboard's "Suggested
	// free models" reads (opencode zen/go). It states limits as limit.context /
	// limit.output, which the parser below understands.
	if meta, ok := providers.GetProviderMeta(providerID); ok && meta.ModelsFetcher != nil {
		return meta.ModelsFetcher.URL
	}
	return ""
}

// providerHasModelsFetcher reports whether the registry declares a models feed
// for the provider (the dashboard's "Suggested free models" source).
func providerHasModelsFetcher(providerID string) bool {
	meta, ok := providers.GetProviderMeta(providerID)
	return ok && meta.ModelsFetcher != nil && meta.ModelsFetcher.URL != ""
}

// fetchModelsFeedLiveCatalog reads the provider's OpenAI-style models feed.
func (h *ChatHandler) fetchModelsFeedLiveCatalog(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData, providerID, token string) []LiveModel {
	feedURL := modelsFeedCatalogURL(connData, providerID)
	if feedURL == "" || token == "" {
		return nil
	}

	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	for k, v := range providers.ModelsListHeaders(providerID) {
		headers[k] = v
	}

	reqCtx, cancel := context.WithTimeout(ctx, liveCatalogFastHTTP)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil
	}
	for key, value := range handlerutil.WithDefaultUserAgent(headers) {
		req.Header.Set(key, value)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		log.Warn("models", "models feed catalog fetch failed", "provider", providerID, "conn", conn.ID, "error", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	return parseModelsFeedLiveModels(body)
}

// parseModelsFeedLiveModels reads the data/models array, keeping each entry's
// declared limits. Both spellings are accepted: OpenRouter-style feeds use
// snake_case (context_length / top_provider.max_completion_tokens), normalized
// catalogues use camelCase (contextLength / maxOutputTokens). An entry that
// states a limit marks it authoritative so it outranks the static tables;
// an entry that states none keeps the static resolution.
func parseModelsFeedLiveModels(body []byte) []LiveModel {
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

		ctxLen := firstPositiveInt(entry, "context_length", "contextLength", "context_window", "contextWindow")
		maxOut := firstPositiveInt(entry, "max_completion_tokens", "max_output_tokens", "maxOutputTokens", "maxOutput")
		if top, ok := entry["top_provider"].(map[string]any); ok {
			if ctxLen == 0 {
				ctxLen = firstPositiveInt(top, "context_length", "contextLength")
			}
			if maxOut == 0 {
				maxOut = firstPositiveInt(top, "max_completion_tokens", "maxOutputTokens")
			}
		}
		// OpenCode's zen/go feeds state limits as limit.context / limit.output
		// (the models.dev shape), not context_length.
		if lim, ok := entry["limit"].(map[string]any); ok {
			if ctxLen == 0 {
				ctxLen = firstPositiveInt(lim, "context", "context_length", "contextLength")
			}
			if maxOut == 0 {
				maxOut = firstPositiveInt(lim, "output", "max_output_tokens", "maxOutputTokens")
			}
		}

		out = append(out, LiveModel{
			ID:                  id,
			Name:                firstNonEmptyString(entry, "name"),
			ContextLength:       ctxLen,
			MaxOutput:           maxOut,
			LimitsAuthoritative: ctxLen > 0 || maxOut > 0,
		})
	}
	return out
}
