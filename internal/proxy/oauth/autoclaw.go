package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	Register("autoclaw", refreshAutoclaw)
}

// Autoclaw refresh endpoint constants. The refresh grant is not signed with
// the desktop client's app key (the refresh call carries the bearer token
// only), so no app secret is needed here — and none is kept in this file.
const (
	autoclawRefreshPath = "/userapi/v1/refresh"
	autoclawSourceID    = "autoclaw"
)

// autoclawBaseURL is a var so tests can point it at an httptest server.
// Production value is the real upstream host.
var autoclawBaseURL = "https://autoglm-api.autoglm.ai"

// SetAutoclawBaseURLForTest overrides the refresh endpoint base URL and
// returns the previous value, so tests can restore it. It exists solely to
// let the executor package's tests drive the refresher against a fake server
// without real credentials or network.
func SetAutoclawBaseURLForTest(url string) (old string) {
	old = autoclawBaseURL
	autoclawBaseURL = url
	return
}

// refreshAutoclaw rotates an Autoclaw access token from its durable refresh
// token + device_id. One login supplies both; this function keeps them alive
// for the lifetime of the connection.
//
// The rotation is lazy, not proactive: the executor calls this on the request
// path (dedup-cached for 10s), and the background refresher
// (internal/proxy/oauth/background.go) never selects Autoclaw connections —
// it only picks authType "oauth" rows carrying a refreshToken + expiresAt,
// while an Autoclaw row keeps its credential in apiKey
// (`refresh_token|device_id|app_key`). A connection that sits idle therefore expires
// upstream and is re-rotated on its next request.
//
// The refresh endpoint signs the request with the current access token when
// one is present; it tolerates a missing/expired bearer for the refresh
// grant itself.
func refreshAutoclaw(ctx context.Context, p *Params) (*TokenResult, error) {
	if p.RefreshToken == "" {
		return nil, fmt.Errorf("autoclaw: refresh_token is required")
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	deviceID := ""
	if p.ProviderSpecificData != nil {
		deviceID = strings.TrimSpace(p.ProviderSpecificData["device_id"])
	}

	body := map[string]string{
		"source_id":     autoclawSourceID,
		"device_id":     deviceID,
		"refresh_token": p.RefreshToken,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("autoclaw: marshal refresh body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, autoclawBaseURL+autoclawRefreshPath, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("autoclaw: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("autoclaw: refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("autoclaw: read refresh response: %w", err)
	}

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return nil, fmt.Errorf("autoclaw: parse refresh response: %w", err)
	}
	if envelope.Code != 0 || envelope.Data.AccessToken == "" {
		return nil, fmt.Errorf("autoclaw: refresh failed: code=%d body=%s", envelope.Code, rawBody)
	}

	newRT := envelope.Data.RefreshToken
	if newRT == "" {
		newRT = p.RefreshToken // upstream echoes the same refresh token; keep ours
	}

	// ~20h upstream access-token lifetime. Nothing watches it proactively:
	// the background refresher does not select Autoclaw connections, so the
	// executor re-rotates on the next request instead (dedup-cached 10s).
	return &TokenResult{
		AccessToken:  envelope.Data.AccessToken,
		RefreshToken: newRT,
		ExpiresIn:    20 * 60 * 60,
	}, nil
}
