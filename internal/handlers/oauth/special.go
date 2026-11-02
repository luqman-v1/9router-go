package oauth

import (
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"

	_ "modernc.org/sqlite"
)

// ---------- cursor: import + auto-import from local state.vscdb ----------

var cursorUUIDPattern = regexp.MustCompile(`^[a-f0-9-]{32,}$`)

// HandleCursorImport validates pasted Cursor token + machine ID and stores it.
// POST /api/oauth/cursor/import
func (h *OAuthHandler) HandleCursorImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessToken string `json:"accessToken"`
		MachineID   string `json:"machineId"`
		Name        string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	token, machineID := strings.TrimSpace(body.AccessToken), strings.TrimSpace(body.MachineID)
	if token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "access token is required")
		return
	}
	if machineID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "machine ID is required")
		return
	}
	if len(token) < 50 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid token format: token appears too short")
		return
	}
	if !cursorUUIDPattern.MatchString(strings.ReplaceAll(strings.ToLower(machineID), "-", "")) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid machine ID format: expected UUID format")
		return
	}
	email := extractEmailFromJWT(token)
	name := body.Name
	if name == "" {
		name = "Cursor"
		if email != "" {
			name += " (" + email + ")"
		}
	}
	connID := "cursor-" + shortHash(token)
	dataMap := map[string]any{"apiKey": token, "accessToken": token}
	if email != "" {
		dataMap["email"] = email
	}
	dataMap["providerSpecificData"] = map[string]any{"machineId": machineID, "authMethod": "imported"}
	h.saveSpecialConnection(w, "cursor", connID, name, email, dataMap, nil)
}

// HandleCursorAutoImport reads the local Cursor IDE state.vscdb (server host only).
// GET /api/oauth/cursor/auto-import
func (h *OAuthHandler) HandleCursorAutoImport(w http.ResponseWriter, r *http.Request) {
	token, machineID, err := readCursorStateDB()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("auto-import failed: %v", err))
		return
	}
	email := extractEmailFromJWT(token)
	name := "Cursor"
	if email != "" {
		name += " (" + email + ")"
	}
	connID := "cursor-" + shortHash(token)
	dataMap := map[string]any{"apiKey": token, "accessToken": token}
	if email != "" {
		dataMap["email"] = email
	}
	dataMap["providerSpecificData"] = map[string]any{"machineId": machineID, "authMethod": "imported"}
	h.saveSpecialConnection(w, "cursor", connID, name, email, dataMap, map[string]any{"machineId": machineID})
}

func cursorStatePaths() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		return []string{
			filepath.Join(base, "Cursor", "User", "globalStorage", "state.vscdb"),
			filepath.Join(base, "Cursor - Insiders", "User", "globalStorage", "state.vscdb"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{
			filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb"),
			filepath.Join(appData, "Cursor - Insiders", "User", "globalStorage", "state.vscdb"),
		}
	default:
		return []string{filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb")}
	}
}

func readCursorStateDB() (token, machineID string, err error) {
	var lastErr string
	for _, p := range cursorStatePaths() {
		if _, serr := os.Stat(p); serr != nil {
			lastErr = "not found: " + p
			continue
		}
		db, oerr := sql.Open("sqlite", "file:"+p+"?mode=ro&immutable=1")
		if oerr != nil {
			lastErr = oerr.Error()
			continue
		}
		got := map[string]string{}
		for _, k := range []string{"cursorAuth/accessToken", "cursorAuth/token", "storage.serviceMachineId", "storage.machineId", "telemetry.machineId"} {
			var v string
			if qerr := db.QueryRow("SELECT value FROM itemTable WHERE key = ?", k).Scan(&v); qerr == nil && v != "" {
				got[k] = v
			}
		}
		db.Close()
		token = firstNonEmpty(got["cursorAuth/accessToken"], got["cursorAuth/token"])
		machineID = firstNonEmpty(got["storage.serviceMachineId"], got["storage.machineId"], got["telemetry.machineId"])
		if token != "" && machineID != "" {
			return token, machineID, nil
		}
		lastErr = "token or machineId missing in " + p
	}
	return "", "", fmt.Errorf("%s (is Cursor IDE installed and logged in on this host?)", lastErr)
}

// ---------- kimchi: browser-token flow ----------

var (
	kimchiAppBase  = "https://app.kimchi.dev"
	kimchiValidate = "https://api.cast.ai/v1/llm/openai/supported-providers"
	kimchiUserInfo = "https://app.kimchi.dev/api/v1/me"
)

// HandleKimchiAuthorize returns the kimchi.dev cli-auth URL.
// GET /api/oauth/kimchi/authorize
func (h *OAuthHandler) HandleKimchiAuthorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := callbackRedirectURI(r)
	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}
	p := url.Values{"callback": {redirectURI}, "state": {state}}
	authURL := kimchiAppBase + "/cli-auth?" + p.Encode()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url": authURL, "authUrl": authURL, "state": state,
		"redirectUri": redirectURI, "flowType": "browser_token", "provider": "kimchi",
	})
}

