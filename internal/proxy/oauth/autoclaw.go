package oauth

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
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

// Every identity_gateway call, including refresh, requires the desktop
// client's MD5 envelope. A signed request with an unknown token answers
// 400000 "not logged in"; an unsigned one answers 400002 "sign error".
const (
	autoclawAppID     = "100003"
	autoclawAppKey    = "38d2391985e2369a5fb8227d8e6cd5e5"
	autoclawClientVer = "2.6.1"
)

func autoclawSign(ts string) string {
	sum := md5.Sum([]byte(autoclawAppID + "&" + ts + "&" + autoclawAppKey))
	return hex.EncodeToString(sum[:])
}

// AutoClaw tokens already carry the Bearer prefix. Avoid emitting
// "Bearer Bearer <jwt>", which upstream rejects as an invalid token.
func bearerValue(token string) string {
	if strings.HasPrefix(strings.TrimSpace(token), "Bearer ") {
		return token
	}
	return "Bearer " + token
}

const (
	autoclawRefreshPath      = "/userapi/v1/refresh"
	autoclawAgentRefreshPath = "/userapi/v1/agent-refresh"
	autoclawSourceID         = "autoclaw"
	autoclawRefreshCodeAgent = 400002 // desktop client retries the agent endpoint on this code
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

	deviceID, baseURL := "", autoclawBaseURL
	if p.ProviderSpecificData != nil {
		deviceID = strings.TrimSpace(p.ProviderSpecificData["device_id"])
		if custom := strings.TrimSpace(p.ProviderSpecificData["base_url"]); custom != "" {
			baseURL = strings.TrimRight(custom, "/")
		}
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

	// The desktop client (v2.6.1, r1n.refresh) falls back to the agent
	result, code, err := requestAutoclawRefresh(ctx, client, baseURL, autoclawRefreshPath, raw, p.AccessToken)
	if err != nil {
		return nil, err
	}
	if code == autoclawRefreshCodeAgent {
		result, _, err = requestAutoclawRefresh(ctx, client, baseURL, autoclawAgentRefreshPath, raw, p.AccessToken)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func requestAutoclawRefresh(ctx context.Context, client *http.Client, baseURL, path string, raw []byte, accessToken string) (*TokenResult, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, fmt.Errorf("autoclaw: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	ts := fmt.Sprint(time.Now().Unix())
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("X-Auth-Appid", autoclawAppID)
	req.Header.Set("X-Auth-TimeStamp", ts)
	req.Header.Set("X-Auth-Sign", autoclawSign(ts))
	req.Header.Set("X-Product", "autoclaw")
	req.Header.Set("X-Version", autoclawClientVer)
	req.Header.Set("X-Tm", "win")
	req.Header.Set("X-Lang", "zh-CN")
	if accessToken != "" {
		req.Header.Set("Authorization", bearerValue(accessToken))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("autoclaw: refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, 0, fmt.Errorf("autoclaw: read refresh response: %w", err)
	}

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return nil, 0, fmt.Errorf("autoclaw: parse refresh response: %w", err)
	}
	if envelope.Code == autoclawRefreshCodeAgent {
		return nil, envelope.Code, nil
	}
	if envelope.Code != 0 || envelope.Data.AccessToken == "" || strings.TrimSpace(envelope.Data.RefreshToken) == "" {
		return nil, envelope.Code, fmt.Errorf("autoclaw: refresh failed: code=%d body=%s", envelope.Code, rawBody)
	}
	// The desktop client requires a fresh refresh_token on every rotation
	// ($bd in main.cjs checks both fields). A missing one is a hard failure:
	// silently reusing the stale token would break the next refresh.
	return &TokenResult{
		AccessToken:  envelope.Data.AccessToken,
		RefreshToken: envelope.Data.RefreshToken,
		ExpiresIn:    20 * 60 * 60,
	}, 0, nil
}
