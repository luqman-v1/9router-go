package dashboard

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/executor"
)

// validateProbeTimeout mirrors upstream's AbortSignal.timeout(8000) on probes.
const validateProbeTimeout = 8 * time.Second

// probeClientKey carries the *http.Client a probe should use (e.g. the
// connection's proxy-bound client during a dashboard connection test).
type probeClientKey struct{}

// withProbeClient makes validateProbeDo dial through client instead of
// http.DefaultClient. A nil client keeps the default.
func withProbeClient(ctx context.Context, client *http.Client) context.Context {
	if client == nil {
		return ctx
	}
	return context.WithValue(ctx, probeClientKey{}, client)
}

var directProbeClient = &http.Client{
	Transport: &http.Transport{
		Proxy: nil, // direct connection to bypass proxy allowlist
	},
}

func isProxyFailure(err error, resp *http.Response) bool {
	if resp != nil && resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-Proxy-Error") != "" || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/plain") {
			return true
		}
	}
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "proxy") || strings.Contains(errStr, "connect tunnel failed") || strings.Contains(errStr, "blocked-by-allowlist") || strings.Contains(errStr, "forbidden") {
			return true
		}
	}
	return false
}

// validateProbeDo performs an outbound probe request and returns the status code
// plus the (truncated) response body. Package-level so tests can stub the
// upstream call instead of hitting the network.
var validateProbeDo = func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, validateProbeTimeout)
	defer cancel()
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := http.DefaultClient
	if c, ok := ctx.Value(probeClientKey{}).(*http.Client); ok && c != nil {
		client = c
	}
	resp, err := client.Do(req)
	if isProxyFailure(err, resp) {
		if resp != nil {
			resp.Body.Close()
		}
		var directReader io.Reader
		if len(body) > 0 {
			directReader = bytes.NewReader(body)
		}
		if directReq, dErr := http.NewRequestWithContext(ctx, method, rawURL, directReader); dErr == nil {
			for k, v := range headers {
				directReq.Header.Set(k, v)
			}
			if directResp, dErr2 := directProbeClient.Do(directReq); dErr2 == nil {
				resp = directResp
				err = nil
			}
		}
	}
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, err
}

// validateOutcome is the result of a key probe. supported=false means this
// backend cannot probe the provider at all.
type validateOutcome struct {
	valid     bool
	message   string
	supported bool
}

