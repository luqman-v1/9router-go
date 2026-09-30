package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/constants"
)

// UpstreamError captures a non-200 upstream response.
type UpstreamError struct {
	StatusCode int
	Body       []byte
	// Header is the upstream response's headers. Rate-limit carriers such as
	// Retry-After live only here — a Gemini/Antigravity 429 body repeats the
	// wait in a google.rpc.RetryInfo payload at best — and the error is built
	// where the response is still in hand, so this is the only chance to keep
	// them.
	Header http.Header
}

func (e *UpstreamError) Error() string {
	// Include the upstream body (truncated) so 4xx/5xx failures are diagnosable
	// from the fallback log alone — the body often carries Google/Antigravity's
	// actual rejection reason ("Invalid tool parameters", unknown model, etc.).
	body := strings.TrimSpace(string(e.Body))
	if strings.HasPrefix(body, "<!DOCTYPE html") || strings.HasPrefix(body, "<html") {
		lower := strings.ToLower(body)
		if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "attention required") {
			return fmt.Sprintf("upstream returned %d: Cloudflare WAF challenge (Attention Required!): check User-Agent or network proxy", e.StatusCode)
		}
		if titleStart := strings.Index(lower, "<title>"); titleStart != -1 {
			titleEnd := strings.Index(lower[titleStart:], "</title>")
			if titleEnd != -1 {
				titleText := strings.TrimSpace(body[titleStart+7 : titleStart+titleEnd])
				return fmt.Sprintf("upstream returned %d: HTML page (%s)", e.StatusCode, titleText)
			}
		}
		return fmt.Sprintf("upstream returned %d: HTML error page", e.StatusCode)
	}
	if len(body) > 512 {
		body = body[:512] + "... (truncated)"
	}
	if body != "" {
		return fmt.Sprintf("upstream returned %d: %s", e.StatusCode, body)
	}
	return fmt.Sprintf("upstream returned %d", e.StatusCode)
}

var directProxyClient = &http.Client{
	Transport: constants.DefaultHTTPTransportConfig.NewTransport(),
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

// DoRequest sends an HTTP POST to url with body and auth, returns the raw response.
// Caller must close resp.Body.
func DoRequest(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if isProxyFailure(err, resp) {
		if resp != nil {
			resp.Body.Close()
		}
		if directReq, dErr := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body)); dErr == nil {
			directReq.Header.Set("Content-Type", "application/json")
			for k, v := range headers {
				directReq.Header.Set(k, v)
			}
			if directResp, dErr2 := directProxyClient.Do(directReq); dErr2 == nil {
				resp = directResp
				err = nil
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("upstream returned %d and body read failed: %w", resp.StatusCode, readErr)
		}
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header}
	}
	return resp, nil
}
