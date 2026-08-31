package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/providers"
)

func init() {
	Register("cline", RefreshCline)
}

// RefreshCline refreshes tokens using Cline's extension JSON contract.
func RefreshCline(ctx context.Context, p *Params) (*TokenResult, error) {
	if p.RefreshToken == "" {
		return nil, fmt.Errorf("cline: refresh_token is required")
	}

	tokenURL := "https://api.cline.bot/v1/auth/refresh"
	if cfg, ok := providers.KnownOAuthConfigs["cline"]; ok && cfg.TokenURL != "" {
		tokenURL = cfg.TokenURL
	}

	reqBody, err := json.Marshal(map[string]string{
		"refreshToken": p.RefreshToken,
		"grantType":    "refresh_token",
		"clientType":   "extension",
	})
	if err != nil {
		return nil, fmt.Errorf("cline: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("cline: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("cline: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cline: refresh returned status %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    string `json:"expiresAt"`
			ExpiresIn    int    `json:"expiresIn"`
		} `json:"data"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    string `json:"expiresAt"`
		ExpiresIn    int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("cline: parse response: %w", err)
	}

	accToken := parsed.Data.AccessToken
	if accToken == "" {
		accToken = parsed.AccessToken
	}
	if accToken == "" {
		return nil, fmt.Errorf("cline: no accessToken found in response")
	}

	expiresIn := 3600
	expiresAtStr := parsed.Data.ExpiresAt
	if expiresAtStr == "" {
		expiresAtStr = parsed.ExpiresAt
	}
	if expiresAtStr != "" {
		if t, err := time.Parse(time.RFC3339, expiresAtStr); err == nil {
			sec := int(time.Until(t).Seconds())
			if sec > 0 {
				expiresIn = sec
			}
		}
	} else if parsed.Data.ExpiresIn > 0 {
		expiresIn = parsed.Data.ExpiresIn
	} else if parsed.ExpiresIn > 0 {
		expiresIn = parsed.ExpiresIn
	}

	return &TokenResult{
		AccessToken: accToken,
		ExpiresIn:   expiresIn,
	}, nil
}
