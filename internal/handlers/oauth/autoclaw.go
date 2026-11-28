package oauth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

// The proof the desktop client obtains from the Aliyun captcha SDK is sent
// upstream as ali_captcha_verify_param; the dashboard obtains it the same way.
const autoclawCaptchaChallengeCode = 630014

// AutoClaw answers oauth-url without a captcha proof with code 631002 and a
const autoclawCaptchaGateCode = 631002

// The config endpoint tells the dashboard whether a challenge is required and
// which Aliyun scene to instantiate. The desktop client calls it before every
// authorize attempt; proof can be silent (risk-based V3), so the UI must not
// assume a visible slider.
const autoclawCaptchaConfigPath = "/userapi/overseasv1/oauth-captcha-config"

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
			return nil, fmt.Errorf("autoclaw oauth: upstream requires an Aliyun captcha proof (code 631002 is a decoy 'version unsupported' message); complete the verification in the browser")
		}
		if code == autoclawCaptchaChallengeCode {
			return nil, fmt.Errorf("autoclaw oauth: captcha challenge required — complete the verification in the browser")
		}
		return nil, fmt.Errorf("autoclaw oauth: business code %v", out["code"])
	}
	return out, nil
}

// autoclawUserEmail fetches the account email after OAuth login. The login
// response carries only user_id; the desktop client reads the email from
// /userapi/v1/user-profile. A failure is not fatal: the connection still
// works, just labelled with the user id.
func autoclawUserEmail(ctx context.Context, deviceID, accessToken string) string {
	raw, err := json.Marshal(map[string]any{"source_id": "autoclaw", "device_id": deviceID})
	if err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, autoclawOAuthOrigin+"/userapi/v1/user-profile", strings.NewReader(string(raw)))
	if err != nil {
		return ""
	}
	ts := fmt.Sprint(time.Now().Unix())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "node")
	req.Header.Set("X-Auth-Appid", "100003")
	req.Header.Set("X-Auth-TimeStamp", ts)
	req.Header.Set("X-Auth-Sign", autoclawSign(ts))
	req.Header.Set("X-Product", "autoclaw")
	req.Header.Set("X-Version", "2.6.1")
	req.Header.Set("X-Tm", "win")
	req.Header.Set("X-Lang", "en")
	req.Header.Set("Authorization", accessToken)
	resp, err := proxy.NewDirectSafeTransport().RoundTrip(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil || out["code"] != float64(0) {
		return ""
	}
	payload, _ := out["data"].(map[string]any)
	email, _ := payload["email"].(string)
	return strings.TrimSpace(email)
}

// HandleAutoclawCaptchaConfig exposes the upstream Aliyun captcha settings so
// the dashboard can initialise the same SDK the desktop app uses. GET
// /api/oauth/autoclaw/captcha-config
func (h *OAuthHandler) HandleAutoclawCaptchaConfig(w http.ResponseWriter, r *http.Request) {
	data, err := autoclawOAuthPost(r.Context(), autoclawCaptchaConfigPath, map[string]any{})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	payload, _ := data["data"].(map[string]any)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"enabled": payload["enabled"] == true,
		"region":  payload["region"],
		"prefix":  payload["prefix"],
		"sceneId": payload["scene_id"],
	})
}

// HandleAutoclawAuthorize begins the Z.ai OAuth flow. The dashboard runs the
// same Aliyun captcha SDK as the desktop client and sends the resulting proof
// as ?captcha_proof=; the gateway forwards it as ali_captcha_verify_param.
// GET /api/oauth/autoclaw/authorize?redirect_uri=...&state=...
func (h *OAuthHandler) HandleAutoclawAuthorize(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}
	// chat.z.ai accepts only the redirect URI registered for the AutoClaw
	// OAuth client. Bind it for the duration of the login and relay the code
	// into the dashboard's callback poller.
	if err := autoclawLoopback.start(); err != nil {
		handlerutil.WriteJSONError(w, http.StatusConflict, "autoclaw oauth: local callback listener: "+err.Error())
		return
	}
	redirectURI := "http://" + autoclawLoopbackHost + ":" + strconv.Itoa(autoclawLoopbackPort) + autoclawLoopbackPath
	deviceID := zcodeUUID()
	payload := map[string]any{
		"navigate_uri": redirectURI,
		"source_id":    "autoclaw",
		"device_id":    deviceID,
	}
	if proof := strings.TrimSpace(r.URL.Query().Get("captcha_proof")); proof != "" {
		payload["ali_captcha_verify_param"] = proof
	}
	data, err := autoclawOAuthPost(r.Context(), "/userapi/overseasv1/zai-oauth-url", payload)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	respPayload, _ := data["data"].(map[string]any)
	authURL, _ := respPayload["oauth_url"].(string)
	if authURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "autoclaw oauth: missing oauth_url")
		return
	}
	if returned, _ := respPayload["state"].(string); returned != "" {
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
	// The login response carries only user_id. Read the email from the
	// profile endpoint so the connection is labelled with the account the
	// operator actually signed in with.
	if email := autoclawUserEmail(r.Context(), body.DeviceID, accessToken); email != "" {
		dataMap["email"] = email
	}
	raw, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "encode connection data")
		return
	}
	name := connectionDisplayName("autoclaw", body.Name, userID, "")
	if email, _ := dataMap["email"].(string); email != "" {
		name = email
	}
	if err := h.Repo.CreateProviderConnectionFull(r.Context(), id, "autoclaw", "oauth", name, nil, string(raw)); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "save connection: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "connectionId": id, "provider": "autoclaw"})
}
