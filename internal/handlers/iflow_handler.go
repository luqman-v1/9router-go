package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
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

	// Forward via proxy
	extraHeaders := map[string]string{
		"User-Agent":       userAgent,
		"session-id":       sessionID,
		"x-iflow-timestamp": strconv.FormatInt(timestamp, 10),
		"x-iflow-signature": signature,
	}
	resp, err := proxy.ForwardIflow(h.Client, cfg, apiKey, reqBody, isStream, extraHeaders)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	start := time.Now()
	if metrics == nil {
		metrics = &streamMetrics{}
	}
	if isStream {
		return h.handleStreamResponse(w, resp.Body, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(w, resp.Body, translateResponse)
}
