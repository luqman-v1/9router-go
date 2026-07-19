package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// xaiClientID is the public OAuth client ID for xAI.
const xaiClientID = "b1a00492-073a-47ea-816f-4c329264a828"
const xaiTokenURL = "https://auth.x.ai/oauth2/token"

func init() {
	Register("xai", refreshXAI)
}

// refreshXAI refreshes an xAI token using standard OAuth2 refresh_token grant.
// xAI uses a public client (no client_secret needed).
func refreshXAI(p *Params) (*TokenResult, error) {
	vals := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {xaiClientID},
		"refresh_token": {p.RefreshToken},
	}

	req, err := http.NewRequest("POST", xaiTokenURL, strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, fmt.Errorf("xAI create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xAI POST: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("xAI read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xAI refresh returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token,omitempty"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("xAI parse: %w", err)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("xAI empty access token")
	}

	return &TokenResult{
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
	}, nil
}
