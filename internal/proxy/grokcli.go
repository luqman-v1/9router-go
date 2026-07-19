package proxy

import (
	"net/http"

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
func ForwardCodex(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
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

// ForwardKiro forwards to kiro with kiro-specific headers.
func ForwardKiro(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	setAuth(headers, cfg, apiKey)
	streamHeaders(headers, isStream)
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}

// ForwardAzure forwards to Azure OpenAI with api-key header.
func ForwardAzure(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool, endpoint string) (*http.Response, error) {
	headers := map[string]string{"Content-Type": "application/json", "api-key": apiKey}
	streamHeaders(headers, isStream)
	return DoRequest(client, "POST", endpoint, headers, body)
}

// ForwardCommandcode forwards to CommandCode with bearer auth.
func ForwardCommandcode(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte) (*http.Response, error) {
	headers := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + apiKey}
	return DoRequest(client, "POST", cfg.BaseURL, headers, body)
}
