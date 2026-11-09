package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	Register("zcode", RefreshZcode)
}

// zcodeOAuthBase is the Z.ai OAuth endpoint.
const zcodeOAuthBase = "https://zcode.z.ai"

// RefreshZcode refreshes a ZCode plan JWT using the stored device-flow refresh
// token. Port of bridge auth.js:refreshPlanJwt.
func RefreshZcode(ctx context.Context, p *Params) (*TokenResult, error) {
	return refreshZcodeJWT(ctx, p, zcodeOAuthBase)
}

func refreshZcodeJWT(ctx context.Context, p *Params, oauthBase string) (*TokenResult, error) {
	if oauthBase == "" {
		oauthBase = zcodeOAuthBase
	}
	rt := strings.TrimSpace(p.RefreshToken)
	if rt == "" {
		return nil, fmt.Errorf("zcode: no refresh token stored; re-login via the dashboard")
	}

	tokenURL := strings.TrimRight(oauthBase, "/") + "/api/v1/oauth/token"

	payload, err := json.Marshal(map[string]any{
		"provider":      "zai",
		"grant_type":    "refresh_token",
		"refresh_token": rt,
	})
	if err != nil {
		return nil, fmt.Errorf("zcode: marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("zcode: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zcode: refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Data *struct {
			Token string         `json:"token"`
			User  map[string]any `json:"user"`
			Zai   *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"zai"`
			Bigmodel *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"bigmodel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Data == nil || resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("zcode: refresh rejected (re-login required): status=%d body=%s", resp.StatusCode, firstN(string(body), 300))
	}

	data := out.Data
	jwt := strings.TrimSpace(data.Token)
	if jwt == "" {
		return nil, fmt.Errorf("zcode: refresh response missing plan JWT")
	}

	var newAccess, newRefresh string
	if data.Zai != nil {
		newAccess = data.Zai.AccessToken
		newRefresh = data.Zai.RefreshToken
	} else if data.Bigmodel != nil {
		newAccess = data.Bigmodel.AccessToken
		newRefresh = data.Bigmodel.RefreshToken
	}
	if newRefresh == "" {
		newRefresh = rt // keep original if server doesn't rotate
	}

	// The plan JWT carries no exp claim, so there is nothing to schedule from:
	// a token ends when the account's token_version is bumped (a re-login), and
	// that surfaces as a 401 on the next request. Report no expiry rather than
	// inventing one, so the refresher never re-runs a grant that cannot work.
	return &TokenResult{
		AccessToken:  jwt,
		RefreshToken: newRefresh,
		Scope:        newAccess, // piggyback the OAuth access token here
	}, nil
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