// HandleValidateProvider handles POST /api/providers/validate.
// Mirrors upstream src/app/api/providers/validate/route.js: it answers
// {valid, supported, error} instead of upstream's {valid, error}.
func (h *DashboardHandler) HandleValidateProvider(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Provider             string         `json:"provider"`
		APIKey               string         `json:"apiKey"`
		ProviderSpecificData map[string]any `json:"providerSpecificData"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	provider := providers.ResolveAlias(strings.TrimSpace(req.Provider))
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Provider and API key required")
		return
	}

	// Custom provider nodes (OpenAI-compatible, Anthropic-compatible, Custom Embedding)
	if h.Repo != nil {
		if node, nodeData, err := h.Repo.GetProviderNodeByID(provider); err == nil && node != nil {
			h.validateProviderNodeConnection(w, r, node, nodeData, req.APIKey, req.ProviderSpecificData)
			return
		}
	}

	cfg, known := providers.KnownProviders[provider]
	// ollama-local needs no key; noAuth providers are valid without one.
	if req.APIKey == "" && provider != "ollama-local" && !(known && cfg.NoAuth) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Provider and API key required")
		return
	}
	if !known {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Provider validation not supported")
		return
	}

	out := validateProviderKey(r.Context(), provider, cfg, req.APIKey, req.ProviderSpecificData)
	if !out.supported {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Provider validation not supported")
		return
	}

	payload := map[string]any{"valid": out.valid, "supported": true, "error": nil}
	if !out.valid {
		message := out.message
		if message == "" {
			message = "Invalid API key"
		}
		payload["error"] = message
	}
	handlerutil.WriteJSON(w, http.StatusOK, payload)
}

func (h *DashboardHandler) validateProviderNodeConnection(
	w http.ResponseWriter,
	r *http.Request,
	node *models.ProviderNode,
	nodeData *db.ProviderNodeData,
	apiKey string,
	psd map[string]any,
) {
	ctx := r.Context()
	baseURL := ""
	if nodeData != nil {
		baseURL = strings.TrimSpace(nodeData.BaseURL)
	}
	if baseURL == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":     false,
			"supported": true,
			"error":     "Missing base URL for provider node",
		})
		return
	}

	nodeType := ""
	if node != nil && node.Type != nil {
		nodeType = *node.Type
	}

	// 1. Custom Embedding
	if strings.HasPrefix(node.ID, "custom-embedding-") || nodeType == "custom-embedding" {
		base := strings.TrimSuffix(baseURL, "/")
		status, _, err := validateProbeDo(ctx, http.MethodGet, base+"/models", map[string]string{
			"Authorization": "Bearer " + apiKey,
		}, nil)
		if err == nil && status == http.StatusOK {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"valid": true, "supported": true, "error": nil})
			return
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"valid": false, "supported": true, "error": "Invalid API key"})
			return
		}
		embedPayload, _ := json.Marshal(map[string]any{"model": "test", "input": "ping"})
		eStatus, _, eErr := validateProbeDo(ctx, http.MethodPost, base+"/embeddings", map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
		}, embedPayload)
		if eErr != nil {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"valid":     false,
				"supported": true,
				"error":     validateNodeNetworkMessage(eErr),
			})
			return
		}
		isValid := eStatus != http.StatusUnauthorized && eStatus != http.StatusForbidden
		errMsg := ""
		if !isValid {
			errMsg = "Invalid API key"
		}
		var errVal any
		if !isValid {
			errVal = errMsg
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":     isValid,
			"supported": true,
			"error":     errVal,
		})
		return
	}

	// 2. Anthropic Compatible
	if strings.HasPrefix(node.ID, "anthropic-compatible-") || nodeType == "anthropic-compatible" {
		base := strings.TrimSuffix(baseURL, "/")
		if strings.HasSuffix(base, "/messages") {
			base = base[:len(base)-len("/messages")]
		}
		model := AnthropicValidationModel
		if psd != nil {
			if m, ok := psd["assignedModel"].(string); ok && strings.TrimSpace(m) != "" {
				model = strings.TrimSpace(m)
			}
		}
		payload, _ := json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		})
		status, _, err := validateProbeDo(ctx, http.MethodPost, base+"/v1/messages", map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
			"Authorization":     "Bearer " + apiKey,
			"Content-Type":      "application/json",
		}, payload)
		if err == nil {
			isValid := status != http.StatusUnauthorized && status != http.StatusForbidden
			var errVal any
			if !isValid {
				errVal = "Invalid API key"
			}
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"valid":     isValid,
				"supported": true,
				"error":     errVal,
			})
			return
		}
		// Fallback: try /models
		mStatus, _, mErr := validateProbeDo(ctx, http.MethodGet, base+"/models", map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
			"Authorization":     "Bearer " + apiKey,
		}, nil)
		if mErr != nil {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"valid":     false,
				"supported": true,
				"error":     validateNodeNetworkMessage(err),
			})
			return
		}
		isValid := mStatus == http.StatusOK
		var errVal any
		if !isValid {
			errVal = "Invalid API key"
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":     isValid,
			"supported": true,
			"error":     errVal,
		})
		return
	}

	// 3. OpenAI Compatible (Default)
	base := strings.TrimSuffix(baseURL, "/")
	status, body, err := validateProbeDo(ctx, http.MethodGet, base+"/models", map[string]string{
		"Authorization": "Bearer " + apiKey,
	}, nil)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":     false,
			"supported": true,
			"error":     validateNodeNetworkMessage(err),
		})
		return
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":     false,
			"supported": true,
			"error":     "Invalid API key",
		})
		return
	}

	// Many compatible endpoints (LiteLLM, vLLM, public proxies) expose GET /models
	// without authentication. Probe /chat/completions to verify auth actually succeeds.
	model := "gpt-4o-mini"
	if psd != nil {
		if m, ok := psd["assignedModel"].(string); ok && strings.TrimSpace(m) != "" {
			model = strings.TrimSpace(m)
		}
	}
	if model == "gpt-4o-mini" && len(body) > 0 {
		var modelsRes struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &modelsRes) == nil && len(modelsRes.Data) > 0 && modelsRes.Data[0].ID != "" {
			model = modelsRes.Data[0].ID
		}
	}
	chatPayload, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	chatStatus, _, chatErr := validateProbeDo(ctx, http.MethodPost, base+"/chat/completions", map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	}, chatPayload)
	if chatErr == nil && (chatStatus == http.StatusUnauthorized || chatStatus == http.StatusForbidden) {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":     false,
			"supported": true,
			"error":     "Invalid API key",
		})
		return
	}

	isValid := status == http.StatusOK
	var errVal any
	if !isValid {
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			errVal = fmt.Sprintf("Unexpected status (%d)", status)
		} else {
			errVal = "Invalid API key"
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":     isValid,
		"supported": true,
		"error":     errVal,
	})
}

// (GET /models, falling back to a minimal chat request).
func validateProviderKey(ctx context.Context, provider string, cfg providers.ProviderConfig, apiKey string, psd map[string]any) validateOutcome {
	if cfg.NoAuth {
		return validateOutcome{valid: true, supported: true}
	}

	switch provider {
	case "cloudflare-ai":
		return validateCloudflareAI(ctx, cfg, apiKey, psd)
	case "azure":
		return validateAzure(ctx, apiKey, psd)
	case "ollama-local":
		return validateOllamaLocal(ctx, psd)
	case "gemini":
		status, _, err := validateProbeDo(ctx, http.MethodGet,
			"https://generativelanguage.googleapis.com/v1/models?key="+apiKey, nil, nil)
		if err != nil {
			return validateOutcome{supported: true, message: err.Error()}
		}
		return validateOutcome{valid: status == http.StatusOK, supported: true}
	case "xiaomi-tokenplan":
		return validateXiaomiTokenplan(ctx, apiKey, psd)
	case "grok-web":
		return validateGrokWeb(ctx, apiKey)
	case "perplexity-web":
		return validatePerplexityWeb(ctx, apiKey)
	case "qoder", "qoder-cn":
		return validateQoder(ctx, provider, apiKey, psd)
	case "v1m":
		return validateV1M(ctx, apiKey)
	case "bedrock", "bedrock-xai":
		// A signed ListFoundationModels call. The registry's BaseURL carries a
		// {region} placeholder, so the generic probe would dial a literal "{region}"
		// host — this entry signs its own request against the resolved region.
		valid, message := proxy.BedrockProbe(ctx, http.DefaultClient, apiKey, psd)
		return validateOutcome{valid: valid, supported: true, message: message}
	}

	if isAnthropicProbe(cfg) {
		return validateAnthropicStyle(ctx, provider, cfg, apiKey)
	}
	return validateGeneric(ctx, provider, cfg, apiKey)
}

// isAnthropicProbe reports whether the provider speaks the Anthropic messages
// shape. The registry already carries the full /v1/messages URL, so the probe
// posts to it as-is (upstream's `case "anthropic"` branch).
func isAnthropicProbe(cfg providers.ProviderConfig) bool {
	return cfg.AuthHeader == "x-api-key" || strings.HasSuffix(strings.TrimSuffix(cfg.BaseURL, "/"), "/messages")
}

func validateAnthropicStyle(ctx context.Context, provider string, cfg providers.ProviderConfig, apiKey string) validateOutcome {
	url := strings.TrimSpace(cfg.BaseURL)
	if url == "" {
		return validateOutcome{}
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      validateDefaultModel(provider),
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "test"}},
	})
	headers := validateAuthHeaders(cfg, apiKey)
	headers["anthropic-version"] = "2023-06-01"
	headers["content-type"] = "application/json"
	// 400/529 still prove the key was accepted; only 401/403 mean bad key.
	status, _, err := validateProbeDo(ctx, http.MethodPost, url, headers, payload)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	return validateOutcome{
		valid:     status != http.StatusUnauthorized && status != http.StatusForbidden,
		supported: true,
	}
}

func validateGeneric(ctx context.Context, provider string, cfg providers.ProviderConfig, apiKey string) validateOutcome {
	if cfg.BaseURL == "" {
		return validateOutcome{}
	}
	headers := validateAuthHeaders(cfg, apiKey)
	headers["Content-Type"] = "application/json"

	status, _, err := validateProbeDo(ctx, http.MethodGet, validateModelsURL(cfg.BaseURL), headers, nil)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return validateOutcome{supported: true}
	}
	if status >= 200 && status < 300 {
		return validateOutcome{valid: true, supported: true}
	}

	// Ambiguous /models answer (404, 405, 429…) — fall back to a chat probe.
	payload, _ := json.Marshal(map[string]any{
		"model":      validateDefaultModel(provider),
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	status, _, err = validateProbeDo(ctx, http.MethodPost, cfg.BaseURL, headers, payload)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	return validateOutcome{
		valid:     status != http.StatusUnauthorized && status != http.StatusForbidden,
		supported: true,
	}
}

func validateCloudflareAI(ctx context.Context, cfg providers.ProviderConfig, apiKey string, psd map[string]any) validateOutcome {
	accountID := psdStr(psd, "accountId")
	if accountID == "" {
		accountID = cloudflareAccountFromBaseURL(cfg.BaseURL)
	}
	if accountID == "" {
		return validateOutcome{supported: true, message: "Missing Account ID"}
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      validateDefaultModel("cloudflare-ai"),
		"messages":   []map[string]string{{"role": "user", "content": "test"}},
		"max_tokens": 1,
	})
	url := "https://api.cloudflare.com/client/v4/accounts/" + accountID + "/ai/v1/chat/completions"
	status, _, err := validateProbeDo(ctx, http.MethodPost, url, map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	}, payload)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	valid := status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusNotFound
	out := validateOutcome{valid: valid, supported: true}
	if !valid {
		out.message = "Invalid API token or Account ID"
	}
	return out
}

// validateV1M probes v1m's only endpoint. The registry ships no /models for
// it, so the cheapest call that proves the key works is a one-token System One
// evaluation; a rejected body still proves the credential, so only 401/403
// count as invalid.
func validateV1M(ctx context.Context, apiKey string) validateOutcome {
	payload, _ := json.Marshal(map[string]any{
		"model":     v1mProbeModel,
		"state":     "probe",
		"questions": map[string]any{"probe": map[string]string{"type": "noul"}},
	})
	status, _, err := validateProbeDo(ctx, http.MethodPost, v1mProbeURL, map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	}, payload)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	return validateOutcome{
		valid:     status != http.StatusUnauthorized && status != http.StatusForbidden,
		supported: true,
	}
}

// cloudflareAccountFromBaseURL extracts the account id baked into the registry
// BaseURL from a CLOUDFLARE_ACCOUNT_ID env var, if any.
func cloudflareAccountFromBaseURL(baseURL string) string {
	const marker = "/accounts/"
	idx := strings.Index(baseURL, marker)
	if idx < 0 {
		return ""
	}
	rest := baseURL[idx+len(marker):]
	if end := strings.Index(rest, "/"); end >= 0 {
		return rest[:end]
	}
	return rest
}

func validateAzure(ctx context.Context, apiKey string, psd map[string]any) validateOutcome {
	endpoint := strings.TrimSuffix(strings.TrimSpace(psdStr(psd, "azureEndpoint")), "/")
	deployment := psdStr(psd, "deployment")
	if deployment == "" {
		deployment = "gpt-4"
	}
	apiVersion := psdStr(psd, "apiVersion")
	if apiVersion == "" {
		apiVersion = "2024-10-01-preview"
	}
	if endpoint == "" {
		return validateOutcome{supported: true, message: "Invalid API key or Azure configuration"}
	}
	headers := map[string]string{
		"api-key":      apiKey,
		"Content-Type": "application/json",
	}
	if org := psdStr(psd, "organization"); org != "" {
		headers["OpenAI-Organization"] = org
	}
	payload, _ := json.Marshal(map[string]any{
		"messages":   []map[string]string{{"role": "user", "content": "test"}},
		"max_tokens": 1,
	})
	url := endpoint + "/openai/deployments/" + deployment + "/chat/completions?api-version=" + apiVersion
	status, _, err := validateProbeDo(ctx, http.MethodPost, url, headers, payload)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	valid := status != http.StatusUnauthorized && status != http.StatusForbidden
	out := validateOutcome{valid: valid, supported: true}
	if !valid {
		out.message = "Invalid API key or Azure configuration"
	}
	return out
}

func validateOllamaLocal(ctx context.Context, psd map[string]any) validateOutcome {
	host := strings.TrimSuffix(strings.TrimSpace(psdStr(psd, "baseUrl")), "/")
	if host == "" {
		host = "http://localhost:11434"
	}
	status, _, err := validateProbeDo(ctx, http.MethodGet, host+"/api/tags", nil, nil)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	out := validateOutcome{valid: status == http.StatusOK, supported: true}
	if !out.valid {
		out.message = "Could not reach Ollama at " + host
	}
	return out
}

// validateXiaomiTokenplanBase maps Token Plan regions to their API base URLs.
var validateXiaomiTokenplanBase = map[string]string{
	"sgp": "https://token-plan-sgp.xiaomimimo.com/v1",
	"cn":  "https://token-plan-cn.xiaomimimo.com/v1",
	"ams": "https://token-plan-ams.xiaomimimo.com/v1",
}

func validateXiaomiTokenplan(ctx context.Context, apiKey string, psd map[string]any) validateOutcome {
	region := psdStr(psd, "region")
	base := validateXiaomiTokenplanBase[region]
	if base == "" {
		base = validateXiaomiTokenplanBase["sgp"]
	}
	status, _, err := validateProbeDo(ctx, http.MethodGet, base+"/models", map[string]string{
		"Authorization": "Bearer " + apiKey,
	}, nil)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	// /models answers 403 for valid keys without list permission — only 401 is fatal.
	return validateOutcome{valid: status != http.StatusUnauthorized, supported: true}
}

// --- grok-web: SSO cookie probe (upstream `case "grok-web"`) ---

const (
	grokWebProbeURL = "https://grok.com/rest/app-chat/conversations/new"
	grokWebUA       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

// GrokWebStatsigID is the fixed x-statsig-id grok.com expects from a browser
// session (upstream base64s the same literal).
var grokWebStatsigID = base64.StdEncoding.EncodeToString([]byte("e:TypeError: Cannot read properties of null (reading 'children')"))

func validateGrokWeb(ctx context.Context, apiKey string) validateOutcome {
	token := strings.TrimPrefix(strings.TrimSpace(apiKey), "sso=")

	traceID := validateRandomHex(16)
	spanID := validateRandomHex(8)
	payload, _ := json.Marshal(map[string]any{
		"temporary":                   true,
		"modelName":                   "grok-4",
		"modelMode":                   "MODEL_MODE_GROK_4",
		"message":                     "ping",
		"fileAttachments":             []any{},
		"imageAttachments":            []any{},
		"disableSearch":               false,
		"enableImageGeneration":       false,
		"returnImageBytes":            false,
		"returnRawGrokInXaiRequest":   false,
		"enableImageStreaming":        false,
		"imageGenerationCount":        0,
		"forceConcise":                false,
		"toolOverrides":               map[string]any{},
		"enableSideBySide":            true,
		"sendFinalMetadata":           true,
		"isReasoning":                 false,
		"disableTextFollowUps":        true,
		"disableMemory":               true,
		"forceSideBySide":             false,
		"isAsyncChat":                 false,
		"disableSelfHarmShortCircuit": false,
	})
	headers := map[string]string{
		"Accept":             "*/*",
		"Accept-Encoding":    "gzip, deflate, br, zstd",
		"Accept-Language":    "en-US,en;q=0.9",
		"Cache-Control":      "no-cache",
		"Content-Type":       "application/json",
		"Cookie":             "sso=" + token,
		"Origin":             "https://grok.com",
		"Pragma":             "no-cache",
		"Referer":            "https://grok.com/",
		"Sec-Ch-Ua":          `"Google Chrome";v="136", "Chromium";v="136", "Not(A:Brand";v="24"`,
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"macOS"`,
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "same-origin",
		"User-Agent":         grokWebUA,
		"x-statsig-id":       grokWebStatsigID,
		"x-xai-request-id":   uuid.New().String(),
		"traceparent":        "00-" + traceID + "-" + spanID + "-00",
	}
	status, _, err := validateProbeDo(ctx, http.MethodPost, grokWebProbeURL, headers, payload)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	// Any non-401/403 answer (200, 400, 429) means the cookie was accepted.
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return validateOutcome{supported: true, message: "Invalid SSO cookie — re-paste from grok.com DevTools → Cookies → sso"}
	}
	return validateOutcome{valid: true, supported: true}
}

