package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"9router/proxy/internal/log"
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
	ctx context.Context,
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
		log.Warn("gemini", "parse model failed", "error", err)
	}
	modelName := reqMeta.Model
	if modelName == "" {
		modelName = "gemini-3-flash"
	}

	// OAuth refresh for providers using Gemini-native format (antigravity, etc)
	var projectID string
	refreshedKey, pid, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
	if err != nil {
		log.Warn("gemini", "token refresh error", "conn", connectionID, "error", err)
	} else {
		apiKey = refreshedKey
		projectID = pid
	}

	if (provider == "antigravity" || provider == "gemini-cli") && projectID == "" {
		if pid := fetchAntigravityProjectID(ctx, h.Client, apiKey); pid != "" {
			projectID = pid
			go func() {
				if _, err := h.Repo.RawDB().Exec("UPDATE providerConnections SET data = json_set(data, '$.projectId', ?) WHERE id = ?", pid, connectionID); err != nil {
					log.Warn("gemini", "update projectId failed", "conn", connectionID, "error", err)
				}
			}()
		} else {
			// Access token might be invalid/expired, force refresh OAuth token and retry
			log.Info("gemini", "force refresh OAuth token", "conn", connectionID)
			refreshedKey, pid2, err2 := h.forceRefreshOAuthToken(connectionID)
			if err2 == nil && refreshedKey != "" {
				apiKey = refreshedKey
				if pid2 != "" {
					projectID = pid2
				} else if pid := fetchAntigravityProjectID(ctx, h.Client, apiKey); pid != "" {
					projectID = pid
					go func() {
					if _, err := h.Repo.RawDB().Exec("UPDATE providerConnections SET data = json_set(data, '$.projectId', ?) WHERE id = ?", pid, connectionID); err != nil {
						log.Warn("gemini", "update projectId failed", "conn", connectionID, "error", err)
					}
				}()
				}
			}
		}
	}

	if projectID == "" {
		// Fallback to OpenAI compatibility endpoint if project ID is missing
		log.Info("gemini", "no projectID, fallback to OpenAI", "provider", provider)
		return h.forwardRequest(ctx, w, cfg, apiKey, body, isStream, translateResponse, metrics)
	}

	resp, err := proxy.ForwardGemini(ctx, h.Client, cfg, apiKey, string(body), isStream, projectID, modelName)
	if err != nil {
		return fmt.Errorf("ForwardGemini (%s/%s): %w", provider, modelName, err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	// Handle response — antigravity wraps non-streaming response in {"response": {...}}
	// For streaming (SSE), the events are NOT wrapped — pipe directly.
	if projectID != "" && !isStream {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Error("gemini", "read response body failed", "error", err)
			return fmt.Errorf("read gemini response body: %w", err)
		}
		unwrapped := translator.UnwrapAntigravityResponse(raw)
		return h.handleGeminiNonStream(ctx, w, bytes.NewReader(unwrapped), translateResponse, metrics)
	}
	if isStream {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			return h.handleGeminiNonStream(ctx, w, resp.Body, translateResponse, metrics)
		}
		stallReader := proxy.NewStallReader(resp.Body, 0, provider)
		bodyCloser = stallReader
		return h.handleGeminiStream(w, stallReader, translateResponse, metrics)
	}
	return h.handleGeminiNonStream(ctx, w, resp.Body, translateResponse, metrics)
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
		log.Info("oauth", "token expired, custom refresh", "provider", provider, "project", projectID)
		result, err := refresher(context.Background(), &oauth.Params{
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
			log.Error("oauth", "unmarshal connection data failed", "conn", connectionID, "error", err)
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
			log.Error("oauth", "marshal connection data failed", "conn", connectionID, "error", err)
		} else {
			db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
				string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
		}
		log.Info("oauth", "token refreshed", "provider", provider, "project", result.ProjectID)
		return result.AccessToken, result.ProjectID, nil
	}

	// Fall back to standard OAuth2
	cfg, ok := providers.KnownOAuthConfigs[provider]
	if !ok {
		return currentToken, projectID, nil
	}

	log.Info("oauth", "token expired, standard refresh", "provider", provider, "project", projectID)
	tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
	if err != nil {
		return currentToken, projectID, fmt.Errorf("OAuth refresh for %s: %w", provider, err)
	}

	update := tokenResp.BuildConnectionUpdate()
	var existing map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
		log.Error("oauth", "unmarshal connection data failed", "conn", connectionID, "error", err)
		existing = make(map[string]interface{})
	}
	for k, v := range update {
		existing[k] = v
	}
	mergedJSON, err := json.Marshal(existing)
	if err != nil {
		log.Error("oauth", "marshal connection data failed", "conn", connectionID, "error", err)
	} else {
		db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
			string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
	}

	log.Info("oauth", "token refreshed", "provider", provider, "project", projectID)
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
		log.Info("oauth", "force refresh", "provider", provider)
		result, err := refresher(context.Background(), &oauth.Params{
			Client:       h.Client,
			Provider:     provider,
			RefreshToken: oauthData.RefreshToken,
		})
		if err == nil && result != nil {
			var existing map[string]interface{}
			if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
				log.Error("oauth", "unmarshal conn data failed", "conn", connectionID, "error", err)
				existing = make(map[string]interface{})
			}
			existing["accessToken"] = result.AccessToken
			if result.ProjectID != "" {
				existing["projectId"] = result.ProjectID
			}
			existing["expiresAt"] = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339)
			mergedJSON, err := json.Marshal(existing)
			if err != nil {
				log.Error("oauth", "marshal conn data failed", "conn", connectionID, "error", err)
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

	log.Info("oauth", "force refresh (standard)", "provider", provider)
	tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
	if err != nil {
		return "", "", fmt.Errorf("OAuth refresh for %s: %w", provider, err)
	}

	update := tokenResp.BuildConnectionUpdate()
	var existing map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
		log.Error("oauth", "unmarshal conn data failed", "conn", connectionID, "error", err)
		existing = make(map[string]interface{})
	}
	for k, v := range update {
		existing[k] = v
	}
	mergedJSON, err := json.Marshal(existing)
	if err != nil {
		log.Error("oauth", "marshal conn data failed", "conn", connectionID, "error", err)
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
	flusher := proxy.WriteSSEHeaders(w)
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
			log.Error("gemini", "chunk translate error", "error", err)
			return
		}
		if openaiChunk == nil {
			return
		}

		if metrics.TTFT == 0 {
			metrics.TTFT = time.Since(start).Milliseconds()
		}
		metrics.ResponseBuf.Write(openaiChunk)

		if translateResponse {
			// openaiChunk may have multiple SSE lines -- split and translate each
			for _, sse := range strings.Split(string(openaiChunk), "\n") {
				sse = strings.TrimSpace(sse)
				if !strings.HasPrefix(sse, "data: ") {
					continue
				}
				claudeChunk, tErr := translator.TranslateOpenAIToClaudeStream([]byte(strings.TrimPrefix(sse, "data: ")))
				if tErr != nil {
					log.Error("gemini", "claude translate error", "error", tErr)
					continue
				}
				if claudeChunk == nil {
					continue
				}
				w.Write(claudeChunk)
			}
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
func (h *ChatHandler) handleGeminiNonStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translateResp bool, metrics *streamMetrics) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read gemini response body: %w", err)
	}

	if metrics != nil {
		metrics.ResponseBuf.Write(body)
	}

	// Use the full translator which handles tool calls, thinking, etc.
	openaiResp, usage, err := translator.TranslateGeminiResponseToOpenAI(body)
	if err == nil && usage != nil {
		translator.SetUsage(ctx, usage)
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
			translator.SetUsage(ctx, claudeUsage)
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
