package oauth

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/providers"
)

// deviceProviders lists providers supporting the device-code family.
// kimi-coding aliases to kimi (dual-auth merge, like upstream).
var deviceProviders = []string{
	"qoder", "qoder-cn", "kilocode", "grok-cli", "github", "kiro", "kimi", "kimi-coding",
	"codebuddy-cn", "codebuddy-intl", "muse",
}

func deviceSupported(p string) bool {
	for _, v := range deviceProviders {
		if v == p {
			return true
		}
	}
	return false
}

func deviceCanonical(p string) string {
	if p == "kimi-coding" {
		return "kimi"
	}
	return p
}

var awsRegionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d{1,2}$`)

// deviceClient is the HTTP client every device-code exchange uses.
//
// It wraps the environment transport in proxy.FallbackTransport for the same
// reason the chat path does (internal/proxy/fallback_transport.go): a sandbox
// or corporate HTTP(S)_PROXY refuses the CONNECT tunnel to provider hosts with
// 403 Forbidden, and Go renders that as `Get "https://…": Forbidden` — a
// transport error, not a status. A bare &http.Client{} has no direct-connection
// retry, so a Qoder login could open the login page and then fail every poll
// with exactly that message, leaving the modal stuck on "waiting".
var deviceClient = &http.Client{
	Transport: proxy.NewFallbackTransport(http.DefaultTransport),
	Timeout:   15 * time.Second,
}

func postJSON(url string, body any, headers map[string]string) (map[string]any, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := deviceClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("invalid JSON: %s", firstLine(string(out), 200))
	}
	return data, resp.StatusCode, nil
}

func postForm(url string, form url.Values, headers map[string]string) (map[string]any, int, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := deviceClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return map[string]any{"_raw": string(out)}, resp.StatusCode, nil
	}
	return data, resp.StatusCode, nil
}

func getJSON(url string, headers map[string]string) (map[string]any, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := deviceClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("invalid JSON")
	}
	return data, resp.StatusCode, nil
}

func firstLine(s string, n int) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func strVal(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// HandleDeviceStart begins a device-code flow.
// POST /api/oauth/device/start {"provider","region?","startUrl?","authMethod?"}
func (h *OAuthHandler) HandleDeviceStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider   string `json:"provider"`
		Region     string `json:"region"`
		StartURL   string `json:"startUrl"`
		AuthMethod string `json:"authMethod"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	provider := deviceCanonical(body.Provider)
	if !deviceSupported(body.Provider) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported provider for device flow")
		return
	}
	out, err := deviceStart(provider, body.Region, body.StartURL, body.AuthMethod)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	out["provider"] = body.Provider
	handlerutil.WriteJSON(w, http.StatusOK, out)
}

func deviceStart(provider, region, startURL, authMethod string) (map[string]any, error) {
	switch provider {
	case "qoder", "qoder-cn":
		return qoderStart(provider)
	case "kilocode":
		return kilocodeStart()
	case "grok-cli":
		return grokcliStart()
	case "github":
		return githubStart()
	case "kiro":
		return kiroStart(region, startURL, authMethod)
	case "kimi":
		return kimiStart()
	case "codebuddy-cn", "codebuddy-intl":
		return codebuddyStart(provider)
	case "muse":
		return museStart()
	default:
		return nil, fmt.Errorf("unsupported provider")
	}
}

// --- qoder: local PKCE + nonce, poll openapi.qoder.sh ---

// qoderLoginURL is the account-selection page for the provider's own
// deployment; Qoder CN signs in on qoder.com.cn.
func qoderLoginURL(provider string) string {
	if provider == "qoder-cn" {
		return "https://qoder.com.cn/device/selectAccounts"
	}
	return "https://qoder.com/device/selectAccounts"
}