// HandleKimchiExchange validates the pasted browser token and stores it.
// POST /api/oauth/kimchi/exchange
func (h *OAuthHandler) HandleKimchiExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	ctx := r.Context()
	token := strings.TrimSpace(body.Code)
	if token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing Kimchi token")
		return
	}
	vreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, kimchiValidate, nil)
	vreq.Header.Set("Accept", "application/json")
	vreq.Header.Set("Authorization", "Bearer "+token)
	if vresp, err := (&http.Client{Timeout: 15 * time.Second}).Do(vreq); err != nil || vresp.StatusCode != http.StatusOK {
		status := 0
		if vresp != nil {
			status = vresp.StatusCode
			vresp.Body.Close()
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("Kimchi token validation failed: %v status=%d", err, status))
		return
	} else {
		vresp.Body.Close()
	}
	var userID, username, email, display string
	if ureq, _ := http.NewRequestWithContext(ctx, http.MethodGet, kimchiUserInfo, nil); ureq != nil {
		ureq.Header.Set("Accept", "application/json")
		ureq.Header.Set("Authorization", "Bearer "+token)
		if uresp, err := (&http.Client{Timeout: 10 * time.Second}).Do(ureq); err == nil {
			defer uresp.Body.Close()
			if raw, rerr := io.ReadAll(io.LimitReader(uresp.Body, 1<<20)); rerr == nil && uresp.StatusCode == http.StatusOK {
				var u map[string]any
				if json.Unmarshal(raw, &u) == nil {
					if id, ok := u["id"]; ok {
						userID = fmt.Sprint(id)
					}
					username, _ = u["username"].(string)
					email, _ = u["email"].(string)
					display, _ = u["name"].(string)
				}
			}
		}
	}
	if email == "" && userID != "" {
		email = "kimchi-user-" + userID
	}
	name := body.Name
	if name == "" {
		name = "Kimchi"
		if display != "" {
			name += " (" + display + ")"
		} else if username != "" {
			name += " (" + username + ")"
		} else if email != "" {
			name += " (" + email + ")"
		}
	}
	connID := "kimchi-" + shortHash(token)
	dataMap := map[string]any{"apiKey": token, "accessToken": token}
	if email != "" {
		dataMap["email"] = email
	}
	dataMap["providerSpecificData"] = map[string]any{"authMethod": "browser_token", "userId": userID, "username": username}
	h.saveSpecialConnection(w, "kimchi", connID, name, email, dataMap, nil)
}

// ---------- gitlab PAT ----------

// HandleGitlabPAT verifies a Personal Access Token and stores it.
// POST /api/oauth/gitlab/pat
func (h *OAuthHandler) HandleGitlabPAT(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token   string `json:"token"`
		BaseURL string `json:"baseUrl"`
		Name    string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	ctx := r.Context()
	token := strings.TrimSpace(body.Token)
	if token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Personal Access Token is required")
		return
	}
	base := strings.TrimSuffix(strings.TrimSpace(body.BaseURL), "/")
	if base == "" {
		base = "https://gitlab.com"
	}
	ureq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v4/user", nil)
	ureq.Header.Set("Accept", "application/json")
	ureq.Header.Set("Private-Token", token)
	uresp, err := (&http.Client{Timeout: 15 * time.Second}).Do(ureq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("GitLab request failed: %v", err))
		return
	}
	defer uresp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(uresp.Body, 1<<20))
	if uresp.StatusCode != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, fmt.Sprintf("GitLab token verification failed: %s", firstLine(string(raw), 200)))
		return
	}
	var user map[string]any
	if json.Unmarshal(raw, &user) != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid GitLab user response")
		return
	}
	email := firstNonEmpty(strVal(user, "email"), strVal(user, "public_email"))
	display := firstNonEmpty(strVal(user, "name"), strVal(user, "username"), email)
	name := body.Name
	if name == "" {
		name = "GitLab"
		if display != "" {
			name += " (" + display + ")"
		}
	}
	connID := "gitlab-" + shortHash(token)
	dataMap := map[string]any{"apiKey": token, "accessToken": token}
	if email != "" {
		dataMap["email"] = email
	}
	dataMap["providerSpecificData"] = map[string]any{
		"username": strVal(user, "username"), "email": email,
		"name": strVal(user, "name"), "baseUrl": base, "authKind": "personal_access_token",
	}
	h.saveSpecialConnection(w, "gitlab", connID, name, email, dataMap, nil)
}

