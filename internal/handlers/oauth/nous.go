package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"9router/proxy/internal/providers"
)

var (
	nousDeviceCodeURL = providers.NousDeviceCodeURL
	nousTokenURL      = providers.NousTokenURL
	nousAgentKeyURL   = providers.NousAgentKeyURL
)

const (
	nousOAuthClientID   = "hermes-cli"
	nousOAuthScope      = "inference:invoke inference:mint_agent_key"
	nousDeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
)

func nousStart() (map[string]any, error) {
	form := url.Values{
		"client_id": {nousOAuthClientID},
		"scope":     {nousOAuthScope},
	}

	data, status, err := postForm(nousDeviceCodeURL, form, nil)
	if err != nil {
		return nil, fmt.Errorf("nous device code request failed: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("nous device code request failed: HTTP %d %s", status, strVal(data, "error", "error_description", "message"))
	}

	deviceCode := strVal(data, "device_code")
	userCode := strVal(data, "user_code")
	verificationURI := strVal(data, "verification_uri")
	if verificationURI == "" {
		verificationURI = strVal(data, "verification_url")
	}
	verificationURIComplete := strVal(data, "verification_uri_complete")
	if verificationURIComplete == "" {
		verificationURIComplete = verificationURI
	}
	if deviceCode == "" || userCode == "" || verificationURI == "" {
		return nil, fmt.Errorf("nous device code response unusable (HTTP %d)", status)
	}

	expiresIn := positiveIntOr(data, "expires_in", 900)
	interval := positiveIntOr(data, "interval", 5)

	return map[string]any{
		"device_code":               deviceCode,
		"user_code":                 userCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURIComplete,
		"expires_in":                expiresIn,
		"interval":                  interval,
	}, nil
}

func nousPoll(code string) (deviceTokens, error) {
	var t deviceTokens
	if code == "" {
		return t, fmt.Errorf("authorization_pending")
	}

	form := url.Values{
		"grant_type":  {nousDeviceGrantType},
		"client_id":   {nousOAuthClientID},
		"device_code": {code},
	}

	data, status, err := postForm(nousTokenURL, form, nil)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %w", err)
	}

	if e := strVal(data, "error"); e != "" {
		if e == "authorization_pending" || e == "slow_down" {
			return t, fmt.Errorf("%s", e)
		}
		return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
	}
	if status < 200 || status >= 300 {
		return t, fmt.Errorf("unexpected HTTP status: %d", status)
	}

	t.access = strVal(data, "access_token")
	t.refresh = strVal(data, "refresh_token")
	if t.access == "" {
		return t, fmt.Errorf("no access_token in the Nous token reply")
	}
	t.expiresIn = positiveIntOr(data, "expires_in", 3600)

	// Mint agent key with fallback to access_token
	agentKey, agentKeyID, agentKeyExpiresAt := nousMintAgentKey(context.Background(), t.access)
	apiKey := t.access
	if agentKey != "" {
		apiKey = agentKey
	}

	extra := map[string]any{
		"apiKey":           apiKey,
		"inferenceBaseUrl": providers.NousInferenceBaseURL,
	}
	if agentKeyID != "" {
		extra["agentKeyId"] = agentKeyID
	}
	if agentKeyExpiresAt != "" {
		extra["agentKeyExpiresAt"] = agentKeyExpiresAt
	}
	t.extra = extra

	return t, nil
}

func nousMintAgentKey(ctx context.Context, accessToken string) (string, string, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload := map[string]any{
		"min_ttl_seconds": 7200,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", "", ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nousAgentKeyURL, bytes.NewReader(raw))
	if err != nil {
		return "", "", ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := deviceClient.Do(req)
	if err != nil {
		return "", "", ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", ""
	}

	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", ""
	}

	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return "", "", ""
	}

	apiKey := strVal(data, "api_key")
	keyID := strVal(data, "key_id")
	expiresAt := strVal(data, "expires_at")

	return apiKey, keyID, expiresAt
}
