package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// forwardAzureRequest handles Azure OpenAI with dynamic URL from connection data.
func (h *ChatHandler) forwardAzureRequest(
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	var oreq struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &oreq)
	modelName := oreq.Model
	if modelName == "" {
		modelName = "gpt-4"
	}

	endpoint := os.Getenv("AZURE_ENDPOINT")
	apiVersion := os.Getenv("AZURE_API_VERSION")
	if apiVersion == "" {
		apiVersion = "2024-10-01-preview"
	}
	deployment := os.Getenv("AZURE_DEPLOYMENT")
	if deployment == "" {
		deployment = modelName
	}
	if endpoint == "" {
		endpoint = "https://api.openai.com"
	}

	baseURL := strings.TrimRight(endpoint, "/")
	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		baseURL, deployment, apiVersion)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", apiKey)
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
		return h.handleStreamResponse(w, resp.Body, translateResponse, time.Now(), metrics)
	}
	return h.handleJSONResponse(w, resp.Body, translateResponse)
}
