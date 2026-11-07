package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

func init() {
	Register("perch", refreshPerch)
}

// refreshPerch exchanges a Perch refresh token for a fresh Supabase session.
// The dance mirrors the official CLI and the open-source adapters
// (PerchAI2API / pi-perchai): GET the CLI auth config, then POST the
// refresh_token grant at the Supabase project the config names. The project
// URL is checked against the one Perch publishes, so a tampered config cannot
// redirect the refresh token.
func refreshPerch(ctx context.Context, p *Params) (*TokenResult, error) {
	if p.RefreshToken == "" {
		return nil, fmt.Errorf("missing refresh token")
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	cfg, err := perchAuthConfig(ctx, client)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string]string{"refresh_token": p.RefreshToken})
	if err != nil {
		return nil, fmt.Errorf("perch refresh: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.SupabaseURL+"/auth/v1/token?grant_type=refresh_token", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("perch refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseAnonKey)
	req.Header.Set("User-Agent", providers.PerchCLIUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perch refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("perch refresh: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("perch refresh returned %d: %s", resp.StatusCode, truncateForError(raw))
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &tokenData); err != nil {
		return nil, fmt.Errorf("perch refresh: decode token response: %w", err)
	}
	if tokenData.AccessToken == "" {
		return nil, fmt.Errorf("perch refresh returned no access_token")
	}
	newRefresh := tokenData.RefreshToken
	if newRefresh == "" {
		newRefresh = p.RefreshToken
	}
	expiresIn := tokenData.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return &TokenResult{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: newRefresh,
		ExpiresIn:    expiresIn,
	}, nil
}

// perchAuthConfig reads GET /api/perch-terminal/cli-auth/config and checks it
// names the Supabase project Perch publishes, before any credential is sent.
func perchAuthConfig(ctx context.Context, client *http.Client) (*perchConfig, error) {
	code, raw, err := doGet(ctx, client, providers.PerchAppURL+"/api/perch-terminal/cli-auth/config")
	if err != nil {
		return nil, fmt.Errorf("perch auth config: %w", err)
	}
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("perch auth config returned %d", code)
	}
	var cfg perchConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("perch auth config: decode response: %w", err)
	}
	if strings.TrimSuffix(cfg.SupabaseURL, "/") != providers.PerchSupabaseURL {
		return nil, fmt.Errorf("perch auth config: unexpected Supabase project %q", cfg.SupabaseURL)
	}
	if cfg.SupabaseAnonKey == "" || len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("perch auth config: incomplete")
	}
	return &cfg, nil
}

type perchConfig struct {
	SupabaseURL     string   `json:"supabaseUrl"`
	SupabaseAnonKey string   `json:"supabaseAnonKey"`
	Providers       []string `json:"providers"`
}

// doGet performs one GET and returns the status plus a capped body.
func doGet(ctx context.Context, client *http.Client, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providers.PerchCLIUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func truncateForError(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}
