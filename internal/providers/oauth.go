package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"9router/proxy/internal/handlerutil"
)

// OAuthClientConfig holds OAuth app credentials for token refresh.
type OAuthClientConfig struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
}

// OAuthConnectionData holds the persisted OAuth fields inside ConnectionData.
type OAuthConnectionData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"` // RFC3339 / ISO 8601
	ProjectID    string `json:"projectId"`
}

// OAuthTokenResponse is the JSON response from a token refresh endpoint.
type OAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
}

// KnownOAuthConfigs maps provider IDs to their OAuth client configuration for token refresh.
// Client secrets should be set via environment variables.
var KnownOAuthConfigs = map[string]OAuthClientConfig{
	"antigravity": {
		ClientID:     envOr("ANTIGRAVITY_OAUTH_CLIENT_ID", "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"),
		ClientSecret: envOr("ANTIGRAVITY_OAUTH_CLIENT_SECRET", "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"),
		TokenURL:     "https://oauth2.googleapis.com/token",
	},
	"xai": {
		ClientID:     envOr("XAI_OAUTH_CLIENT_ID", "b1a00492-073a-47ea-816f-4c329264a828"),
		ClientSecret: envOr("XAI_OAUTH_CLIENT_SECRET", ""),
		TokenURL:     "https://auth.x.ai/oauth2/token",
	},
	"codex": {
		ClientID:     envOr("CODEX_OAUTH_CLIENT_ID", "app_EMoamEEZ73f0CkXaXp7hrann"),
		ClientSecret: envOr("CODEX_OAUTH_CLIENT_SECRET", ""),
		TokenURL:     "https://auth.openai.com/oauth/token",
	},
	"github": {
		ClientID:     envOr("GITHUB_OAUTH_CLIENT_ID", "Iv1.b507a08c87ecfe98"),
		ClientSecret: envOr("GITHUB_OAUTH_CLIENT_SECRET", ""),
		TokenURL:     "https://github.com/login/oauth/access_token",
	},
	"iflow": {
		ClientID:     envOr("IFLOW_OAUTH_CLIENT_ID", "10009311001"),
		ClientSecret: envOr("IFLOW_OAUTH_CLIENT_SECRET", "4Z3YjXycVsQvyGF1etiNlIBB4RsqSDtW"),
		TokenURL:     "https://iflow.cn/oauth/token",
	},
	"gemini-cli": {
		ClientID:     envOr("GEMINI_CLI_OAUTH_CLIENT_ID", "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"),
		ClientSecret: envOr("GEMINI_CLI_OAUTH_CLIENT_SECRET", "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"),
		TokenURL:     "https://oauth2.googleapis.com/token",
	},
	"kimi-coding": {
		ClientID:     envOr("KIMI_CODING_OAUTH_CLIENT_ID", ""),
		ClientSecret: envOr("KIMI_CODING_OAUTH_CLIENT_SECRET", ""),
		TokenURL:     "https://auth.kimi.com/api/oauth/token",
	},
	"qwen": {
		ClientID:     envOr("QWEN_OAUTH_CLIENT_ID", "f0304373b74a44d2b584a3fb70ca9e56"),
		ClientSecret: envOr("QWEN_OAUTH_CLIENT_SECRET", ""),
		TokenURL:     "https://chat.qwen.ai/api/v1/oauth2/token",
	},
	"qoder": {
		ClientID:     envOr("QODER_OAUTH_CLIENT_ID", ""),
		ClientSecret: envOr("QODER_OAUTH_CLIENT_SECRET", ""),
		TokenURL:     "https://center.qoder.sh/algo/api/v3/user/refresh_token",
	},
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// RefreshToken performs an OAuth token refresh for the given provider configuration.
// Returns the new access token and expires_in duration.
func RefreshToken(cfg OAuthClientConfig, refreshToken string) (*OAuthTokenResponse, error) {
	values := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	resp, err := http.PostForm(cfg.TokenURL, values)
	if err != nil {
		return nil, fmt.Errorf("OAuth refresh POST: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read OAuth response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth token refresh returned %d: %s", resp.StatusCode, string(body))
	}

	return ParseRefreshResponse(body)
}

// ParseOAuthConnection extracts OAuth fields from a ConnectionData blob.
func ParseOAuthConnection(data map[string]interface{}) *OAuthConnectionData {
	if data == nil {
		return nil
	}
	return &OAuthConnectionData{
		AccessToken:  handlerutil.GetString(data, "accessToken"),
		RefreshToken: handlerutil.GetString(data, "refreshToken"),
		ExpiresAt:    handlerutil.GetString(data, "expiresAt"),
		ProjectID:    handlerutil.GetString(data, "projectId"),
	}
}

// IsExpired checks if the OAuth token is expired or will expire within 60 seconds.
func (o *OAuthConnectionData) IsExpired() bool {
	if o.ExpiresAt == "" {
		return true
	}
	exp, err := time.Parse(time.RFC3339, o.ExpiresAt)
	if err != nil {
		return true
	}
	return time.Now().After(exp.Add(-60 * time.Second))
}

// BuildRefreshPayload builds the URL-encoded form body for a token refresh request.
func (c *OAuthClientConfig) BuildRefreshPayload(refreshToken string) string {
	return url.Values{
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}.Encode()
}

// ParseRefreshResponse parses the JSON response from a token refresh endpoint.
func ParseRefreshResponse(body []byte) (*OAuthTokenResponse, error) {
	var resp OAuthTokenResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse OAuth response: %w", err)
	}
	if resp.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in OAuth response")
	}
	return &resp, nil
}

// BuildConnectionUpdate builds a partial ConnectionData map for DB update.
func (r *OAuthTokenResponse) BuildConnectionUpdate() map[string]interface{} {
	return map[string]interface{}{
		"accessToken": r.AccessToken,
		"expiresAt":   time.Now().Add(time.Duration(r.ExpiresIn) * time.Second).Format(time.RFC3339),
	}
}
