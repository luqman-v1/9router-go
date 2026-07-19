package oauth

import (
	"fmt"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

// RegisterAll registers all built-in OAuth refreshers.
func RegisterAll() {
	// Standard OAuth2 refresh_token grant (Google, xAI, codex, claude, etc.)
	Register("standard", standardRefresh)
}

// Standard OAuth2 refresh_token grant.
func standardRefresh(p *Params) (*TokenResult, error) {
	// KnownOAuthConfigs lookup is done by the caller (handlers/fallback.go)
	// This is a simple POST to token_url with refresh_token grant.
	// Actual client_id/client_secret are passed via Params extensions.
	return nil, fmt.Errorf("standard refresh not implemented")
}

// OAuthTokenResponse from a standard OAuth2 token endpoint.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
}

// DoRefreshToken performs a standard OAuth2 refresh_token grant.
func DoRefreshToken(client *http.Client, tokenURL, clientID, clientSecret, refreshToken string) (*TokenResult, error) {
	vals := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	resp, err := client.PostForm(tokenURL, vals)
	if err != nil {
		return nil, fmt.Errorf("OAuth POST: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth returned %d: %s", resp.StatusCode, string(body))
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("empty access token")
	}

	return &TokenResult{
		AccessToken: tr.AccessToken,
		ExpiresIn:   tr.ExpiresIn,
		Scope:       tr.Scope,
	}, nil
}

// BuildConnectionUpdate builds a DB update map from a refresh result.
func BuildConnectionUpdate(result *TokenResult) map[string]interface{} {
	return map[string]interface{}{
		"accessToken": result.AccessToken,
		"expiresAt":   time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339),
	}
}
