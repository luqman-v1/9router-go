package proxy

import (
	"fmt"
	"io"
	"net/http"

	"9router/proxy/internal/providers"
)

// ForwardOpenAI sends an OpenAI-format request to the provider endpoint.
func ForwardOpenAI(client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	if !cfg.NoAuth {
		switch cfg.AuthScheme {
		case "bearer":
			headers[cfg.AuthHeader] = "Bearer " + apiKey
		case "raw":
			headers[cfg.AuthHeader] = apiKey
		default:
			headers["Authorization"] = "Bearer " + apiKey
		}
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

// ReadBody reads and returns the full response body, closing it.
func ReadBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// UpstreamBody reads the body and wraps non-200 as UpstreamError.
func UpstreamBody(resp *http.Response) ([]byte, error) {
	body, err := ReadBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: body}
	}
	return body, nil
}

// BuildURL joins a base URL with a path segment.
func BuildURL(base, path string) string {
	if base == "" {
		return path
	}
	if path == "" {
		return base
	}
	return fmt.Sprintf("%s/%s", base, path)
}