func qoderStart(provider string) (map[string]any, error) {
	verifier := pkceVerifier()
	challenge := sha256Base64(verifier)
	nonce, machineID := randomUUID(), randomUUID()
	loginURL := qoderLoginURL(provider)
	p := url.Values{
		"challenge": {challenge}, "challenge_method": {"S256"},
		"machine_id": {machineID}, "nonce": {nonce},
	}
	return map[string]any{
		"device_code": nonce, "user_code": strings.ToUpper(firstN(nonce, 8)),
		"verification_uri":          loginURL,
		"verification_uri_complete": loginURL + "?" + p.Encode(),
		"expires_in":                300, "interval": 2,
		"session": map[string]any{"nonce": nonce, "verifier": verifier, "machineId": machineID},
	}, nil
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// --- kilocode: POST initiate, GET poll ---

func kilocodeStart() (map[string]any, error) {
	data, status, err := postJSON("https://api.kilo.ai/api/device-auth/codes", map[string]string{}, nil)
	if err != nil {
		return nil, fmt.Errorf("device auth initiation failed: %v", err)
	}
	if status == http.StatusTooManyRequests {
		return nil, fmt.Errorf("too many pending authorization requests, try again later")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("device auth initiation failed: status %d", status)
	}
	return map[string]any{
		"device_code": strVal(data, "code"), "user_code": strVal(data, "code"),
		"verification_uri":          strVal(data, "verificationUrl"),
		"verification_uri_complete": strVal(data, "verificationUrl"),
		"expires_in":                300, "interval": 3,
		"session": map[string]any{},
	}, nil
}

// --- grok-cli: standard device flow, referrer=grok-build ---

var grokcliUA = providers.GrokCLIPagerUserAgent

func grokcliStart() (map[string]any, error) {
	form := url.Values{
		"client_id": {"b1a00492-073a-47ea-816f-4c329264a828"},
		"scope":     {"openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"},
		"referrer":  {"grok-build"},
	}
	data, status, err := postForm("https://auth.x.ai/oauth2/device/code", form, map[string]string{"User-Agent": grokcliUA})
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("grok CLI device code request failed: %v status=%d", err, status)
	}
	data["session"] = map[string]any{}
	return data, nil
}

// --- github: standard device flow ---

func githubStart() (map[string]any, error) {
	form := url.Values{"client_id": {"Iv1.b507a08c87ecfe98"}, "scope": {"read:user"}}
	data, status, err := postForm("https://github.com/login/device/code", form, nil)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: %v status=%d", err, status)
	}
	data["session"] = map[string]any{}
	return data, nil
}

// --- kiro: AWS SSO OIDC register + device ---

func kiroStart(region, startURL, authMethod string) (map[string]any, error) {
	if strings.TrimSpace(region) == "" {
		region = "us-east-1"
	}
	if !awsRegionPattern.MatchString(region) {
		return nil, fmt.Errorf("invalid region")
	}
	if strings.TrimSpace(startURL) == "" {
		startURL = "https://view.awsapps.com/start"
	}
	if authMethod != "idc" {
		authMethod = "builder-id"
	}
	reg, status, err := postJSON("https://oidc."+region+".amazonaws.com/client/register", map[string]any{
		"clientName": "kiro-oauth-client", "clientType": "public",
		"scopes":     []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"},
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
		"issuerUrl":  "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6",
	}, nil)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("client registration failed: %v status=%d", err, status)
	}
	dev, status, err := postJSON("https://oidc."+region+".amazonaws.com/device_authorization", map[string]any{
		"clientId": strVal(reg, "clientId"), "clientSecret": strVal(reg, "clientSecret"), "startUrl": startURL,
	}, nil)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("device authorization failed: %v status=%d", err, status)
	}
	interval := 5
	if v, ok := dev["interval"].(float64); ok && v > 0 {
		interval = int(v)
	}
	return map[string]any{
		"device_code": strVal(dev, "deviceCode"), "user_code": strVal(dev, "userCode"),
		"verification_uri":          strVal(dev, "verificationUri"),
		"verification_uri_complete": strVal(dev, "verificationUriComplete"),
		"expires_in":                dev["expiresIn"],
		"interval":                  interval,
		"session": map[string]any{
			"clientId": strVal(reg, "clientId"), "clientSecret": strVal(reg, "clientSecret"),
			"region": region, "authMethod": authMethod, "startUrl": startURL,
		},
	}, nil
}

// --- kimi: device flow with X-Msh headers ---

func kimiHeaders(deviceID string) map[string]string {
	if strings.TrimSpace(deviceID) == "" {
		deviceID = randomUUID()
	}
	return map[string]string{
		"X-Msh-Platform": "9router", "X-Msh-Version": "1.0",
		"X-Msh-Device-Name": "unknown", "X-Msh-Device-Model": "server",
		"X-Msh-Device-Id": strings.TrimSpace(deviceID),
	}
}

