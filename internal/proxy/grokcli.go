package proxy

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

func setAuth(headers map[string]string, cfg *providers.ProviderConfig, apiKey string) {
	if cfg.NoAuth {
		return
	}
	switch cfg.AuthScheme {
	case "bearer":
		headers[cfg.AuthHeader] = "Bearer " + apiKey
	case "raw":
		headers[cfg.AuthHeader] = apiKey
	default:
		headers["Authorization"] = "Bearer " + apiKey
	}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
}

func streamHeaders(headers map[string]string, isStream bool) {
	if isStream {
		headers["Accept"] = "text/event-stream"
	}
}

// ForwardGrokCLI forwards to grok-cli using OpenAI Responses API format.
// Body transformation (Chat→Responses API) is done by the caller.
func ForwardGrokCLI(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{
		"User-Agent":               providers.GrokCLIUserAgent,
		"x-grok-client-identifier": providers.GrokCLIClientIdentifier,
		"x-grok-client-version":    providers.GrokCLIVersion,
	}
	setAuth(headers, cfg, apiKey)
	streamHeaders(headers, isStream)
	targetURL := cfg.BaseURL
	if targetURL == "" {
		targetURL = "https://cli-chat-proxy.grok.com/v1/responses"
	} else if strings.TrimRight(targetURL, "/") == "https://cli-chat-proxy.grok.com" {
		targetURL = "https://cli-chat-proxy.grok.com/v1/responses"
	}
	return DoRequest(ctx, client, "POST", targetURL, headers, body)
}

// codexResponsesLiteHeader tells the Codex backend to serve the body in its
// Lite shape. It must not ride along on a body carrying hosted search: Lite
// cannot execute that tool, and upstream drops the header for exactly those
// requests (7bf931781).
const codexResponsesLiteHeader = "x-openai-internal-codex-responses-lite"

// ForwardCodex forwards to codex / perplexity-agent using OpenAI Responses API format.
// Body transformation (Chat→Responses API) is done by the caller.
//
// psd is the connection's providerSpecificData. chatgpt-account-id selects
// which ChatGPT workspace the request bills against; the codex endpoint
// rejects a request that omits it, so a connection stored without
// chatgptAccountId can authorize but never complete a call.
//
// responsesLite is the caller's answer rather than a decision made here: the
// executor owns which shape a body ended up in, and the package boundary keeps
// this one from importing that back.
func ForwardCodex(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool, psd map[string]any, responsesLite bool) (*http.Response, error) {
	headers := map[string]string{
		"originator": "codex_cli_rs",
	}
	if responsesLite {
		cfg = providers.WithStaticHeader(cfg, codexResponsesLiteHeader, "true")
	} else {
		cfg = providers.WithoutStaticHeader(cfg, codexResponsesLiteHeader)
	}
	if accountID, ok := psd["chatgptAccountId"].(string); ok && accountID != "" {
		headers["chatgpt-account-id"] = accountID
	}
	setAuth(headers, cfg, apiKey)
	streamHeaders(headers, isStream)
	return DoRequest(ctx, client, "POST", cfg.BaseURL, headers, body)
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
func ForwardIflow(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool, extraHeaders map[string]string) (*http.Response, error) {
	// iflow uses HMAC-SHA256, not bearer auth — skip setAuth
	headers := make(map[string]string, len(cfg.StaticHeaders)+len(extraHeaders))
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
		headers["X-Stream-Options"] = "include-usage"
	}
	return DoRequest(ctx, client, "POST", cfg.BaseURL, headers, body)
}

// ForwardKimchi forwards to kimchi (OpenAI-format with anthropic field stripping).
func ForwardKimchi(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	setAuth(headers, cfg, apiKey)
	streamHeaders(headers, isStream)
	return DoRequest(ctx, client, "POST", cfg.BaseURL, headers, body)
}

