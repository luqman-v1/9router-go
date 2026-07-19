package proxy

import (
	"net/http"

	"9router/proxy/internal/providers"
)

// ForwardGrokCLI forwards to grok-cli using OpenAI Responses API format.
func ForwardGrokCLI(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	if !cfg.NoAuth {
		headers[cfg.AuthHeader] = "Bearer " + apiKey
	}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
	}
	resp, err := DoRequest(client, "POST", cfg.BaseURL, headers, body)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ForwardCodex forwards to codex / perplexity-agent using OpenAI Responses API format.
func ForwardCodex(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	if !cfg.NoAuth {
		headers[cfg.AuthHeader] = "Bearer " + apiKey
	}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
	}
	// codex uses /v1/responses endpoint, passthrough
	resp, err := DoRequest(client, "POST", cfg.BaseURL, headers, body)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
func ForwardIflow(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{"Authorization": "Bearer " + apiKey}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
		headers["X-Stream-Options"] = "include-usage"
	}
	resp, err := DoRequest(client, "POST", cfg.BaseURL, headers, body)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ForwardKimchi forwards to kimchi with Anthropic field stripping.
func ForwardKimchi(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	if !cfg.NoAuth {
		headers[cfg.AuthHeader] = "Bearer " + apiKey
	}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
	}
	// kimchi uses OpenAI format, passthrough
	resp, err := DoRequest(client, "POST", cfg.BaseURL, headers, body)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