func kimiStart() (map[string]any, error) {
	deviceID := randomUUID()
	form := url.Values{"client_id": {"17e5f671-d194-4dfb-9706-5516cb48c098"}}
	data, status, err := postForm("https://auth.kimi.com/api/oauth/device_authorization", form, kimiHeaders(deviceID))
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: %v status=%d", err, status)
	}
	data["session"] = map[string]any{"deviceId": deviceID}
	return data, nil
}

// --- codebuddy: state + authUrl, GET poll by state ---

func codebuddyVariant(provider string) (base, ua, domain, platform string) {
	if provider == "codebuddy-intl" {
		return "https://www.codebuddy.ai", "IDE/2.63.2 CodeBuddy/2.63.2", "www.codebuddy.ai", "ide"
	}
	return "https://copilot.tencent.com", "CLI/2.63.2 CodeBuddy/2.63.2", "copilot.tencent.com", "CLI"
}

func codebuddyStart(provider string) (map[string]any, error) {
	base, ua, domain, platform := codebuddyVariant(provider)
	headers := map[string]string{
		"User-Agent": ua, "X-Requested-With": "XMLHttpRequest",
		"X-Domain": domain, "X-No-Authorization": "true", "X-No-User-Id": "true", "X-Product": "SaaS",
	}
	data, status, err := postJSON(base+"/v2/plugin/auth/state?platform="+platform, map[string]string{}, headers)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("CodeBuddy state request failed: %v status=%d", err, status)
	}
	var code float64
	if c, ok := data["code"].(float64); ok {
		code = c
	}
	inner, _ := data["data"].(map[string]any)
	state, authURL := strVal(inner, "state"), strVal(inner, "authUrl")
	if code != 0 || state == "" || authURL == "" {
		return nil, fmt.Errorf("CodeBuddy state error: %v", strVal(data, "msg"))
	}
	return map[string]any{
		"device_code": state, "user_code": "",
		"verification_uri": authURL, "verification_uri_complete": authURL,
		"interval": 5, "session": map[string]any{},
	}, nil
}

