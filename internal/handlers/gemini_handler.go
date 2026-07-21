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

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/oauth"
	"9router/proxy/internal/translator"
)

// forwardGeminiNativeRequest handles forwarding for gemini-native providers (antigravity).
func (h *ChatHandler) forwardGeminiNativeRequest(
	w http.ResponseWriter,
	provider string,
	cfg *providers.ProviderConfig,
	apiKey string,
	connectionID string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	// Extract model
	var reqMeta struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqMeta); err != nil {
		log.Printf("[gemini] failed to parse model from request body: %v", err)
	}
	modelName := reqMeta.Model
	if modelName == "" {
		modelName = "gemini-3-flash"
	}

	// OAuth refresh for providers using Gemini-native format (antigravity, etc)
	var projectID string
	refreshedKey, pid, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
	if err != nil {
		log.Printf("[gemini] WARNING: token refresh error for connection %s: %v (continuing with existing/stale token)", connectionID, err)
	} else {
		apiKey = refreshedKey
		projectID = pid
	}

	if (provider == "antigravity" || provider == "gemini-cli") && projectID == "" {
		if pid := fetchAntigravityProjectID(h.Client, apiKey); pid != "" {
			projectID = pid
			go func() {
				if _, err := h.Repo.RawDB().Exec("UPDATE providerConnections SET data = json_set(data, '$.projectId', ?) WHERE id = ?", pid, connectionID); err != nil {
					log.Printf("[gemini] failed to update projectId for connection %s: %v", connectionID, err)
				}
			}()
		} else {
			// Access token might be invalid/expired, force refresh OAuth token and retry
			log.Printf("[gemini] fetchAntigravityProjectID failed with current token, force refreshing OAuth token for connection %s...", connectionID)
			refreshedKey, pid2, err2 := h.forceRefreshOAuthToken(connectionID)
			if err2 == nil && refreshedKey != "" {
				apiKey = refreshedKey
				if pid2 != "" {
					projectID = pid2
				} else if pid := fetchAntigravityProjectID(h.Client, apiKey); pid != "" {
					projectID = pid
					go func() {
					if _, err := h.Repo.RawDB().Exec("UPDATE providerConnections SET data = json_set(data, '$.projectId', ?) WHERE id = ?", pid, connectionID); err != nil {
						log.Printf("[gemini] failed to update projectId for connection %s: %v", connectionID, err)
					}
				}()
				}
			}
		}
	}

	if projectID == "" {
		// Fallback to OpenAI compatibility endpoint if project ID is missing
		log.Printf("[gemini] projectID missing for %s, falling back to OpenAI format", provider)
		return h.forwardRequest(w, cfg, apiKey, body, isStream, translateResponse, metrics)
	}

	resp, err := proxy.ForwardGemini(h.Client, cfg, apiKey, string(body), isStream, projectID, modelName)
	if err != nil {
		return fmt.Errorf("ForwardGemini (%s/%s): %w", provider, modelName, err)
	}
	defer resp.Body.Close()

	// Handle response — antigravity wraps non-streaming response in {"response": {...}}
	// For streaming (SSE), the events are NOT wrapped — pipe directly.
	if projectID != "" && !isStream {
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("[gemini] failed to read non-stream response body: %v", err)
			return fmt.Errorf("read gemini response body: %w", err)
		}
		unwrapped := translator.UnwrapAntigravityResponse(raw)
		return h.handleGeminiNonStream(w, bytes.NewReader(unwrapped), translateResponse)
	}
	if isStream {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			return h.handleGeminiNonStream(w, resp.Body, translateResponse)
		}
		resp.Body = proxy.NewStallReader(resp.Body, 0, provider)
		return h.handleGeminiStream(w, resp.Body, translateResponse, metrics)
	}
	return h.handleGeminiNonStream(w, resp.Body, translateResponse)
}