// --- perplexity-web: session cookie probe (upstream `case "perplexity-web"`) ---

const (
	perplexityWebProbeURL = "https://www.perplexity.ai/rest/sse/perplexity_ask"
	perplexityWebCookie   = "__Secure-next-auth.session-token"
)

func validatePerplexityWeb(ctx context.Context, apiKey string) validateOutcome {
	sessionToken := strings.TrimPrefix(strings.TrimSpace(apiKey), perplexityWebCookie+"=")
	tz := time.Now().Location().String()
	payload, _ := json.Marshal(map[string]any{
		"query_str": "ping",
		"params": map[string]any{
			"query_str":             "ping",
			"search_focus":          "internet",
			"mode":                  "concise",
			"model_preference":      "pplx_pro",
			"sources":               []string{"web"},
			"attachments":           []any{},
			"frontend_uuid":         uuid.New().String(),
			"frontend_context_uuid": uuid.New().String(),
			"version":               "2.18",
			"language":              "en-US",
			"timezone":              tz,
			"search_recency_filter": nil,
			"is_incognito":          true,
			"use_schematized_api":   true,
			"last_backend_uuid":     nil,
		},
	})
	headers := map[string]string{
		"Content-Type":     "application/json",
		"Accept":           "text/event-stream",
		"Origin":           "https://www.perplexity.ai",
		"Referer":          "https://www.perplexity.ai/",
		"User-Agent":       grokWebUA,
		"X-App-ApiClient":  "default",
		"X-App-ApiVersion": "2.18",
		"Cookie":           perplexityWebCookie + "=" + sessionToken,
	}
	status, _, err := validateProbeDo(ctx, http.MethodPost, perplexityWebProbeURL, headers, payload)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return validateOutcome{supported: true, message: "Invalid session cookie — re-paste __Secure-next-auth.session-token from perplexity.ai"}
	}
	return validateOutcome{valid: true, supported: true}
}