// HandleDevicePoll polls once; frontend repeats until authorized.
// POST /api/oauth/device/poll {"provider","device_code","session"?}
func (h *OAuthHandler) HandleDevicePoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider   string         `json:"provider"`
		DeviceCode string         `json:"device_code"`
		Devicecode string         `json:"deviceCode"`
		Session    map[string]any `json:"session"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if !deviceSupported(body.Provider) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported provider for device flow")
		return
	}
	code := body.DeviceCode
	if code == "" {
		code = body.Devicecode
	}
	if code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing device_code")
		return
	}
	if body.Session == nil {
		body.Session = map[string]any{}
	}
	tokens, status, errMsg := devicePoll(deviceCanonical(body.Provider), code, body.Session)
	if errMsg != "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": status, "error": errMsg, "provider": body.Provider})
		return
	}
	if status != "authorized" || strings.TrimSpace(tokens.access) == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "pending", "provider": body.Provider})
		return
	}
	conn := h.saveDeviceConnection(deviceCanonical(body.Provider), tokens)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "authorized", "id": conn.id, "connectionId": conn.id,
		"provider": body.Provider, "name": conn.name, "email": conn.email,
	})
}

type deviceTokens struct {
	access, refresh, email, name string
	expiresIn                    int
	extra                        map[string]any
}

func devicePoll(provider, code string, session map[string]any) (deviceTokens, string, string) {
	var t deviceTokens
	var err error
	switch provider {
	case "qoder", "qoder-cn":
		t, err = qoderPoll(provider, session, code)
	case "kilocode":
		t, err = kilocodePoll(code)
	case "grok-cli":
		t, err = grokcliPoll(code)
	case "github":
		t, err = githubPoll(code)
	case "kiro":
		t, err = kiroPoll(session, code)
	case "kimi":
		t, err = kimiPoll(session, code)
	case "codebuddy-cn", "codebuddy-intl":
		t, err = codebuddyPoll(provider, code)
	case "muse":
		t, err = musePoll(code)
	default:
		return t, "error", "unsupported provider"
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "authorization_pending") || strings.Contains(msg, "slow_down") || strings.Contains(msg, "pending") {
			return t, "pending", ""
		}
		return t, "error", msg
	}
	if t.access == "" {
		return t, "pending", ""
	}
	return t, "authorized", ""
}

type savedConn struct{ id, name, email string }

func (h *OAuthHandler) saveDeviceConnection(provider string, t deviceTokens) savedConn {
	if strings.TrimSpace(t.access) == "" {
		return savedConn{}
	}
	email := t.email
	if email == "" {
		email = extractEmailFromJWT(t.access)
	}
	name := connectionDisplayName(provider, t.name, email, map[string]string{
		"qoder": "Qoder", "qoder-cn": "Qoder CN", "kilocode": "KiloCode", "grok-cli": "Grok CLI",
		"github": "GitHub", "kiro": "Kiro", "kimi": "Kimi", "muse": "Muse",
		"codebuddy-cn": "CodeBuddy", "codebuddy-intl": "CodeBuddy",
	}[provider])
	id := provider + "-" + shortHash(t.access)
	dataMap := map[string]any{"apiKey": t.access, "accessToken": t.access}
	if t.refresh != "" {
		dataMap["refreshToken"] = t.refresh
	}
	if email != "" {
		dataMap["email"] = email
	}
	psd := map[string]any{"authMethod": "device"}
	for k, v := range t.extra {
		psd[k] = v
	}
	dataMap["providerSpecificData"] = psd
	if t.expiresIn > 0 {
		dataMap["expiresAt"] = time.Now().Add(time.Duration(t.expiresIn) * time.Second).UTC().Format(time.RFC3339)
	} else {
		// Upstream persists expiresAt=null when the token endpoint omits
		// expiry; keep the same shape so readers can tell "unknown" apart
		// from a stale timestamp.
		dataMap["expiresAt"] = nil
	}
	if h.Repo != nil && h.Repo.RawDB() != nil {
		now := currentTimestamp()
		raw, _ := json.Marshal(dataMap)
		var existing string
		err := h.Repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE id = ?", id).Scan(&existing)
		if err == nil && existing != "" {
			_, _ = h.Repo.RawDB().Exec(
				"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?", name, string(raw), now, id)
		} else {
			_, _ = h.Repo.RawDB().Exec(
				"INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)",
				id, provider, name, string(raw), now, now)
		}
	}
	return savedConn{id, name, email}
}

// --- polls ---

// qoderOpenAPIBase is the openapi host for the provider's own deployment.
func qoderOpenAPIBase(provider string) string {
	if provider == "qoder-cn" {
		return "https://openapi.qoder.com.cn"
	}
	return "https://openapi.qoder.sh"
}

func qoderPoll(provider string, session map[string]any, nonce string) (deviceTokens, error) {
	var t deviceTokens
	verifier, _ := session["verifier"].(string)
	machineID, _ := session["machineId"].(string)
	if nonce == "" || verifier == "" {
		return t, fmt.Errorf("invalid_request: missing nonce/verifier")
	}
	openapi := qoderOpenAPIBase(provider)
	u := openapi + "/api/v1/deviceToken/poll?nonce=" + url.QueryEscape(nonce) +
		"&verifier=" + url.QueryEscape(verifier) + "&challenge_method=S256"
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Go-http-client/2.0")
	resp, err := deviceClient.Do(req)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 202 || resp.StatusCode == 404 {
		return t, fmt.Errorf("authorization_pending")
	}
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return t, fmt.Errorf("poll_failed: HTTP %d", resp.StatusCode)
	}
	var data map[string]any
	if json.Unmarshal(out, &data) != nil {
		return t, fmt.Errorf("poll_failed: invalid JSON")
	}
	token := strVal(data, "token")
	if token == "" {
		return t, fmt.Errorf("poll_failed: no token")
	}
	t.access = token
	t.refresh = strVal(data, "refresh_token")
	// machineId is generated at start and persisted so every later request from
	// this auth presents the same machine (upstream cosy.js generateMachineId).
	t.extra = map[string]any{"machineId": machineID}
	// The COSY signature is bound to the account's user id, so it must be
	// persisted at login. Upstream stores it in providerSpecificData.userId
	// (src/lib/oauth/providers/qoder.js mapTokens); without it every later
	// request is signed with a placeholder and Qoder answers
	// 403 {"code":"105","message":"Login expired"}.
	uid := strVal(data, "user_id")
	if uid != "" {
		t.extra["userId"] = uid
	}
	if ui, _, _ := getJSON(openapi+"/api/v1/userinfo",
		map[string]string{"Authorization": "Bearer " + token, "User-Agent": "Go-http-client/2.0"}); ui != nil {
		t.name = strVal(ui, "name", "username")
		t.email = strVal(ui, "email")
		if oid := strVal(ui, "organization_id"); oid != "" {
			t.extra["organizationId"] = oid
		}
	}
	// Upstream falls back to a stable synthetic email so a re-login updates the
	// existing row instead of piling up "Account N" duplicates.
	if t.email == "" && uid != "" {
		t.email = "qoder-user-" + uid
	}
	return t, nil
}

func kilocodePoll(code string) (deviceTokens, error) {
	var t deviceTokens
	req, _ := http.NewRequest(http.MethodGet, "https://api.kilo.ai/api/device-auth/codes/"+code, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := deviceClient.Do(req)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 202:
		return t, fmt.Errorf("authorization_pending")
	case 403:
		return t, fmt.Errorf("access_denied: authorization denied by user")
	case 410:
		return t, fmt.Errorf("expired_token: authorization code expired")
	}
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return t, fmt.Errorf("poll_failed: %d", resp.StatusCode)
	}
	var data map[string]any
	if json.Unmarshal(out, &data) != nil {
		return t, fmt.Errorf("poll_failed: invalid JSON")
	}
	if strVal(data, "status") != "approved" || strVal(data, "token") == "" {
		return t, fmt.Errorf("authorization_pending")
	}
	t.access = strVal(data, "token")
	t.email = strVal(data, "userEmail")
	if ui, _, _ := getJSON("https://api.kilo.ai/api/profile",
		map[string]string{"Authorization": "Bearer " + t.access}); ui != nil {
		if orgs, ok := ui["organizations"].([]any); ok && len(orgs) > 0 {
			if m, ok := orgs[0].(map[string]any); ok {
				t.extra = map[string]any{"orgId": strVal(m, "id")}
			}
		}
	}
	return t, nil
}

func grokcliPoll(code string) (deviceTokens, error) {
	var t deviceTokens
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {code}, "client_id": {"b1a00492-073a-47ea-816f-4c329264a828"},
	}
	data, _, err := postForm("https://auth.x.ai/oauth2/token", form, map[string]string{"User-Agent": grokcliUA})
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	if e := strVal(data, "error"); e == "authorization_pending" || e == "slow_down" {
		return t, errors.New(e)
	}
	t.access = strVal(data, "access_token")
	t.refresh = strVal(data, "refresh_token")
	if t.access == "" {
		if e := strVal(data, "error"); e != "" {
			return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
		}
		return t, fmt.Errorf("authorization_pending")
	}
	if ui, _, _ := getJSON("https://cli-chat-proxy.grok.com/v1/user",
		map[string]string{"Authorization": "Bearer " + t.access}); ui != nil {
		t.email = strVal(ui, "email")
		t.name = strVal(ui, "name", "username")
	}
	return t, nil
}

func githubPoll(code string) (deviceTokens, error) {
	var t deviceTokens
	form := url.Values{
		"client_id": {"Iv1.b507a08c87ecfe98"}, "device_code": {code},
		"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	data, _, err := postForm("https://github.com/login/oauth/access_token", form, nil)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	if e := strVal(data, "error"); e == "authorization_pending" || e == "slow_down" {
		return t, errors.New(e)
	}
	t.access = strVal(data, "access_token")
	t.refresh = strVal(data, "refresh_token")
	if t.access == "" {
		if e := strVal(data, "error"); e != "" {
			return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
		}
		return t, fmt.Errorf("authorization_pending")
	}
	ghH := map[string]string{
		"Authorization": "Bearer " + t.access, "X-GitHub-Api-Version": "2022-11-28",
		"User-Agent": "GitHubCopilotChat/0.26.7",
	}
	var copilot, user map[string]any
	copilot, _, _ = getJSON("https://api.github.com/copilot_internal/v2/token", ghH)
	user, _, _ = getJSON("https://api.github.com/user", ghH)
	t.email = strVal(user, "email")
	t.name = strVal(user, "name", "login")
	t.extra = map[string]any{
		"copilotToken":          strVal(copilot, "token"),
		"copilotTokenExpiresAt": strVal(copilot, "expires_at"),
		"githubUserId":          user["id"], "githubLogin": strVal(user, "login"),
		"githubName": strVal(user, "name"), "githubEmail": strVal(user, "email"),
	}
	return t, nil
}

func kiroPoll(session map[string]any, code string) (deviceTokens, error) {
	var t deviceTokens
	region, _ := session["region"].(string)
	if region == "" {
		region = "us-east-1"
	}
	if !awsRegionPattern.MatchString(region) {
		return t, fmt.Errorf("invalid region")
	}
	data, _, err := postJSON("https://oidc."+region+".amazonaws.com/token", map[string]any{
		"clientId": strVal(session, "clientId"), "clientSecret": strVal(session, "clientSecret"),
		"deviceCode": code, "grantType": "urn:ietf:params:oauth:grant-type:device_code",
	}, nil)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	t.access = strVal(data, "accessToken", "access_token")
	t.refresh = strVal(data, "refreshToken", "refresh_token")
	if t.access == "" {
		if e := strVal(data, "error"); e != "" {
			return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description", "message"))
		}
		return t, fmt.Errorf("authorization_pending")
	}
	t.email = extractEmailFromJWT(t.access)
	t.extra = map[string]any{
		"clientId": strVal(session, "clientId"), "clientSecret": strVal(session, "clientSecret"),
		"region": region, "authMethod": strVal(session, "authMethod"), "startUrl": strVal(session, "startUrl"),
	}
	if arn := kiroProfileArn(t.access); arn != "" {
		t.extra["profileArn"] = arn
	}
	return t, nil
}

func kiroProfileArn(accessToken string) string {
	body, _ := json.Marshal(map[string]any{"maxResults": 10})
	req, err := http.NewRequest(http.MethodPost, "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := deviceClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	if json.Unmarshal(out, &data) != nil {
		return ""
	}
	for _, p := range data.Profiles {
		if strings.TrimSpace(p.Arn) != "" {
			return strings.TrimSpace(p.Arn)
		}
	}
	return ""
}

func kimiPoll(session map[string]any, code string) (deviceTokens, error) {
	var t deviceTokens
	deviceID, _ := session["deviceId"].(string)
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {"17e5f671-d194-4dfb-9706-5516cb48c098"},
		"device_code": {code},
	}
	data, _, err := postForm("https://auth.kimi.com/api/oauth/token", form, kimiHeaders(deviceID))
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	if e := strVal(data, "error"); e == "authorization_pending" || e == "slow_down" {
		return t, errors.New(e)
	}
	t.access = strVal(data, "access_token")
	t.refresh = strVal(data, "refresh_token")
	if t.access == "" {
		if e := strVal(data, "error"); e != "" {
			return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
		}
		return t, fmt.Errorf("authorization_pending")
	}
	t.extra = map[string]any{"deviceId": deviceID}
	return t, nil
}

func codebuddyPoll(provider, state string) (deviceTokens, error) {
	var t deviceTokens
	base, ua, domain := "https://copilot.tencent.com", "CLI/2.63.2 CodeBuddy/2.63.2", "copilot.tencent.com"
	if provider == "codebuddy-intl" {
		base, ua, domain = "https://www.codebuddy.ai", "IDE/2.63.2 CodeBuddy/2.63.2", "www.codebuddy.ai"
	}
	headers := map[string]string{
		"Accept": "application/json", "User-Agent": ua, "X-Requested-With": "XMLHttpRequest",
		"X-Domain": domain, "X-No-Authorization": "true", "X-No-User-Id": "true",
		"X-No-Enterprise-Id": "true", "X-No-Department-Info": "true", "X-Product": "SaaS",
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/v2/plugin/auth/token?state="+url.QueryEscape(state), nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := deviceClient.Do(req)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return t, fmt.Errorf("request_failed")
	}
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data map[string]any
	if json.Unmarshal(out, &data) != nil {
		return t, fmt.Errorf("request_failed")
	}
	var code float64
	if c, ok := data["code"].(float64); ok {
		code = c
	}
	inner, _ := data["data"].(map[string]any)
	if code == 0 && strVal(inner, "accessToken") != "" {
		t.access = strVal(inner, "accessToken")
		t.refresh = strVal(inner, "refreshToken")
		return t, nil
	}
	if code == 11217 {
		return t, fmt.Errorf("authorization_pending")
	}
	return t, fmt.Errorf("%s", strVal(data, "msg", "error"))
}
