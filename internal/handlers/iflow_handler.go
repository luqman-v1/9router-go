package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/providers"
)

// forwardIflowRequest handles iFlow API requests with HMAC-SHA256 signature auth.
func (h *ChatHandler) forwardIflowRequest(
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	// Parse body to inject stream_options for streaming requests
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	if isStream {
		reqMap["stream"] = true
		if _, ok := reqMap["stream_options"]; !ok {
			reqMap["stream_options"] = map[string]interface{}{"include_usage": true}
		}
	}
	reqBody, _ := json.Marshal(reqMap)

	// Generate HMAC-SHA256 signature
	sessionID := "session-" + uuid.New().String()
	timestamp := time.Now().UnixMilli()
	userAgent := "iFlow-Cli"
	payload := userAgent + ":" + sessionID + ":" + strconv.FormatInt(timestamp, 10)

	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	// Build request
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("session-id", sessionID)
	req.Header.Set("x-iflow-timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("x-iflow-signature", signature)

	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}

	// Send request
	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	start := time.Now()
	if metrics == nil {
		metrics = &streamMetrics{}
	}
	if isStream {
		return h.handleStreamResponse(w, resp.Body, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(w, resp.Body, translateResponse)
}
