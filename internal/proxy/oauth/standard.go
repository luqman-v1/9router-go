package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

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

// doFormRefresh performs a standard form-urlencoded POST to a token endpoint
// and returns the parsed TokenResult.
func doFormRefresh(ctx context.Context, client *http.Client, tokenURL string, vals url.Values) (*TokenResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh returned %d: %s", resp.StatusCode, string(body))
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
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
		Scope:       result.Scope,
	}, nil
}
