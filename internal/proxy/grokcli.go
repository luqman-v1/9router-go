package proxy

import (
	"fmt"
	"net/http"
	"time"

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
func ForwardGrokCLI(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{
		"x-grok-client-identifier": "grok-cli-go",
		"x-grok-client-version":    "0.1.0",
	}
	setAuth(headers, cfg, apiKey)
	streamHeaders(headers, isStream)
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}

// ForwardCodex forwards to codex / perplexity-agent using OpenAI Responses API format.
// Body transformation (Chat→Responses API) is done by the caller.
func ForwardCodex(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{
		"originator": "codex_cli_rs",
	}
	setAuth(headers, cfg, apiKey)
	streamHeaders(headers, isStream)
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
func ForwardIflow(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool, extraHeaders map[string]string) (*http.Response, error) {
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
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}

// ForwardKimchi forwards to kimchi (OpenAI-format with anthropic field stripping).
func ForwardKimchi(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	setAuth(headers, cfg, apiKey)
	streamHeaders(headers, isStream)
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}

// ForwardKiro forwards to kiro with AWS EventStream headers.
// The caller must handle the EventStream binary response.
func ForwardKiro(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	invocationID := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), time.Now().UnixNano())
	headers := map[string]string{
		"X-Amz-Target":       "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		"Amz-Sdk-Request":    "attempt=1; max=3",
		"Amz-Sdk-Invocation-Id": invocationID,
		"Accept":             "application/vnd.amazon.eventstream",
	}
	setAuth(headers, cfg, apiKey)
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}

// ForwardAzure forwards to Azure OpenAI with api-key header.
// URL (endpoint) is constructed by the caller from env config.
func ForwardAzure(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool, endpoint string) (*http.Response, error) {
	headers := map[string]string{"Content-Type": "application/json", "api-key": apiKey}
	streamHeaders(headers, isStream)
	return DoRequest(client, "POST", endpoint, headers, body)
}

// ForwardCommandcode forwards to CommandCode with custom headers and forced streaming.
// Body transformation (force stream=true) is done by the caller.
func ForwardCommandcode(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte) (*http.Response, error) {
	headers := map[string]string{
		"Content-Type":          "application/json",
		"Authorization":         "Bearer " + apiKey,
		"x-command-code-version": "0.25.7",
		"x-cli-environment":     "cli",
		"Accept":                "text/event-stream",
	}
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}
