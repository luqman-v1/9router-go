package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

// TokenInfo holds OAuth token data from a provider connection.
type TokenInfo struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"`
	TokenType    string `json:"tokenType"`
	Scope        string `json:"scope"`
}

// ProviderSpecificData holds provider-specific OAuth configuration.
type ProviderSpecificData struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	TokenURL     string `json:"tokenEndpoint"`
	Scope        string `json:"scope"`
}

// ParseTokenFromConnection extracts token info from connection.data JSON.
func ParseTokenFromConnection(data string) (*TokenInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("parse connection data: %w", err)
	}

	token := &TokenInfo{
		AccessToken:  handlerutil.GetString(raw, "accessToken"),
		RefreshToken: handlerutil.GetString(raw, "refreshToken"),
		ExpiresAt:    handlerutil.GetString(raw, "expiresAt"),
		TokenType:    handlerutil.GetString(raw, "tokenType"),
		Scope:        handlerutil.GetString(raw, "scope"),
	}
	return token, nil
}

// ParseProviderSpecificData extracts OAuth config from connection.data JSON.
func ParseProviderSpecificData(data string) (*ProviderSpecificData, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("parse connection data: %w", err)
	}

	psd, ok := raw["providerSpecificData"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("no providerSpecificData found")
	}

	return &ProviderSpecificData{
		ClientID:     handlerutil.GetString(psd, "clientId"),
		ClientSecret: handlerutil.GetString(psd, "clientSecret"),
		TokenURL:     handlerutil.GetString(psd, "tokenEndpoint"),
		Scope:        handlerutil.GetString(psd, "scope"),
	}, nil
}

// IsTokenExpired checks if the access token is expired (with 5min buffer).
func IsTokenExpired(token *TokenInfo) bool {
	if token.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil {
		return true
	}
	return time.Now().Add(5 * time.Minute).After(t)
}

// RefreshToken exchanges a refresh token for a new access token.
func RefreshToken(ctx context.Context, token *TokenInfo, psd *ProviderSpecificData) (*TokenInfo, error) {
	if token.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}
	if psd.TokenURL == "" {
		return nil, fmt.Errorf("no token endpoint configured")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.RefreshToken)
	if psd.ClientID != "" {
		form.Set("client_id", psd.ClientID)
	}
	if psd.ClientSecret != "" {
		form.Set("client_secret", psd.ClientSecret)
	}
	if psd.Scope != "" {
		form.Set("scope", psd.Scope)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", psd.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("refresh returned %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	newToken := &TokenInfo{
		AccessToken:  handlerutil.GetString(result, "access_token"),
		RefreshToken: handlerutil.GetString(result, "refresh_token"),
		TokenType:    handlerutil.GetString(result, "token_type"),
	}

	if newToken.RefreshToken == "" {
		newToken.RefreshToken = token.RefreshToken
	}

	expiresIn := getInt(result, "expires_in")
	if expiresIn > 0 {
		newToken.ExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
	}

	return newToken, nil
}

func getInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}
