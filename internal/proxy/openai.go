package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
)

// ForwardOpenAI sends an OpenAI-format request to the provider endpoint.
func ForwardOpenAI(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
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
	// Match Node's undici global fetch (user-agent "node", accept "*/*").
	// An operator StaticHeaders value always wins, whatever its casing.
	if !hasHeaderFold(headers, "User-Agent") {
		headers["User-Agent"] = "node"
	}
	if !hasHeaderFold(headers, "Accept") {
		headers["Accept"] = "*/*"
	}
	// The operator's Custom Headers are applied last so they also win over the
	// defaults set just above, which is the order upstream uses
	// (open-sse/executors/base.js:132).
	headers = providers.ApplyHeaderOverrides(headers, cfg.HeaderOverrides)
	resp, err := DoRequest(ctx, client, "POST", cfg.BaseURL, headers, body)
	if err != nil {
		return nil, fmt.Errorf("forward to %s: %w", cfg.BaseURL, err)
	}
	return resp, nil
}

// ReadBody reads and returns the response body (capped to prevent
// unbounded memory use), closing it.
func ReadBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
}

// UpstreamBody reads the body and wraps non-200 as UpstreamError.
func UpstreamBody(resp *http.Response) ([]byte, error) {
	body, err := ReadBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read upstream body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
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

// hasHeaderFold reports whether headers already carries key under any casing.
func hasHeaderFold(headers map[string]string, key string) bool {
	for k := range headers {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}
