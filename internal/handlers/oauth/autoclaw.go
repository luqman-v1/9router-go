package oauth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"

	"9router/proxy/internal/proxy"
)

// AutoClaw's desktop client signs every model request with a client-side
// app key (KNc.appKey in main.cjs, build 2.6.1). It is a client identifier,
// not an account credential: the value is captured from the installed bundle
// and is required for the MD5 envelope x-auth-sign. It replaces the old
// operator-pasted third field so an OAuth login alone is enough.
const autoclawOAuthAppKey = "38d2391985e2369a5fb8227d8e6cd5e5"

// autoclawSign computes the client's MD5 envelope: md5(appID&ts&appKey).
func autoclawSign(ts string) string {
	sum := md5.Sum([]byte("100003&" + ts + "&" + autoclawOAuthAppKey))
	return hex.EncodeToString(sum[:])
}

// AutoClaw answers zai-oauth-url without a captcha proof with code 631002
// and a misleading "version unsupported" message. A bad proof yields 630014.
const autoclawCaptchaGateCode = 631002

var autoclawOAuthOrigin = "https://autoglm-api.autoglm.ai"

// autoclawOAuthPost mirrors the desktop client's identity_gateway calls:
//
//	POST /userapi/overseasv1/zai-oauth-url   {navigate_uri, source_id, device_id}
//	POST /userapi/overseasv1/zai-oauth-login {code, state, navigate_uri, source_id, device_id}
func autoclawOAuthPost(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("autoclaw oauth: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, autoclawOAuthOrigin+path, strings.NewReader(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("autoclaw oauth: build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// The identity gateway requires the desktop client's MD5 envelope on
	// every call, including the OAuth endpoints. Without it the gateway
	// answers business code 400002 "Middleware sign error".
	ts := fmt.Sprint(time.Now().Unix())
	req.Header.Set("X-Auth-Appid", "100003")
	req.Header.Set("X-Auth-TimeStamp", ts)
	req.Header.Set("X-Auth-Sign", autoclawSign(ts))
	req.Header.Set("X-Product", "autoclaw")
	req.Header.Set("X-Version", "2.6.1")
	req.Header.Set("X-Tm", "win")
	req.Header.Set("X-Lang", "zh-CN")

	resp, err := proxy.NewDirectSafeTransport().RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("autoclaw oauth: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("autoclaw oauth: read: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(rawResp, &out); err != nil {
		// The WAF answers Go's default UA with 405 + an HTML page; with a
		// browser UA we always get the JSON envelope. Keep the diagnostic
		// short and note the likely cause.
		return nil, fmt.Errorf("autoclaw oauth: non-JSON response (HTTP %d, %s); the gateway WAF may be blocking the request", resp.StatusCode, firstN(string(rawResp), 60))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("autoclaw oauth: HTTP %d: %s", resp.StatusCode, firstN(string(rawResp), 200))
	}
	if code, _ := out["code"].(float64); code != 0 {
		if code == autoclawCaptchaGateCode {
			return nil, fmt.Errorf("autoclaw oauth: upstream requires an Aliyun captcha proof (code 631002 is a decoy 'version unsupported' message); sign in from the AutoClaw desktop app instead")
		}
		if code == 630014 {
			return nil, fmt.Errorf("autoclaw oauth: captcha challenge required — the Aliyun slider widget must run in a browser")
		}
		return nil, fmt.Errorf("autoclaw oauth: business code %v", out["code"])
	}
	return out, nil
}

// HandleAutoclawAuthorize begins the Z.ai OAuth flow.
// GET /api/oauth/autoclaw/authorize?redirect_uri=...&state=...
func (h *OAuthHandler) HandleAutoclawAuthorize(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}
	redirectURI := callbackRedirectURI(r)
	deviceID := zcodeUUID()
	data, err := autoclawOAuthPost(r.Context(), "/userapi/overseasv1/zai-oauth-url", map[string]any{
		"navigate_uri": redirectURI,
		"source_id":    "autoclaw",
		"device_id":    deviceID,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	payload, _ := data["data"].(map[string]any)
	authURL, _ := payload["oauth_url"].(string)
	if authURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "autoclaw oauth: missing oauth_url")
		return
	}
	if returned, _ := payload["state"].(string); returned != "" {
		state = returned
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url": authURL, "authUrl": authURL, "state": state,
		"redirectUri": redirectURI, "flowType": "authorization_code", "provider": "autoclaw",
		"deviceId": deviceID,
	})
}

// HandleAutoclawExchange exchanges the callback code and stores one
// OAuth-backed connection. No API key field is involved: the executor reads
// refresh_token from the credential and signs with the constant app key.
// POST /api/oauth/autoclaw/exchange
func (h *OAuthHandler) HandleAutoclawExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code        string `json:"code"`
		State       string `json:"state"`
		RedirectURI string `json:"redirectUri"`
		DeviceID    string `json:"deviceId"`
		Name        string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if body.Code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
		return
	}
	redirectURI := exchangeRedirectURI(r, body.RedirectURI, "")
	data, err := autoclawOAuthPost(r.Context(), "/userapi/overseasv1/zai-oauth-login", map[string]any{
		"code":         body.Code,
		"state":        body.State,
		"navigate_uri": redirectURI,
		"source_id":    "autoclaw",
		"device_id":    body.DeviceID,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	payload, _ := data["data"].(map[string]any)
	accessToken, _ := payload["access_token"].(string)
	refreshToken, _ := payload["refresh_token"].(string)
	userID, _ := payload["user_id"].(string)
	if accessToken == "" || refreshToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "autoclaw oauth: missing tokens")
		return
	}

	// Durable credential for the request-path executor:
	// refresh_token|device_id|app_key (the app key is the constant above).
	apiKey := refreshToken + "|" + body.DeviceID + "|" + autoclawOAuthAppKey
	id := "autoclaw-" + shortHash(accessToken)
	dataMap := map[string]any{
		"apiKey":       apiKey,
		"accessToken":  accessToken,
		"refreshToken": refreshToken,
		"providerSpecificData": map[string]any{
			"authMethod": "oauth",
			"deviceId":   body.DeviceID,
			"userId":     userID,
		},
		"expiresAt": time.Now().Add(20 * time.Hour).UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "encode connection data")
		return
	}
	name := connectionDisplayName("autoclaw", body.Name, userID, "")
	if err := h.Repo.CreateProviderConnectionFull(r.Context(), id, "autoclaw", "oauth", name, nil, string(raw)); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "save connection: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "connectionId": id, "provider": "autoclaw"})
}