// The Qoder endpoint table, PAT check, and credential exchange now live in the
// executor package (qoder_endpoints.go, qoder_catalog.go, qoder_credentials.go)
// because chat needs them too: model_config comes from the same COSY-signed
// model list. The dashboard and chat share one copy rather than drifting apart.

func validateQoder(ctx context.Context, provider, apiKey string, psd map[string]any) validateOutcome {
	ep := executor.QoderEndpointsFor(provider)
	token := strings.TrimSpace(apiKey)
	if token == "" {
		token = psdStr(psd, "accessToken")
	}
	if token == "" {
		return validateOutcome{supported: true, message: "Qoder credential is empty"}
	}
	userID := psdStr(psd, "userId", "user_id", "id")

	do := qoderProbeDoer(ctx)
	if executor.IsQoderPAT(token) {
		// A PAT cannot sign COSY requests — exchange it for a job token first.
		jobToken, err := executor.ExchangeQoderJobToken(ctx, do, ep.JobTokenExchangeURL, token)
		if err != nil {
			return validateOutcome{supported: true, message: err.Error()}
		}
		token = jobToken
		if userID == "" {
			userID = executor.FetchQoderUserID(ctx, do, ep.UserInfoURL, jobToken)
		}
	}
	if userID == "" {
		// COSY signing needs a user id; without it the model list cannot be asked for.
		return validateOutcome{supported: true, message: "Qoder user ID missing — re-login or paste a PAT"}
	}

	modelListURL := ep.ModelListURLForToken(token)
	headers, err := executor.BuildQoderCosyHeaders(nil, modelListURL, executor.QoderCosyCreds{
		UserID:    userID,
		AuthToken: token,
		MachineID: psdStr(psd, "machineId", "machine_id"),
	})
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	headers["Accept"] = "application/json"
	headers["Accept-Encoding"] = "identity"

	status, body, err := do(ctx, http.MethodGet, modelListURL, headers, nil)
	if err != nil {
		return validateOutcome{supported: true, message: err.Error()}
	}
	if status < 200 || status >= 300 {
		return validateOutcome{supported: true, message: fmt.Sprintf("Qoder model list returned %d", status)}
	}
	if countQoderCatalogModels(body) == 0 {
		return validateOutcome{supported: true, message: "Qoder returned no models for this credential"}
	}
	return validateOutcome{valid: true, supported: true}
}