// ForwardKiro forwards to kiro with AWS EventStream headers.
// The caller must handle the EventStream binary response.
//
// Ported from open-sse/executors/kiro.js: the Amazon surfaces (q.*, then
// codewhisperer.*) are tried before the Kiro IDE gateway, regionalized from the
// profile, and a 401/403/404 moves on to the next surface — one dead gateway
// must not look like a dead account. authMethod decides the TokenType header
// and the profile ARN travels in `x-amzn-codewhisperer-profile-arn`.
func ForwardKiro(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool, psd map[string]any) (*http.Response, error) {
	clean := cleanKiroBody(body)
	endpoints := kiroEndpoints(cfg, psd)

	var lastErr error
	for idx, endpoint := range endpoints {
		last := idx == len(endpoints)-1
		invocationID := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), time.Now().UnixNano())
		headers := map[string]string{
			"X-Amz-Target":                    "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
			"Amz-Sdk-Request":                 "attempt=1; max=3",
			"Amz-Sdk-Invocation-Id":           invocationID,
			"Accept":                          "application/vnd.amazon.eventstream",
			"User-Agent":                      "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0",
			"X-Amz-User-Agent":                "aws-sdk-js/3.0.0 kiro-ide/1.0.0",
			"x-amzn-kiro-agent-mode":          "spec",
			"x-amzn-codewhisperer-machine-id": "kiro-desktop",
		}
		if apiKey != "" {
			headers["x-amz-sso-bearer"] = apiKey
		}
		if profileArn, ok := psd["profileArn"].(string); ok && profileArn != "" {
			headers["x-amzn-codewhisperer-profile-arn"] = profileArn
		}
		if tokenType := kiroTokenType(psd); tokenType != "" {
			headers["TokenType"] = tokenType
		}
		setAuth(headers, cfg, apiKey)

		resp, err := DoRequest(ctx, client, "POST", endpoint, headers, clean)
		if err != nil {
			lastErr = err
			if last {
				return nil, err
			}
			continue
		}
		if last || !kiroEndpointFallbackStatus(resp.StatusCode) {
			return resp, nil
		}
		// Surface rejected the credential or retired the path: drain and try the
		// next gateway rather than reporting a dead account.
		resp.Body.Close()
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kiro: no endpoint available")
	}
	return nil, lastErr
}

// kiroTokenType mirrors upstream buildHeaders: API-key connections announce
// TokenType=API_KEY, enterprise IdP tokens EXTERNAL_IDP, everything else sends
// no TokenType at all.
func kiroTokenType(psd map[string]any) string {
	switch authMethod, _ := psd["authMethod"].(string); authMethod {
	case "api_key":
		return "API_KEY"
	case "external_idp":
		return "EXTERNAL_IDP"
	default:
		return ""
	}
}

// kiroEndpointFallbackStatus is upstream KIRO_ENDPOINT_FALLBACK_STATUSES: an
// expired token, a foreign token or a retired path is a surface problem, not a
// payload problem, so the next gateway gets a turn. 400 stays terminal.
func kiroEndpointFallbackStatus(status int) bool {
	return status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusNotFound
}