// ---------- iflow cookie ----------

var iflowCookieHeaders = map[string]string{
	"Accept": "application/json, text/plain, */*", "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
	"Connection": "keep-alive",
}

// HandleIflowCookie exchanges a platform.iflow.cn BXAuth cookie for an API key.
// POST /api/oauth/iflow/cookie
func (h *OAuthHandler) HandleIflowCookie(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cookie string `json:"cookie"`
		Name   string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	ctx := r.Context()
	cookie := strings.TrimSpace(body.Cookie)
	if cookie == "" || !strings.Contains(cookie, "BXAuth=") {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "cookie must contain BXAuth field")
		return
	}
	if !strings.HasSuffix(cookie, ";") {
		cookie += ";"
	}
	client := &http.Client{Timeout: 15 * time.Second}
	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://platform.iflow.cn/api/openapi/apikey", nil)
	for k, v := range iflowCookieHeaders {
		getReq.Header.Set(k, v)
	}
	getReq.Header.Set("Cookie", cookie)
	getResp, err := client.Do(getReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("request failed: %v", err))
		return
	}
	defer getResp.Body.Close()
	getRaw, _ := io.ReadAll(io.LimitReader(getResp.Body, 1<<20))
	if getResp.StatusCode != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("failed to fetch API key info: %s", firstLine(string(getRaw), 200)))
		return
	}
	var getResult struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if json.Unmarshal(getRaw, &getResult) != nil || !getResult.Success {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("API key fetch failed: %s", getResult.Message))
		return
	}
	if getResult.Data.Name == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing name in API key info")
		return
	}
	postBody, _ := json.Marshal(map[string]string{"name": getResult.Data.Name})
	postReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://platform.iflow.cn/api/openapi/apikey", strings.NewReader(string(postBody)))
	for k, v := range iflowCookieHeaders {
		postReq.Header.Set(k, v)
	}
	postReq.Header.Set("Cookie", cookie)
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("Origin", "https://platform.iflow.cn")
	postReq.Header.Set("Referer", "https://platform.iflow.cn/")
	postResp, err := client.Do(postReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("request failed: %v", err))
		return
	}
	defer postResp.Body.Close()
	postRaw, _ := io.ReadAll(io.LimitReader(postResp.Body, 1<<20))
	if postResp.StatusCode != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("failed to refresh API key: %s", firstLine(string(postRaw), 200)))
		return
	}
	var postResult struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			APIKey     string `json:"apiKey"`
			Name       string `json:"name"`
			ExpireTime string `json:"expireTime"`
		} `json:"data"`
	}
	if json.Unmarshal(postRaw, &postResult) != nil || !postResult.Success {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("API key refresh failed: %s", postResult.Message))
		return
	}
	if postResult.Data.APIKey == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing API key in response")
		return
	}
	bxAuth := ""
	if m := regexp.MustCompile(`BXAuth=([^;]+)`).FindStringSubmatch(cookie); len(m) == 2 {
		bxAuth = "BXAuth=" + m[1] + ";"
	}
	name := body.Name
	if name == "" {
		name = firstNonEmpty(postResult.Data.Name, getResult.Data.Name)
	}
	email := name
	connID := "iflow-" + shortHash(postResult.Data.APIKey)
	dataMap := map[string]any{"apiKey": postResult.Data.APIKey, "accessToken": postResult.Data.APIKey}
	dataMap["providerSpecificData"] = map[string]any{"cookie": bxAuth, "expireTime": postResult.Data.ExpireTime, "authMethod": "cookie"}
	h.saveSpecialConnection(w, "iflow", connID, name, email, dataMap, nil)
}

// saveSpecialConnection upserts an oauth-family connection and writes the standard response.
func (h *OAuthHandler) saveSpecialConnection(w http.ResponseWriter, provider, connID, name, email string, dataMap map[string]any, _ map[string]any) {
	now := currentTimestamp()
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
				"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?", name, string(dataBytes), now, connID)
		} else {
			_, err = h.Repo.RawDB().Exec(
				"INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)",
				connID, provider, name, string(dataBytes), now, now)
		}
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
			return
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "status": "authorized", "id": connID, "connectionId": connID,
		"provider": provider, "name": name, "email": email,
		"connection": map[string]any{"id": connID, "provider": provider, "email": email},
	})
}