// refreshOAuthTokenIfExpired checks and refreshes OAuth token for any provider with refreshToken.
func (h *ChatHandler) refreshOAuthTokenIfExpired(connectionID, currentToken string) (string, string, error) {
	if connectionID == "" {
		return currentToken, "", nil
	}

	db := h.Repo.RawDB()
	row := db.QueryRow("SELECT provider, data FROM providerConnections WHERE id = ?", connectionID)
	var provider string
	var rawData string
	if err := row.Scan(&provider, &rawData); err != nil {
		return currentToken, "", nil
	}

	var connMap map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &connMap); err != nil {
		return currentToken, "", nil
	}

	oauthData := providers.ParseOAuthConnection(connMap)
	projectID := ""
	if oauthData != nil {
		projectID = oauthData.ProjectID
	}
	if oauthData == nil || oauthData.RefreshToken == "" || !oauthData.IsExpired() {
		return currentToken, projectID, nil
	}

	// Try per-provider OAuth refresher first
	if refresher := oauth.Get(provider); refresher != nil {
		log.Printf("[oauth] token expired for %s/%s, using custom refresher...", provider, projectID)
		result, err := refresher(&oauth.Params{
			Client:       h.Client,
			Provider:     provider,
			RefreshToken: oauthData.RefreshToken,
			AccessToken:  currentToken,
		})
		if err != nil {
			return currentToken, projectID, fmt.Errorf("OAuth refresh for %s: %w", provider, err)
		}
		update := oauth.BuildConnectionUpdate(result)
		var existing map[string]interface{}
		if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
			log.Printf("[oauth] failed to unmarshal existing connection data for %s: %v", connectionID, err)
			existing = make(map[string]interface{})
		}
		for k, v := range update {
			existing[k] = v
		}
		if result.ProjectID != "" {
			existing["projectId"] = result.ProjectID
		}
		mergedJSON, err := json.Marshal(existing)
		if err != nil {
			log.Printf("[oauth] failed to marshal updated connection data for %s: %v", connectionID, err)
		} else {
			db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
				string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
		}
		log.Printf("[oauth] token refreshed for %s/%s", provider, result.ProjectID)
		return result.AccessToken, result.ProjectID, nil
	}

	// Fall back to standard OAuth2
	cfg, ok := providers.KnownOAuthConfigs[provider]
	if !ok {
		return currentToken, projectID, nil
	}

	log.Printf("[oauth] token expired for %s/%s, refreshing (standard)...", provider, projectID)
	tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
	if err != nil {
		return currentToken, projectID, fmt.Errorf("OAuth refresh for %s: %w", provider, err)
	}

	update := tokenResp.BuildConnectionUpdate()
	var existing map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
		log.Printf("[oauth] failed to unmarshal existing connection data for %s: %v", connectionID, err)
		existing = make(map[string]interface{})
	}
	for k, v := range update {
		existing[k] = v
	}
	mergedJSON, err := json.Marshal(existing)
	if err != nil {
		log.Printf("[oauth] failed to marshal updated connection data for %s: %v", connectionID, err)
	} else {
		db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
			string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
	}

	log.Printf("[oauth] token refreshed for %s/%s", provider, projectID)
	return tokenResp.AccessToken, projectID, nil
}

// forceRefreshOAuthToken unconditionally refreshes the OAuth token for a connection ID.
func (h *ChatHandler) forceRefreshOAuthToken(connectionID string) (string, string, error) {
	if connectionID == "" {
		return "", "", nil
	}

	db := h.Repo.RawDB()
	row := db.QueryRow("SELECT provider, data FROM providerConnections WHERE id = ?", connectionID)
	var provider string
	var rawData string
	if err := row.Scan(&provider, &rawData); err != nil {
		return "", "", err
	}

	var connMap map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &connMap); err != nil {
		return "", "", err
	}

	oauthData := providers.ParseOAuthConnection(connMap)
	if oauthData == nil || oauthData.RefreshToken == "" {
		return "", "", fmt.Errorf("no refresh token available")
	}

	// Try per-provider OAuth refresher first
	if refresher := oauth.Get(provider); refresher != nil {
		log.Printf("[oauth] force refreshing token for %s...", provider)
		result, err := refresher(&oauth.Params{
			Client:       h.Client,
			Provider:     provider,
			RefreshToken: oauthData.RefreshToken,
		})
		if err == nil && result != nil {
			var existing map[string]interface{}
			if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
				log.Printf("[oauth] failed to unmarshal connection data for %s: %v", connectionID, err)
				existing = make(map[string]interface{})
			}
			existing["accessToken"] = result.AccessToken
			if result.ProjectID != "" {
				existing["projectId"] = result.ProjectID
			}
			existing["expiresAt"] = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339)
			mergedJSON, err := json.Marshal(existing)
			if err != nil {
				log.Printf("[oauth] failed to marshal connection data for %s: %v", connectionID, err)
			} else {
				db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
					string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
			}
			return result.AccessToken, result.ProjectID, nil
		}
	}

	// Fall back to standard OAuth2
	cfg, ok := providers.KnownOAuthConfigs[provider]
	if !ok {
		return "", "", fmt.Errorf("no OAuth config for %s", provider)
	}

	log.Printf("[oauth] force refreshing token for %s (standard)...", provider)
	tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
	if err != nil {
		return "", "", fmt.Errorf("OAuth refresh for %s: %w", provider, err)
	}

	update := tokenResp.BuildConnectionUpdate()
	var existing map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
		log.Printf("[oauth] failed to unmarshal connection data for %s: %v", connectionID, err)
		existing = make(map[string]interface{})
	}
	for k, v := range update {
		existing[k] = v
	}
	mergedJSON, err := json.Marshal(existing)
	if err != nil {
		log.Printf("[oauth] failed to marshal connection data for %s: %v", connectionID, err)
	} else {
		db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
			string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
	}

	pid := ""
	if v, ok := existing["projectId"].(string); ok {
		pid = v
	}
	return tokenResp.AccessToken, pid, nil
}

