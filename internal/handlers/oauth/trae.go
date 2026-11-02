package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

var (
	traeClientID     = "ono9krqynydwx5"
	traeClientSecret = "-"
	traeGuidanceURLs = []string{
		"https://api.marscode.com/cloudide/api/v3/trae/GetLoginGuidance",
		"https://api.trae.ai/cloudide/api/v3/trae/GetLoginGuidance",
		"https://www.trae.ai/cloudide/api/v3/trae/GetLoginGuidance",
	}
	// SSRF guard: never honor loginHost from callbacks, only this allowlist.
	traeAPIOrigins = []string{
		"https://api.marscode.com",
		"https://api.trae.ai",
		"https://www.trae.ai",
		"https://www.marscode.com",
	}
	traeExchangePath = "/cloudide/api/v3/trae/oauth/ExchangeToken"
	traeUserInfoPath = "/cloudide/api/v3/trae/GetUserInfo"
	traeAuthPath     = "/authorization"
	traeUserAgent    = "Trae/1.0.0 antigravity-cockpit-tools"
)

func traePostJSON(ctx context.Context, url string, body any, headers map[string]string) ([]byte, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", traeUserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return out, resp.StatusCode, err
}

func traeStr(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[p]
	}
	if s, ok := cur.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// HandleTraeAuthorize returns the browser verification URL.
// GET /api/oauth/trae/authorize — mirrors upstream GetLoginGuidance flow.
func (h *OAuthHandler) HandleTraeAuthorize(w http.ResponseWriter, r *http.Request) {
	traceID := r.URL.Query().Get("state")
	if traceID == "" {
		traceID = randomUUID()
	}
	redirectURI := callbackRedirectURI(r)
	loginHost := ""
	var lastErr string
	guidanceBody := map[string]string{"loginTraceID": traceID, "login_trace_id": traceID}
	for _, u := range traeGuidanceURLs {
		out, status, err := traePostJSON(r.Context(), u, guidanceBody, nil)
		if err != nil || status != http.StatusOK {
			lastErr = fmt.Sprintf("%s err=%v status=%d", u, err, status)
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(out, &data); err != nil {
			lastErr = u + " invalid JSON"
			continue
		}
		for _, path := range [][]string{
			{"Result", "LoginHost"}, {"Result", "loginHost"}, {"Result", "LoginURL"},
			{"result", "loginHost"}, {"data", "Result", "LoginHost"}, {"data", "loginHost"},
			{"LoginHost"}, {"loginHost"},
		} {
			if v := traeStr(data, path...); v != "" {
				loginHost = v
				break
			}
		}
		if loginHost != "" {
			break
		}
		lastErr = u + " missing LoginHost"
	}
	if loginHost == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("Trae GetLoginGuidance failed: %s", lastErr))
		return
	}
	host := loginHost
	if !strings.HasPrefix(host, "http") {
		host = "https://" + strings.TrimLeft(host, "/")
	}
	machineID := randomUUID()
	p := url.Values{
		"login_version": {"1"}, "auth_from": {"trae"}, "login_channel": {"native_ide"},
		"plugin_version": {"local"}, "auth_type": {"local"}, "client_id": {traeClientID},
		"redirect": {"0"}, "login_trace_id": {traceID}, "auth_callback_url": {redirectURI},
		"machine_id": {machineID}, "device_id": {"0"}, "x_device_id": {"0"}, "x_machine_id": {machineID},
		"x_device_brand": {"unknown"}, "x_device_type": {"unknown"}, "x_os_version": {"unknown"},
		"x_env": {""}, "x_app_version": {"3.5.54"}, "x_app_type": {"stable"},
	}
	authURL := strings.TrimSuffix(host, "/") + traeAuthPath + "?" + p.Encode()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url": authURL, "authUrl": authURL, "state": traceID, "loginTraceId": traceID,
		"loginHost": loginHost, "redirectUri": redirectURI,
		"flowType": "authorization_code", "provider": "trae",
	})
}

