package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const claudeTokenURL = "https://api.anthropic.com/v1/oauth/token"
const claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

func init() {
	Register("claude", refreshClaude)
}

// refreshClaude refreshes an Anthropic Claude OAuth token.
// Claude uses JSON body encoding for refresh (not form-urlencoded).
func refreshClaude(ctx context.Context, p *Params) (*TokenResult, error) {
	body := map[string]interface{}{
		"grant_type":    "refresh_token",
		"client_id":     claudeClientID,
		"refresh_token": p.RefreshToken,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("claude marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", claudeTokenURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("claude create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude POST: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("claude read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claude refresh returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("claude parse: %w", err)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("claude empty access token")
	}

	return &TokenResult{
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
		Scope:       result.Scope,
	}, nil
}
