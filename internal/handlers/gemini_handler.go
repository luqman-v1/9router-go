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
	"9router/proxy/internal/translator"
)

// forwardGeminiNativeRequest handles forwarding for gemini-native providers (antigravity).
func (h *ChatHandler) forwardGeminiNativeRequest(
	w http.ResponseWriter,
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
	_ = json.Unmarshal(body, &reqMeta)
	modelName := reqMeta.Model
	if modelName == "" {
		modelName = "gemini-3-flash"
	}

	// OAuth refresh for providers using Gemini-native format (antigravity, etc)
	var projectID string
	refreshedKey, pid, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
	if err != nil {
		log.Printf("[gemini] token refresh error: %v", err)
	} else {
		apiKey = refreshedKey
		projectID = pid
	}

	// Translate OpenAI → Gemini native format
	geminiBody, err := translator.TranslateOpenAIToGemini(body)
	if err != nil {
		return fmt.Errorf("translate to Gemini: %w", err)
	}

	// Wrap for antigravity if needed
	sendBody := geminiBody
	if projectID != "" {
		wrapped, err := translator.WrapForAntigravity(geminiBody, projectID, modelName)
		if err != nil {
			return fmt.Errorf("wrap for antigravity: %w", err)
		}
		sendBody = wrapped
	}

	// Build URL
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	action := "generateContent"
	if isStream {
		action = "streamGenerateContent?alt=sse"
	}

	var requestURL string
	if projectID != "" {
		requestURL = fmt.Sprintf("%s/v1internal:%s", baseURL, action)
	} else {
		requestURL = fmt.Sprintf("%s/%s:%s", baseURL, modelName, action)
	}

	// Send request
	req, err := http.NewRequest("POST", requestURL, bytes.NewReader(sendBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "antigravity/ide/2.1.1 darwin/arm64")

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	// Handle response — antigravity wraps response in {"response": {...}}
	if projectID != "" {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		unwrapped := translator.UnwrapAntigravityResponse(raw)
		if isStream {
			return h.handleGeminiStream(w, io.NopCloser(bytes.NewReader(unwrapped)), translateResponse, metrics)
		}
		return h.handleGeminiNonStream(w, bytes.NewReader(unwrapped))
	}
	if isStream {
		return h.handleGeminiStream(w, resp.Body, translateResponse, metrics)
	}
	return h.handleGeminiNonStream(w, resp.Body)
}

// refreshAntigravityToken checks token expiry and refreshes via Google OAuth.
// Returns (refreshedAccessToken, projectID, error).
func (h *ChatHandler) refreshAntigravityToken(connectionID, currentToken string) (string, string, error) {
	if connectionID == "" {
		return currentToken, "", nil
	}

	db := h.Repo.RawDB()
	row := db.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connectionID)
	var rawData string
	if err := row.Scan(&rawData); err != nil {
		return currentToken, "", fmt.Errorf("fetch connection: %w", err)
	}

	var connMap map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &connMap); err != nil {
		return currentToken, "", fmt.Errorf("parse connection data: %w", err)
	}

	oauthData := providers.ParseOAuthConnection(connMap)
	projectID := ""
	if oauthData != nil {
		projectID = oauthData.ProjectID
	}

	if oauthData == nil || oauthData.RefreshToken == "" || !oauthData.IsExpired() {
		return currentToken, projectID, nil
	}

	log.Printf("[gemini] OAuth token expired, refreshing via Google OAuth...")

	cfg := providers.KnownOAuthConfigs["antigravity"]
	tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
	if err != nil {
		return currentToken, projectID, fmt.Errorf("OAuth refresh: %w", err)
	}

	// Update DB
	update := tokenResp.BuildConnectionUpdate()
	var existing map[string]interface{}
	json.Unmarshal([]byte(rawData), &existing)
	for k, v := range update {
		existing[k] = v
	}
	mergedJSON, _ := json.Marshal(existing)
	db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
		string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)

	log.Printf("[gemini] OAuth token refreshed successfully")
	return tokenResp.AccessToken, projectID, nil
}

func (h *ChatHandler) handleGeminiStream(w http.ResponseWriter, upstream io.Reader, translateResponse bool, metrics *streamMetrics) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	state := &translator.GeminiStreamState{}
	buf := make([]byte, 64*1024)
	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			data := buf[:n]
			for _, line := range strings.Split(string(data), "\n\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if strings.HasPrefix(line, "data: ") {
					line = line[6:]
				}
				if line == "[DONE]" {
					continue
				}
				out, tErr := translator.TranslateGeminiChunkToOpenAI([]byte(line), state)
				if tErr != nil {
					log.Printf("[gemini] translate chunk error: %v", tErr)
					continue
				}
				if out != nil {
					if metrics != nil && metrics.ttft == 0 {
						metrics.ttft = time.Now().UnixMilli()
					}
					metrics.responseBuf.Write(out)
					w.Write(out)
					if flusher != nil {
						flusher.Flush()
					}
				}
			}
		}
		if err != nil {
			break
		}
	}

	if !translateResponse {
		w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	if state.Usage != nil {
		translator.SetLastUsage(state.Usage)
	}
	return nil
}

func (h *ChatHandler) handleGeminiNonStream(w http.ResponseWriter, upstream io.Reader) error {
	geminiResp, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	openaiResp, err := translator.TranslateGeminiResponseToOpenAI(geminiResp)
	if err != nil {
		return fmt.Errorf("translate Gemini response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openaiResp)
	return nil
}

// refreshOAuthTokenIfExpired checks and refreshes OAuth token for any provider with refreshToken.
// Returns (refreshedAccessToken, projectID, error).
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

	cfg, ok := providers.KnownOAuthConfigs[provider]
	if !ok {
		return currentToken, projectID, nil
	}

	log.Printf("[oauth] token expired for %s/%s, refreshing...", provider, projectID)
	tokenResp, err := providers.RefreshToken(cfg, oauthData.RefreshToken)
	if err != nil {
		return currentToken, projectID, fmt.Errorf("OAuth refresh for %s: %w", provider, err)
	}

	update := tokenResp.BuildConnectionUpdate()
	var existing map[string]interface{}
	json.Unmarshal([]byte(rawData), &existing)
	for k, v := range update {
		existing[k] = v
	}
	mergedJSON, _ := json.Marshal(existing)
	db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
		string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)

	log.Printf("[oauth] token refreshed for %s/%s", provider, projectID)
	return tokenResp.AccessToken, projectID, nil
}
