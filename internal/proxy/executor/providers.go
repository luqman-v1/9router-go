package executor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
)

// ---- Provider-specific executors ----

// ForwardGrokCLI forwards to grok-cli using Responses API format.
// Transforms Chat Completions body → Responses API body before forwarding.
func ForwardGrokCLI(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform body: %w", err)
	}
	resp, err := proxy.ForwardGrokCLI(req.Client, req.Config, req.APIKey, transformedBody, req.IsStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return handleCodexStream(w, resp.Body)
}

// ForwardCodex forwards to codex using Responses API format.
// Transforms Chat Completions body → Responses API body before forwarding.
func ForwardCodex(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform body: %w", err)
	}
	resp, err := proxy.ForwardCodex(req.Client, req.Config, req.APIKey, transformedBody, req.IsStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return handleCodexStream(w, resp.Body)
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
// Injects stream_options and generates HMAC headers before forwarding.
func ForwardIflow(w http.ResponseWriter, req *Request) error {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	if req.IsStream {
		reqMap["stream"] = true
		if _, ok := reqMap["stream_options"]; !ok {
			reqMap["stream_options"] = map[string]interface{}{"include_usage": true}
		}
	}
	reqBody, _ := json.Marshal(reqMap)

	// HMAC-SHA256 signature
	sessionID := "session-" + uuid.New().String()
	timestamp := time.Now().UnixMilli()
	userAgent := "iFlow-Cli"
	payload := userAgent + ":" + sessionID + ":" + strconv.FormatInt(timestamp, 10)

	mac := hmac.New(sha256.New, []byte(req.APIKey))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	extraHeaders := map[string]string{
		"User-Agent":       userAgent,
		"session-id":       sessionID,
		"x-iflow-timestamp": strconv.FormatInt(timestamp, 10),
		"x-iflow-signature": signature,
	}
	resp, err := proxy.ForwardIflow(req.Client, req.Config, req.APIKey, reqBody, req.IsStream, extraHeaders)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if req.IsStream {
		return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
	}
	return jsonResponse(w, resp.Body, req.TranslateResp)
}

// ForwardKimchi forwards to kimchi with Anthropic field stripping.
// Cleans Anthropic-specific fields from body before forwarding.
func ForwardKimchi(w http.ResponseWriter, req *Request) error {
	var reqBody map[string]any
	if err := json.Unmarshal(req.Body, &reqBody); err != nil {
		return fmt.Errorf("parse request body: %w", err)
	}

	CleanKimchiBody(reqBody)

	cleanedBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal cleaned body: %w", err)
	}

	resp, err := proxy.ForwardKimchi(req.Client, req.Config, req.APIKey, cleanedBody, req.IsStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if req.IsStream {
		return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
	}
	return jsonResponse(w, resp.Body, req.TranslateResp)
}

// ForwardKiro forwards to kiro with AWS EventStream response handling.
// Uses EventStream binary parsing instead of standard SSE.
func ForwardKiro(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardKiro(req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return handleKiroStream(w, resp.Body)
}

// ForwardAzure forwards to Azure OpenAI with dynamic URL from env vars.
func ForwardAzure(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &oreq)
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

	r, err := http.NewRequest("POST", url, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("api-key", req.APIKey)
	if req.IsStream {
		r.Header.Set("Accept", "text/event-stream")
	}

	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if req.IsStream {
		return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
	}
	return jsonResponse(w, resp.Body, req.TranslateResp)
}

// ForwardCommandcode forwards to CommandCode with NDJSON→SSE translation.
func ForwardCommandcode(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &oreq)

	// Force stream=true — commandcode always uses NDJSON
	var reqMap map[string]interface{}
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	reqMap["stream"] = true
	reqBody, _ := json.Marshal(reqMap)

	// Build request with custom headers (not using proxy.ForwardCommandcode)
	r, err := http.NewRequest("POST", req.Config.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+req.APIKey)
	r.Header.Set("x-session-id", uuid.New().String())
	r.Header.Set("x-command-code-version", "0.25.7")
	r.Header.Set("x-cli-environment", "cli")
	r.Header.Set("Accept", "text/event-stream")

	resp, err := req.Client.Do(r)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	return handleCommandcodeStream(w, resp.Body, oreq.Model)
}
