package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

func init() {
	Register("kiro", refreshKiro)
}

// Hourly rotating Kiro SDK identity (upstream kiroModels.js parity).
var kiroSocialUA = "kiro-cli/1.0.0"

// kiroRegionPattern guards the region interpolated into the IDC token URL.
var kiroRegionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z]+-[0-9]+$`)

// refreshKiro ports upstream refreshKiroToken (open-sse/services/tokenRefresh/providers.js):
// AWS SSO OIDC device-code tokens refresh via the per-account clientId/clientSecret
// against https://oidc.<region>.amazonaws.com/token (camelCase JSON contract);
// Kiro social tokens refresh via the desktop refresh endpoint (upstream tokenUrl).
// External IdP is not ported — no Go-side config exists for it yet.
func refreshKiro(ctx context.Context, p *Params) (*TokenResult, error) {
	if p.RefreshToken == "" {
		return nil, fmt.Errorf("kiro: refresh_token is required")
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	if psd := p.ProviderSpecificData; psd != nil {
		if cid, cs := strings.TrimSpace(psd["clientId"]), strings.TrimSpace(psd["clientSecret"]); cid != "" && cs != "" {
			return refreshKiroAWS(ctx, client, p.RefreshToken, cid, cs, strings.TrimSpace(psd["region"]))
		}
	}
	return refreshKiroSocial(ctx, client, p.RefreshToken)
}

// refreshKiroAWS refreshes an AWS SSO OIDC token via the per-account client
// credentials stored at login time (upstream kiro device branch).
func refreshKiroAWS(ctx context.Context, client *http.Client, refreshToken, clientID, clientSecret, region string) (*TokenResult, error) {
	if region == "" {
		region = "us-east-1"
	}
	if !kiroRegionPattern.MatchString(region) {
		return nil, fmt.Errorf("kiro: invalid region %q", region)
	}

	reqBody, err := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	})
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oidc."+region+".amazonaws.com/token", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("kiro: create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("kiro: read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kiro: %w", &providers.OAuthRefreshError{Status: resp.StatusCode, Body: truncateBody(body)})
	}

	var tokens struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("kiro: parse refresh response: %w", err)
	}
	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("kiro: empty access token in refresh response")
	}

	out := &TokenResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	return out, nil
}

// refreshKiroSocial refreshes a Kiro social token via the desktop refresh
// endpoint (upstream PROVIDERS.kiro.tokenUrl).
func refreshKiroSocial(ctx context.Context, client *http.Client, refreshToken string) (*TokenResult, error) {
	reqBody, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal social refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("kiro: create social refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", kiroSocialUA)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: social refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("kiro: read social refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kiro: social %w", &providers.OAuthRefreshError{Status: resp.StatusCode, Body: truncateBody(body)})
	}

	var tokens struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("kiro: parse social refresh response: %w", err)
	}
	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("kiro: empty access token in social refresh response")
	}

	out := &TokenResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	return out, nil
}