// qoderProbeDoer adapts the dashboard's probe seam to the Qoder transport
// interface, so chat and the dashboard share one implementation of the
// credential exchange and the model-list fetch while keeping their own
// transports (the probe honours a connection's proxy-bound client and is
// stubbed in tests).
func qoderProbeDoer(ctx context.Context) executor.QoderDoer {
	return func(innerCtx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		return validateProbeDo(innerCtx, method, rawURL, headers, body)
	}
}

// countQoderCatalogModels counts the routable entries of a /algo/api/v2/model/list
// answer ({chat:[{key, enable}]}), mirroring upstream's models.length filter.
func countQoderCatalogModels(body []byte) int {
	var out struct {
		Chat []map[string]any `json:"chat"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0
	}
	count := 0
	for _, entry := range out.Chat {
		key, _ := entry["key"].(string)
		if key == "" {
			continue
		}
		if enable, ok := entry["enable"].(bool); ok && !enable {
			continue
		}
		count++
	}
	return count
}

func validateRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := crand.Read(buf); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(buf)
}

func validateTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func validateAuthHeaders(cfg providers.ProviderConfig, apiKey string) map[string]string {
	headers := map[string]string{}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	header := cfg.AuthHeader
	if header == "" {
		header = "Authorization"
	}
	switch cfg.AuthScheme {
	case "raw":
		headers[header] = apiKey
	default:
		headers[header] = "Bearer " + apiKey
	}
	return headers
}

// validateModelsURL derives the listing endpoint from a chat completions URL,
// mirroring upstream's replace(/\/chat\/completions$/, "/models").
func validateModelsURL(baseURL string) string {
	url := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	switch {
	case strings.HasSuffix(url, "/chat/completions"):
		return strings.TrimSuffix(url, "/chat/completions") + "/models"
	case strings.HasSuffix(url, "/chatbot"):
		return strings.TrimSuffix(url, "/chatbot") + "/models"
	}
	return url
}

// validateDefaultModel is upstream's getDefaultModel: the provider's first
// registry model, used as the probe payload model.
func validateDefaultModel(provider string) string {
	if models := providers.GetProviderModels(provider); len(models) > 0 {
		return models[0]
	}
	return "test"
}
