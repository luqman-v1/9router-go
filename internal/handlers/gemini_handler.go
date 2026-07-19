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

	resp, err := proxy.ForwardGemini(h.Client, cfg, apiKey, string(body), isStream, projectID, modelName)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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
		json.Unmarshal([]byte(rawData), &existing)
		for k, v := range update {
			existing[k] = v
		}
		if result.ProjectID != "" {
			existing["projectId"] = result.ProjectID
		}
		mergedJSON, _ := json.Marshal(existing)
		db.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
			string(mergedJSON), time.Now().UTC().Format(time.RFC3339), connectionID)
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

// handleGeminiStream processes Gemini stream SSE chunks and translates to OpenAI format.
// The stream drops the first SSE line (model metadata), then translates each content block SSE.
func (h *ChatHandler) handleGeminiStream(w http.ResponseWriter, upstream io.Reader, translateResponse bool, metrics *streamMetrics) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	firstLine := true
	geminiState := &translator.GeminiStreamState{}

	return proxy.ScanStream(upstream, func(chunk []byte) {
		if firstLine {
			firstLine = false
			return
		}
		chunkStr := strings.TrimSpace(string(chunk))
		if chunkStr == "" || chunkStr == "data: [DONE]" {
			return
		}
		if !strings.HasPrefix(chunkStr, "data: ") {
			return
		}

		dataStr := strings.TrimPrefix(chunkStr, "data: ")

		// Translate each Gemini SSE chunk to OpenAI SSE chunk
		openaiChunk, err := translator.TranslateGeminiChunkToOpenAI([]byte(dataStr), geminiState)
		if err != nil || openaiChunk == nil {
			return
		}

		if metrics.ttft == 0 {
			metrics.ttft = time.Since(time.Now()).Milliseconds()
		}
		metrics.responseBuf.Write(openaiChunk)
		w.Write(openaiChunk)
		w.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})
}

// handleGeminiNonStream translates a Gemini non-stream response to OpenAI format.
func (h *ChatHandler) handleGeminiNonStream(w http.ResponseWriter, upstream io.Reader) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return err
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	var choices []translator.OpenAIResponseChoice
	for _, c := range geminiResp.Candidates {
		text := ""
		if len(c.Content.Parts) > 0 {
			text = c.Content.Parts[0].Text
		}
		fr := "stop"
		if c.FinishReason != "" && c.FinishReason != "STOP" {
			fr = strings.ToLower(c.FinishReason)
		}
		choices = append(choices, translator.OpenAIResponseChoice{
			Index: 0,
			Message: translator.OpenAIRespMsg{
				Role:    "assistant",
				Content: text,
			},
			FinishReason: &fr,
		})
	}

	usage := &translator.OpenAIUsage{}
	if geminiResp.UsageMetadata != nil {
		usage.PromptTokens = geminiResp.UsageMetadata.PromptTokenCount
		usage.CompletionTokens = geminiResp.UsageMetadata.CandidatesTokenCount
	}

	resp := translator.OpenAIResponse{
		ID:      "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Model:   "gemini",
		Choices: choices,
		Usage:   usage,
	}
	openaiResp, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openaiResp)
	return nil
}