// handleGeminiStream processes Gemini stream SSE chunks and translates to OpenAI format.
// The stream drops the first SSE line (model metadata), then translates each content block SSE.
func (h *ChatHandler) handleGeminiStream(w http.ResponseWriter, upstream io.Reader, translateResponse bool, metrics *streamMetrics) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	geminiState := &translator.GeminiStreamState{}
	start := time.Now()

	return proxy.ScanStream(upstream, func(chunk []byte) {
		chunkStr := strings.TrimSpace(string(chunk))
		if chunkStr == "" || chunkStr == "[DONE]" {
			return
		}

		// Unwrap antigravity envelope if present (SSE chunks may be wrapped as {"response": {...}})
		unwrapped := translator.UnwrapAntigravityResponse(chunk)

		// Translate each Gemini SSE chunk to OpenAI SSE chunk
		openaiChunk, err := translator.TranslateGeminiChunkToOpenAI(unwrapped, geminiState)
		if err != nil {
			log.Printf("[gemini_stream_error] TranslateGeminiChunkToOpenAI error: %v", err)
			return
		}
		if openaiChunk == nil {
			return
		}

		if metrics.ttft == 0 {
			metrics.ttft = time.Since(start).Milliseconds()
		}
		metrics.responseBuf.Write(openaiChunk)

		if translateResponse {
			// Translate OpenAI → Claude (Anthropic) format
			claudeChunk, tErr := translator.TranslateOpenAIToClaudeStream(openaiChunk)
			if tErr != nil {
				log.Printf("[gemini_claude_stream_error] TranslateOpenAIToClaudeStream error: %v | openaiChunk: %s", tErr, string(openaiChunk))
				return
			}
			if claudeChunk == nil {
				return
			}
			w.Write(claudeChunk)
		} else {
			w.Write(openaiChunk)
			w.Write([]byte("\n\n"))
		}
		if flusher != nil {
			flusher.Flush()
		}
	})
}

// handleGeminiNonStream translates a Gemini non-stream response to OpenAI format,
// and then to Claude format if translateResponse is true.
func (h *ChatHandler) handleGeminiNonStream(w http.ResponseWriter, upstream io.Reader, translateResp bool) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read gemini response body: %w", err)
	}

	// Use the full translator which handles tool calls, thinking, etc.
	openaiResp, usage, err := translator.TranslateGeminiResponseToOpenAI(body)
	if err == nil && usage != nil {
		translator.SetLastUsage(usage)
	}
	if err != nil {
		// Fallback: write raw body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	if translateResp {
		// OpenAI → Claude (Anthropic) format
		claudeResp, claudeUsage, tErr := translator.TranslateOpenAIToClaude(openaiResp)
		if tErr == nil && claudeUsage != nil {
			translator.SetLastUsage(claudeUsage)
		}
		if tErr != nil || claudeResp == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(openaiResp)
			return nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(claudeResp)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openaiResp)
	return nil
}