// kiroEndpoints returns the ordered gateway list: Amazon surfaces first (q.*
// before the legacy codewhisperer host), then the Kiro IDE gateway, with the
// AWS hosts regionalized to the account's own region.
func kiroEndpoints(cfg *providers.ProviderConfig, psd map[string]any) []string {
	region, _ := psd["region"].(string)
	region = strings.TrimSpace(region)
	if region == "" {
		region = "us-east-1"
	}
	regionalize := func(endpoint string) string {
		if region == "us-east-1" || !strings.Contains(endpoint, "amazonaws.com") {
			return endpoint
		}
		parts := strings.SplitN(endpoint, "://", 2)
		if len(parts) != 2 {
			return endpoint
		}
		host := strings.SplitN(parts[1], "/", 2)
		service := strings.SplitN(host[0], ".", 2)
		if len(service) != 2 {
			return endpoint
		}
		localized := service[0] + "." + region + ".amazonaws.com"
		if len(host) == 2 {
			return parts[0] + "://" + localized + "/" + host[1]
		}
		return parts[0] + "://" + localized
	}

	q := "https://q." + region + ".amazonaws.com/generateAssistantResponse"
	codewhisperer := regionalize("https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse")
	kiroGateway := "https://runtime.us-east-1.kiro.dev/generateAssistantResponse"

	ordered := []string{q, codewhisperer}
	if override := strings.TrimSpace(cfg.BaseURL); override != "" && !strings.Contains(override, "amazonaws.com") {
		// A configured non-Amazon base URL (self-hosted mirror) is the only one.
		return []string{override}
	}
	if override := strings.TrimSpace(cfg.BaseURL); override != "" {
		ordered = []string{regionalize(override), codewhisperer}
	}
	return append(ordered, kiroGateway)
}

const (
	// KiroToolResultsPlaceholder is a neutral placeholder for user turns carrying only tool results.
	// Prevents models from answering "Nothing in progress to continue" (decolua/9router #4109).
	KiroToolResultsPlaceholder = "Tool results provided."
	KiroEmptyUserPlaceholder   = "continue"
)

func kiroEmptyUserContent(hasToolResults bool) string {
	if hasToolResults {
		return KiroToolResultsPlaceholder
	}
	return KiroEmptyUserPlaceholder
}

func normalizeKiroUserMessage(uim map[string]any) {
	content, _ := uim["content"].(string)
	if strings.TrimSpace(content) == "" {
		var hasToolResults bool
		if ctx, ok := uim["userInputMessageContext"].(map[string]any); ok {
			if tr, ok := ctx["toolResults"].([]any); ok && len(tr) > 0 {
				hasToolResults = true
			}
		}
		uim["content"] = kiroEmptyUserContent(hasToolResults)
	}
}

func cleanKiroBody(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m, handlerutil.UpstreamBody); err != nil {
		return body
	}
	delete(m, "systemPrompt")
	delete(m, "agentMode")

	// PR #4109: normalize user turns carrying only tool results
	if uim, ok := m["userInputMessage"].(map[string]any); ok {
		normalizeKiroUserMessage(uim)
	}
	if cs, ok := m["conversationState"].(map[string]any); ok {
		delete(cs, "agentContinuationId")
		delete(cs, "agentTaskType")
		if cur, ok := cs["currentMessage"].(map[string]any); ok {
			if uim, ok := cur["userInputMessage"].(map[string]any); ok {
				normalizeKiroUserMessage(uim)
			}
		}
		if history, ok := cs["history"].([]any); ok {
			for _, turn := range history {
				if tMap, ok := turn.(map[string]any); ok {
					if uim, ok := tMap["userInputMessage"].(map[string]any); ok {
						normalizeKiroUserMessage(uim)
					}
				}
			}
		}
	}
	if out, err := json.Marshal(m); err == nil {
		return out
	}
	return body
}

// ForwardAzure forwards to Azure OpenAI with api-key header.
// URL (endpoint) is constructed by the caller from env config.
func ForwardAzure(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool, endpoint string) (*http.Response, error) {
	headers := map[string]string{"Content-Type": "application/json", "api-key": apiKey}
	streamHeaders(headers, isStream)
	return DoRequest(ctx, client, "POST", endpoint, headers, body)
}

// ForwardCommandcode forwards to CommandCode with custom headers and forced streaming.
// Body transformation (force stream=true) is done by the caller.
func ForwardCommandcode(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte) (*http.Response, error) {
	headers := map[string]string{
		"Content-Type":           "application/json",
		"Authorization":          "Bearer " + apiKey,
		"x-command-code-version": "0.25.7",
		"x-cli-environment":      "cli",
		"Accept":                 "text/event-stream",
	}
	return DoRequest(ctx, client, "POST", cfg.BaseURL, headers, body)
}
