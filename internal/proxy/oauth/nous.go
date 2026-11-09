package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"9router/proxy/internal/providers"
)

func init() {
	Register("nous", refreshNous)
}

func refreshNous(ctx context.Context, p *Params) (*TokenResult, error) {
	if p.RefreshToken == "" {
		return nil, fmt.Errorf("missing refresh token")
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {p.RefreshToken},
		"client_id":     {"hermes-cli"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, providers.NousTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-nous-refresh-token", p.RefreshToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nous token refresh failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("nous token refresh returned %d: %s", resp.StatusCode, string(body))
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read Nous token response: %w", err)
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(bodyBytes, &tokenData); err != nil {
		return nil, fmt.Errorf("decode Nous token response: %w", err)
	}
	if tokenData.AccessToken == "" {
		return nil, fmt.Errorf("nous token refresh returned no access_token")
	}

	newRefreshToken := tokenData.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = p.RefreshToken
	}
	expiresIn := tokenData.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	// Mint agent key with fallback to access token
	agentKey := mintNousAgentKey(ctx, client, tokenData.AccessToken)
	apiKey := tokenData.AccessToken
	if agentKey != "" {
		apiKey = agentKey
	}

	return &TokenResult{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: newRefreshToken,
		ExpiresIn:    expiresIn,
		APIKey:       apiKey,
	}, nil
}

func mintNousAgentKey(ctx context.Context, client *http.Client, accessToken string) string {
	payload, _ := json.Marshal(map[string]any{"min_ttl_seconds": 7200})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, providers.NousAgentKeyURL, bytes.NewReader(payload))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}

	var data struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return ""
	}
	return data.APIKey
}
