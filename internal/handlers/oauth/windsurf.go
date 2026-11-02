package oauth

import (
	"bytes"
	"context"
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
	windsurfClientID         = "3GUryQ7ldAeKEuD2obYnppsnmj58eP5u"
	windsurfSignInBase       = "https://www.windsurf.com"
	windsurfSignInPath       = "/windsurf/signin"
	windsurfRegisterBase     = "https://register.windsurf.com"
	windsurfRegisterPath     = "/exa.seat_management_pb.SeatManagementService/RegisterUser"
	windsurfOneTimePath      = "/exa.seat_management_pb.SeatManagementService/GetOneTimeAuthToken"
	windsurfCurrentUserPath  = "/exa.seat_management_pb.SeatManagementService/GetCurrentUser"
	windsurfDefaultAPIServer = "https://server.codeium.com"
	windsurfUserAgent        = "antigravity-cockpit-tools"
)

func windsurfSeatPost(ctx context.Context, baseURL, path string, body any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", windsurfUserAgent)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(out)[:min(200, len(out))])
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("invalid JSON")
	}
	return data, nil
}

// HandleWindsurfAuthorize returns the windsurf.com signin URL.
// GET /api/oauth/windsurf/authorize
func (h *OAuthHandler) HandleWindsurfAuthorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := callbackRedirectURI(r)
	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}
	p := url.Values{
		"response_type": {"token"}, "client_id": {windsurfClientID},
		"redirect_uri": {redirectURI}, "state": {state},
		"prompt": {"login"}, "redirect_parameters_type": {"query"}, "workflow": {"onboarding"},
	}
	authURL := windsurfSignInBase + windsurfSignInPath + "?" + p.Encode()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url": authURL, "authUrl": authURL, "state": state,
		"redirectUri": redirectURI, "flowType": "authorization_code", "provider": "windsurf",
	})
}

func parseWindsurfCallback(raw, expectedState string) (string, error) {
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
	if e := pick("error"); e != "" {
		if d := pick("error_description"); d != "" {
			return "", fmt.Errorf("Windsurf auth failed: %s (%s)", e, d)
		}
		return "", fmt.Errorf("Windsurf auth failed: %s", e)
	}
	token := pick("access_token", "token")
	if token == "" {
		return "", fmt.Errorf("Windsurf callback missing access_token")
	}
	if expectedState != "" {
		if st := pick("state"); st != "" && st != expectedState {
			return "", fmt.Errorf("Windsurf callback state mismatch")
		}
	}
	return token, nil
}

// HandleWindsurfExchange registers the firebase JWT and stores the connection.
// POST /api/oauth/windsurf/exchange
func (h *OAuthHandler) HandleWindsurfExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
		Name  string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	trimmed := strings.TrimSpace(body.Code)
	if trimmed == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
		return
	}

	var firebaseJWT, apiKey, apiServerURL, authMethod string
	if strings.Contains(trimmed, "?") || strings.Contains(trimmed, "access_token=") {
		firebaseJWT, _ = parseWindsurfCallback(trimmed, body.State)
		if firebaseJWT == "" {
			if _, err := parseWindsurfCallback(trimmed, body.State); err != nil {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		authMethod = "oauth"
	} else {
		clean := trimTokenPrefix(trimmed)
		if strings.HasPrefix(clean, "sk-ws-") {
			apiKey, apiServerURL, authMethod = clean, windsurfDefaultAPIServer, "imported"
		} else {
			firebaseJWT, authMethod = clean, "imported"
		}
	}
	if apiKey == "" {
		if firebaseJWT == "" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing access_token")
			return
		}
		reg, err := windsurfSeatPost(r.Context(), windsurfRegisterBase, windsurfRegisterPath, map[string]string{"firebase_id_token": firebaseJWT})
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("Windsurf RegisterUser failed: %v", err))
			return
		}
		apiKey = windsurfStr(reg, "apiKey", "api_key")
		if apiKey == "" {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "Windsurf RegisterUser missing apiKey")
			return
		}
		apiServerURL = windsurfStr(reg, "apiServerUrl", "api_server_url")
		if apiServerURL == "" {
			apiServerURL = windsurfDefaultAPIServer
		}
	}

	email, name := "", ""
	if firebaseJWT != "" {
		if auth, err := windsurfSeatPost(r.Context(), apiServerURL, windsurfOneTimePath, map[string]string{"firebaseIdToken": firebaseJWT}); err == nil {
			if tok := windsurfStr(auth, "authToken", "auth_token"); tok != "" {
				if user, err := windsurfSeatPost(r.Context(), apiServerURL, windsurfCurrentUserPath, map[string]any{"authToken": tok, "includeSubscription": true}); err == nil {
					if u, ok := user["user"].(map[string]any); ok {
						user = u
					}
					email, _ = user["email"].(string)
					name, _ = user["name"].(string)
				}
			}
		}
	}

	connName := connectionDisplayName("windsurf", body.Name, email, name)
	if connName == name && name == "" {
		connName = "Windsurf"
	}
	connID := "windsurf-" + shortHash(apiKey)
	now := currentTimestamp()
	dataMap := map[string]any{"apiKey": apiKey, "accessToken": apiKey}
	if email != "" {
		dataMap["email"] = email
	}
	psd := map[string]any{"authMethod": authMethod, "apiServerUrl": apiServerURL}
	if firebaseJWT != "" {
		psd["firebaseIdToken"] = firebaseJWT
	}
	dataMap["providerSpecificData"] = psd
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
				connID, "windsurf", connName, string(dataBytes), now, now,
			)
		}
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
			return
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "authorized", "id": connID, "connectionId": connID,
		"provider": "windsurf", "name": connName, "email": email,
	})
}

func windsurfStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
