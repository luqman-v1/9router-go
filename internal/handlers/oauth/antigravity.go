package oauth

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

var (
	googleOAuthAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleOAuthTokenURL = "https://oauth2.googleapis.com/token"
)

const defaultAntigravityProjectID = "aicode-consumers"

var defaultAntigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

func getAntigravityOAuthConfig() (clientID, clientSecret, tokenURL string) {
	clientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	clientSecret = "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"
	tokenURL = googleOAuthTokenURL

	if cfg, ok := providers.KnownOAuthConfigs["antigravity"]; ok {
		if cfg.ClientID != "" {
			clientID = cfg.ClientID
		}
		if cfg.ClientSecret != "" {
			clientSecret = cfg.ClientSecret
		}
		if cfg.TokenURL != "" && googleOAuthTokenURL == "https://oauth2.googleapis.com/token" {
			tokenURL = cfg.TokenURL
		}
	}
	if v := os.Getenv("ANTIGRAVITY_OAUTH_CLIENT_ID"); v != "" {
		clientID = v
	}
	if v := os.Getenv("ANTIGRAVITY_OAUTH_CLIENT_SECRET"); v != "" {
		clientSecret = v
	}
	return
}

func getAntigravityRedirectURI(r *http.Request) string {
	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirect_uri"))
	if validRedirectURI(redirectURI) {
		return redirectURI
	}
	return "http://localhost:8080/callback"
}

// HandleAntigravityAuthorize returns the Google OAuth authorization URL for Antigravity.
// GET /api/oauth/antigravity/authorize
func (h *OAuthHandler) HandleAntigravityAuthorize(w http.ResponseWriter, r *http.Request) {
	writeGoogleAuthorize(w, r)
}

// writeGoogleAuthorize builds the Google consumer-OAuth consent URL shared by
// the antigravity and agy-acp logins (same OAuth client, same scopes) and
// writes it as JSON, or redirects when redirect=true.
func writeGoogleAuthorize(w http.ResponseWriter, r *http.Request) {
	clientID, _, _ := getAntigravityOAuthConfig()
	redirectURI := getAntigravityRedirectURI(r)

	scopes := defaultAntigravityScopes
	if customScope := r.URL.Query().Get("scope"); customScope != "" {
		scopes = strings.Split(customScope, " ")
	}

	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}

	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(scopes, " ")},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
		"state":         {state},
	}

	authURL := googleOAuthAuthURL + "?" + params.Encode()

	if r.URL.Query().Get("redirect") == "true" {
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url":         authURL,
		"redirectUrl": authURL,
		"authUrl":     authURL,
		"state":       state,
		"redirectUri": redirectURI,
	})
}

// HandleAntigravityExchange exchanges a Google authorization code for tokens
// and stores the Antigravity connection in the database.
// POST /api/oauth/antigravity/exchange
func (h *OAuthHandler) HandleAntigravityExchange(w http.ResponseWriter, r *http.Request) {
	h.exchangeGoogleCode(w, r, googleLoginTarget{
		provider:   "antigravity",
		idPrefix:   "ag-",
		namePrefix: "Antigravity",
		projectID:  defaultAntigravityProjectID,
	})
}

// googleLoginTarget parameterizes the shared Google consumer-OAuth login for
// the providers that sign in with the antigravity OAuth client. Each target
// stores its own connection rows; no provider ever writes another's row.
type googleLoginTarget struct {
	provider   string
	idPrefix   string
	namePrefix string
	projectID  string
}

// googleTokenData is the Google token-endpoint response shared by every
// consumer-OAuth login.
type googleTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
}

func (h *OAuthHandler) exchangeGoogleCode(w http.ResponseWriter, r *http.Request, target googleLoginTarget) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirectUri"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	code := cleanAuthCode(body.Code)
	redirectURI := strings.TrimSpace(body.RedirectURI)
	if code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
		return
	}
	if !validRedirectURI(redirectURI) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing or invalid redirectUri")
		return
	}

	tokenData, ok := requestGoogleTokens(w, r, code, redirectURI, target.provider)
	if !ok {
		return
	}
	h.storeGoogleConnection(w, tokenData, target)
}