func parseTraeCallback(raw string) (refresh, loginHost, cloudTok string, err error) {
	text := strings.TrimSpace(raw)
	q := text
	if i := strings.Index(text, "?"); i >= 0 {
		q = text[i+1:]
	} else if strings.HasPrefix(text, "#") {
		q = text[1:]
	}
	vals, _ := url.ParseQuery(q)
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(vals.Get(k)); v != "" {
				return v
			}
		}
		return ""
	}
	if e := pick("error", "error_code", "errorCode"); e != "" {
		d := pick("error_description", "error_desc", "message")
		if d != "" {
			return "", "", "", fmt.Errorf("Trae auth failed: %s (%s)", e, d)
		}
		return "", "", "", fmt.Errorf("Trae auth failed: %s", e)
	}
	refresh = pick("refreshToken", "refresh_token", "RefreshToken")
	if refresh == "" {
		return "", "", "", fmt.Errorf("Trae callback missing refreshToken")
	}
	loginHost = pick("loginHost", "login_host", "LoginHost", "host", "consoleHost")
	if loginHost == "" {
		return "", "", "", fmt.Errorf("Trae callback missing loginHost")
	}
	cloudTok = pick("x-cloudide-token", "xCloudideToken", "accessToken", "access_token", "token")
	return refresh, loginHost, cloudTok, nil
}

