package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// ForwardGemini sends an OpenAI-format request to a Gemini-native endpoint.
// projectID is non-empty for antigravity (cloudcode-pa.googleapis.com).
func ForwardGemini(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey, bodyStr string, isStream bool, projectID, modelName string) (*http.Response, error) {
	body := []byte(bodyStr)

	// Translate OpenAI → Gemini native
	geminiBody, err := translator.TranslateOpenAIToGemini(body)
	if err != nil {
		return nil, fmt.Errorf("translate to Gemini: %w", err)
	}

	// Wrap for antigravity if needed
	sendBody := geminiBody
	if projectID != "" {
		wrapped, err := translator.WrapForAntigravity(geminiBody, projectID, modelName)
		if err != nil {
			return nil, fmt.Errorf("wrap for antigravity: %w", err)
		}
		sendBody = wrapped
	}

	// Build URL
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if idx := strings.Index(baseURL, "/v1beta/openai"); idx != -1 {
		baseURL = baseURL[:idx]
	} else if idx := strings.Index(baseURL, "/v1/"); idx != -1 {
		baseURL = baseURL[:idx]
	}

	action := "generateContent"
	if isStream {
		action = "streamGenerateContent?alt=sse"
	}
	var requestURL string
	if projectID != "" {
		requestURL = fmt.Sprintf("%s/v1internal:%s", baseURL, action)
	} else {
		requestURL = fmt.Sprintf("%s/v1beta/models/%s:%s", baseURL, modelName, action)
	}

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + apiKey,
		"User-Agent":    "antigravity/ide/2.1.1 darwin/arm64",
	}

	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(sendBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("upstream returned %d and body read failed: %w", resp.StatusCode, readErr)
		}
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}
	return resp, nil
}