// requestGoogleTokens performs the authorization-code exchange against the
// Google token endpoint. It writes the error response itself and returns
// ok=false on any failure.
func requestGoogleTokens(w http.ResponseWriter, r *http.Request, code, redirectURI, provider string) (*googleTokenData, bool) {
	clientID, clientSecret, tokenURL := getAntigravityOAuthConfig()

	form := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}

	tokenReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to create token request")
		return nil, false
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No custom UA: the handler runs server-side against Google token
	// endpoints, and any router brand here is pure self-identification.
	// Go's default UA ("Go-http-client/2.0") is unbranded and sufficient.

	client := &http.Client{Timeout: 15 * time.Second}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		log.Error("oauth", provider+" token exchange failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, provider+" token exchange failed")
		return nil, false
	}
	defer tokenResp.Body.Close()

	respBody, err := io.ReadAll(tokenResp.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read token response")
		return nil, false
	}
	if tokenResp.StatusCode != http.StatusOK {
		// Never log the raw body: token responses carry access/refresh tokens.
		log.Warn("oauth", provider+" token exchange non-200", "status", tokenResp.StatusCode, "bytes", len(respBody))
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange returned status %d", tokenResp.StatusCode))
		return nil, false
	}

	var tokenData googleTokenData
	if err := json.Unmarshal(respBody, &tokenData); err != nil {
		log.Error("oauth", provider+" decode token response failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to decode token response")
		return nil, false
	}
	if tokenData.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "missing access_token in token response")
		return nil, false
	}
	return &tokenData, true
}

// storeGoogleConnection upserts the connection row for a completed Google
// login and writes the success response.
func (h *OAuthHandler) storeGoogleConnection(w http.ResponseWriter, tokenData *googleTokenData, target googleLoginTarget) {
	email := ""
	if tokenData.IDToken != "" {
		email = extractEmailFromJWT(tokenData.IDToken)
	}

	connID := ""
	if email != "" {
		connID = target.idPrefix + shortHash(email)
	} else if tokenData.RefreshToken != "" {
		connID = target.idPrefix + shortHash(tokenData.RefreshToken)
	} else {
		connID = target.idPrefix + randomString(12)
	}

	connName := connectionDisplayName(target.provider, "", email, target.namePrefix)

	now := currentTimestamp()
	dataMap := map[string]any{
		"apiKey":      tokenData.AccessToken,
		"accessToken": tokenData.AccessToken,
		"tokenType":   tokenData.TokenType,
		"projectId":   target.projectID,
	}
	if tokenData.RefreshToken != "" {
		dataMap["refreshToken"] = tokenData.RefreshToken
	}
	if email != "" {
		dataMap["email"] = email
	}
	if tokenData.IDToken != "" {
		dataMap["idToken"] = tokenData.IDToken
	}
	if tokenData.Scope != "" {
		dataMap["scope"] = tokenData.Scope
	}
	if tokenData.ExpiresIn > 0 {
		dataMap["expiresAt"] = time.Now().Add(time.Duration(tokenData.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	dataBytes, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal connection data")
		return
	}

	if h.Repo != nil && h.Repo.RawDB() != nil {
		var existingDataStr string
		err := h.Repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&existingDataStr)
		if err == nil && existingDataStr != "" {
			var existingData map[string]any
			if err := json.Unmarshal([]byte(existingDataStr), &existingData); err == nil {
				if tokenData.RefreshToken == "" {
					if rt, ok := existingData["refreshToken"].(string); ok && rt != "" {
						dataMap["refreshToken"] = rt
					}
				}
				if pid, ok := existingData["projectId"].(string); ok && strings.TrimSpace(pid) != "" {
					dataMap["projectId"] = strings.TrimSpace(pid)
				} else if psd, ok := existingData["providerSpecificData"].(map[string]any); ok {
					if pid, ok := psd["projectId"].(string); ok && strings.TrimSpace(pid) != "" {
						dataMap["projectId"] = strings.TrimSpace(pid)
					}
				}
			}
			dataBytes, _ = json.Marshal(dataMap)
			_, err = h.Repo.RawDB().Exec(
				"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?",
				connName, string(dataBytes), now, connID,
			)
		} else {
			priority := h.Repo.ResolveDefaultPriority(target.provider)
			_, err = h.Repo.RawDB().Exec(
				"INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, ?, 1, ?, ?, ?)",
				connID, target.provider, connName, priority, string(dataBytes), now, now,
			)
		}
		if err != nil {
			log.Error("oauth", "save "+target.provider+" connection failed", "conn", connID, "error", err)
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
			return
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":          connID,
			"provider":    target.provider,
			"email":       email,
			"displayName": connName,
		},
	})
}

func cleanAuthCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	// If the user pasted a full URL or query string like "https://...?code=..." or "code=..."
	if strings.Contains(code, "code=") {
		if u, err := url.Parse(code); err == nil && u.Query().Get("code") != "" {
			code = u.Query().Get("code")
		} else {
			parts := strings.Split(code, "code=")
			if len(parts) > 1 {
				sub := strings.Split(parts[1], "&")
				code = sub[0]
			}
		}
	}
	// Decode percent encoding until fully unescaped so that "4%2F..." or "4%252F..." becomes "4/..."
	for strings.Contains(code, "%") {
		unescaped, err := url.QueryUnescape(code)
		if err != nil || unescaped == code {
			break
		}
		code = unescaped
	}
	return strings.TrimSpace(code)
}
