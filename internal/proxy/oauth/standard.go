package oauth

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

const maxRefreshBodyBytes = 1 << 20

var defaultRefreshHTTPClient = &http.Client{Timeout: 15 * time.Second}

func refreshHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return defaultRefreshHTTPClient
}

// NewStandardRefresher creates a refresher for a standard OAuth2 refresh_token grant.
func NewStandardRefresher(tokenURL, clientID, clientSecret string) Refresher {
	return func(ctx context.Context, p *Params) (*TokenResult, error) {
		vals := url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {clientID},
			"refresh_token": {p.RefreshToken},
		}
		if clientSecret != "" {
			vals.Set("client_secret", clientSecret)
		}

		return doFormRefresh(ctx, p.Client, tokenURL, vals)
	}
}

// truncateBody caps an upstream body used in error messages so credentials
// echoed back by a token endpoint cannot leak into logs.
func truncateBody(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}

// doFormRefresh performs a standard form-urlencoded POST to a token endpoint
// and returns the parsed TokenResult.
func doFormRefresh(ctx context.Context, client *http.Client, tokenURL string, vals url.Values) (*TokenResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := refreshHTTPClient(client).Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRefreshBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Truncate the body so a token endpoint that echoes the request
		// (including refresh_token/client_secret) cannot leak it into logs.
		return nil, &providers.OAuthRefreshError{Status: resp.StatusCode, Body: truncateBody(body)}
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope,omitempty"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access token")
	}

	return &TokenResult{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
		Scope:        result.Scope,
	}, nil
}