// HandleTraeExchange exchanges the callback (or imports a pasted JWT) and stores the connection.
// POST /api/oauth/trae/exchange
func (h *OAuthHandler) HandleTraeExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	trimmed := strings.TrimSpace(body.Code)
	if trimmed == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
		return
	}

	var accessToken, refreshToken, cloudTok string
	if strings.Contains(trimmed, "?") || strings.Contains(trimmed, "&") ||
		(strings.Contains(trimmed, "=") && (strings.Contains(trimmed, "refreshToken") || strings.Contains(trimmed, "refresh_token"))) {
		rt, _, ct, err := parseTraeCallback(trimmed)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		refreshToken, cloudTok = rt, ct
		exBody := map[string]string{
			"ClientID": traeClientID, "RefreshToken": refreshToken,
			"ClientSecret": traeClientSecret, "UserID": "",
		}
		headers := map[string]string{}
		if cloudTok != "" {
			headers["x-cloudide-token"] = cloudTok
		}
		var lastErr string
		for _, origin := range traeAPIOrigins {
			out, status, err := traePostJSON(r.Context(), strings.TrimSuffix(origin, "/")+traeExchangePath, exBody, headers)
			if err != nil || status != http.StatusOK {
				lastErr = fmt.Sprintf("%s err=%v status=%d", origin, err, status)
				continue
			}
			var data map[string]any
			if err := json.Unmarshal(out, &data); err != nil {
				lastErr = origin + " invalid JSON"
				continue
			}
			accessToken = firstNonEmpty(
				traeStr(data, "Result", "AccessToken"), traeStr(data, "Result", "accessToken"),
				traeStr(data, "result", "access_token"), traeStr(data, "accessToken"),
			)
			if accessToken == "" {
				lastErr = origin + " " + firstNonEmpty(
					traeStr(data, "message"), traeStr(data, "msg"),
					traeStr(data, "error"), traeStr(data, "Result", "Message"), "missing AccessToken")
				continue
			}
			if rt2 := firstNonEmpty(
				traeStr(data, "Result", "RefreshToken"), traeStr(data, "result", "refresh_token"),
				traeStr(data, "refreshToken")); rt2 != "" {
				refreshToken = rt2
			}
			lastErr = ""
			break
		}
		if accessToken == "" {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("Trae ExchangeToken failed: %s", lastErr))
			return
		}
	} else {
		// Paste-token mode: raw Cloud-IDE-JWT (strip Bearer-style prefix, case-insensitive).
		accessToken = trimTokenPrefix(trimmed)
		if accessToken == "" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
			return
		}
	}

	// Best-effort userinfo for email/region.
	email, name, aiRegion, region, tenant, userID := "", "", "US-East", "", "marscode", ""
	if accessToken != "" {
		for _, origin := range traeAPIOrigins {
			out, status, err := traePostJSON(r.Context(), strings.TrimSuffix(origin, "/")+traeUserInfoPath,
				map[string]string{}, map[string]string{"x-cloudide-token": accessToken})
			if err != nil || status != http.StatusOK {
				continue
			}
			var data map[string]any
			if json.Unmarshal(out, &data) != nil {
				continue
			}
			email = firstNonEmpty(traeStr(data, "Result", "NonPlainTextEmail"), traeStr(data, "Result", "Email"), traeStr(data, "Result", "email"), traeStr(data, "email"), traeStr(data, "data", "email"))
			name = firstNonEmpty(traeStr(data, "Result", "ScreenName"), traeStr(data, "Result", "Nickname"), traeStr(data, "Result", "Name"), traeStr(data, "result", "nickname"), traeStr(data, "nickname"), traeStr(data, "name"))
			aiRegion = firstNonEmpty(traeStr(data, "Result", "AIRegion"), traeStr(data, "Result", "aiRegion"), traeStr(data, "aiRegion"), aiRegion)
			region = firstNonEmpty(traeStr(data, "Result", "Region"), traeStr(data, "Result", "region"), traeStr(data, "region"))
			tenant = firstNonEmpty(traeStr(data, "Result", "TenantID"), traeStr(data, "Result", "tenantId"), traeStr(data, "tenantId"), tenant)
			userID = firstNonEmpty(traeStr(data, "Result", "UserID"), traeStr(data, "Result", "userId"), traeStr(data, "userId"))
			break
		}
	}
	scope := "marscode-us"
	if rl := strings.ToLower(aiRegion); rl == "sg" || strings.Contains(rl, "singapore") {
		scope = "marscode-sg"
	} else if rl == "cn" || strings.Contains(rl, "china") {
		scope = "marscode-cn"
	}

	connName := connectionDisplayName("trae", body.Name, email, name)
	if connName == name && name == "" {
		connName = "Trae"
	}
	connID := "trae-" + shortHash(accessToken)
	now := currentTimestamp()
	dataMap := map[string]any{"apiKey": accessToken, "accessToken": accessToken}
	if refreshToken != "" {
		dataMap["refreshToken"] = refreshToken
	}
	if email != "" {
		dataMap["email"] = email
	}
	dataMap["providerSpecificData"] = map[string]any{
		"authMethod": "oauth", "aiRegion": aiRegion,
		"region": firstNonEmpty(region, aiRegion), "tenant": tenant, "userId": userID,
		"scope": scope, "webId": "", "bizUserId": "", "userUniqueId": "",
		"appLanguage": "en", "appVersion": "3.5.54",
		"userRegion":   map[bool]string{true: "SG", false: "US"}[scope == "marscode-sg"],
		"userIdentity": "Free",
	}
	dataBytes, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal connection data")
		return
	}
	if h.Repo != nil && h.Repo.RawDB() != nil {
		var existing string
		err := h.Repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&existing)
		if err == nil && existing != "" {
			_, err = h.Repo.RawDB().Exec(
				"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?",
				connName, string(dataBytes), now, connID,
			)
		} else {
			_, err = h.Repo.RawDB().Exec(
				"INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)",
				connID, "trae", connName, string(dataBytes), now, now,
			)
		}
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
			return
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "authorized", "id": connID, "connectionId": connID,
		"provider": "trae", "name": connName, "email": email,
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// trimTokenPrefix strips a leading "Cloud-IDE-JWT " or "Bearer " (case-insensitive).
func trimTokenPrefix(s string) string {
	lowered := strings.ToLower(s)
	for _, pfx := range []string{"cloud-ide-jwt ", "bearer "} {
		if strings.HasPrefix(lowered, pfx) {
			return strings.TrimSpace(s[len(pfx):])
		}
	}
	return s
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return randomString(32)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
