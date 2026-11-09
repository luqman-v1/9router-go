package oauth

import (
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/updater"
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// truncateTokenBody caps an upstream body used in an error message, so a
// credential the token endpoint echoed back cannot reach the logs or the
// client.
func truncateTokenBody(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}

var (
	clineOAuthAuthorizeURL = "https://api.cline.bot/api/v1/auth/authorize"
	clineOAuthTokenURL     = "https://api.cline.bot/api/v1/auth/token"
)

// clineProvider normalizes "cline" and "clinepass" to a supported provider ID.
func clineProvider(p string) (string, bool) {
	switch p {
	case "clinepass":
		return "clinepass", true
	case "", "cline":
		return "cline", true
	default:
		return "", false
	}
}

// HandleClineAuthorize returns the Cline (api.cline.bot) authorization URL with PKCE challenge.
// GET /api/oauth/cline/authorize?provider=cline|clinepass
// Mirrors upstream 9router: client_type=extension; the callback follows the
// dashboard host (override via ?redirect_uri=).
func (h *OAuthHandler) HandleClineAuthorize(w http.ResponseWriter, r *http.Request) {
	provider, ok := clineProvider(r.URL.Query().Get("provider"))
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported provider (want cline|clinepass)")
		return
	}

	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}
	verifier := randomString(64)
	challenge := sha256Base64(verifier)
	redirectURI := callbackRedirectURI(r)

	params := url.Values{
		"client_type":  {"extension"},
		"callback_url": {redirectURI},
		"redirect_uri": {redirectURI},
	}
	authURL := clineOAuthAuthorizeURL + "?" + params.Encode()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url":           authURL,
		"authUrl":       authURL,
		"state":         state,
		"codeVerifier":  verifier,
		"codeChallenge": challenge,
		"redirectUri":   redirectURI,
		"flowType":      "authorization_code",
		"callbackPath":  "/callback",
		"provider":      provider,
	})
}

// HandleClineExchange exchanges the Cline authorization code for tokens and stores the connection.
// POST /api/oauth/cline/exchange
func (h *OAuthHandler) HandleClineExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider     string `json:"provider"`
		Code         string `json:"code"`
		CodeVerifier string `json:"codeVerifier"`
		RedirectURI  string `json:"redirectUri"`
		RedirectUri  string `json:"redirect_uri"`
		State        string `json:"state"`
		Name         string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}

	provider, ok := clineProvider(body.Provider)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported provider (want cline|clinepass)")
		return
	}
	if body.Code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
		return
	}
	redirectURI := exchangeRedirectURI(r, body.RedirectURI, body.RedirectUri)

	tokenURL := clineOAuthTokenURL
	accessToken, refreshToken := "", ""
	if acc, ref, ok := decodeClineCode(body.Code); ok {
		accessToken, refreshToken = acc, ref
	} else {
		if body.CodeVerifier == "" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing codeVerifier parameter")
			return
		}
		acc, ref, err := exchangeClineCode(r.Context(), tokenURL, body.Code, body.CodeVerifier, redirectURI)
		if err != nil {
			log.Warn("oauth", "cline token exchange non-200", "provider", provider, "error", err)
			handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("Cline token exchange failed: %v", err))
			return
		}
		accessToken, refreshToken = acc, ref
	}
	if accessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "missing accessToken in token response")
		return
	}

	// Name new connections by account email (the user asked for email, not a
	// generic "ClinePass" label that collides across accounts). The
	// base64url bundle carries email/firstName/lastName/expiresAt; the
	// /auth/token path returns only tokens, so name-by-email applies to the
	// bundle path and stays a stable default otherwise.
	email, firstName, lastName := decodeClineIdentity(body.Code)
	connName := connectionDisplayName(provider, body.Name, email,
		strings.TrimSpace(strings.TrimSpace(firstName)+" "+strings.TrimSpace(lastName)))

	connID := provider + "-" + shortHash(accessToken)

	now := currentTimestamp()
	dataMap := map[string]any{
		"apiKey":      accessToken,
		"accessToken": accessToken,
	}
	if refreshToken != "" {
		dataMap["refreshToken"] = refreshToken
	}
	if email != "" {
		dataMap["email"] = email
	}
	dataMap["providerSpecificData"] = map[string]any{
		"authMethod": "authorization_code",
		"firstName":  firstName,
		"lastName":   lastName,
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
			if refreshToken == "" {
				var old map[string]any
				if jerr := json.Unmarshal([]byte(existing), &old); jerr == nil {
					if rt, ok := old["refreshToken"].(string); ok && rt != "" {
						dataMap["refreshToken"] = rt
					}
				}
				dataBytes, _ = json.Marshal(dataMap)
			}
			_, err = h.Repo.RawDB().Exec(
				"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?",
				connName, string(dataBytes), now, connID,
			)
		} else {
			_, err = h.Repo.RawDB().Exec(
				"INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)",
				connID, provider, connName, string(dataBytes), now, now,
			)
		}
		if err != nil {
			log.Error("oauth", "save cline connection failed", "conn", connID, "error", err)
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
			return
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":       "authorized",
		"id":           connID,
		"connectionId": connID,
		"provider":     provider,
		"name":         connName,
	})
}

// decodeClineCode mirrors upstream: Cline embeds the token bundle as base64
// JSON in the code param ({accessToken, refreshToken, ...}). The browser
// callback URL-encodes the bundle, so '-'/'_' (base64url) arrive instead of
// '+//' — normalize both alphabets before decoding.
func decodeClineCode(code string) (access, refresh string, ok bool) {
	trimmed := strings.TrimSpace(code)
	// Strip the trailing signature segment the extension appends after the
	// JSON payload (base64url of the signature bytes, not padding).
	if i := strings.Index(trimmed, "}"); i >= 0 {
		if raw, err := base64.StdEncoding.DecodeString(padBase64(trimmed[:i+1])); err == nil {
			trimmed = string(raw)
		}
	}
	padded := padBase64(trimmed)
	raw, err := base64.URLEncoding.DecodeString(padded)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(padded)
		if err != nil {
			return "", "", false
		}
	}
	s := string(raw)
	if i := strings.LastIndex(s, "}"); i >= 0 {
		s = s[:i+1]
	} else {
		return "", "", false
	}
	var bundle struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal([]byte(s), &bundle); err != nil || bundle.AccessToken == "" {
		return "", "", false
	}
	return bundle.AccessToken, bundle.RefreshToken, true
}

// decodeClineIdentity extracts the account identity fields embedded in the
// base64url token bundle (email, firstName, lastName). Returns empties when
// the code is not a decodable bundle (e.g. the /auth/token path).
func decodeClineIdentity(code string) (email, firstName, lastName string) {
	trimmed := strings.TrimSpace(code)
	padded := padBase64(trimmed)
	raw, err := base64.URLEncoding.DecodeString(padded)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(padded)
		if err != nil {
			return "", "", ""
		}
	}
	s := string(raw)
	if i := strings.LastIndex(s, "}"); i >= 0 {
		s = s[:i+1]
	} else {
		return "", "", ""
	}
	var bundle struct {
		Email     string `json:"email"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	}
	if err := json.Unmarshal([]byte(s), &bundle); err != nil {
		return "", "", ""
	}
	return bundle.Email, bundle.FirstName, bundle.LastName
}

// padBase64 restores the '=' padding stripped from URL-safe payloads.
func padBase64(s string) string {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return s
}

// exchangeClineCode POSTs the authorization code to Cline's token endpoint
// using the snake_case extension contract.
func exchangeClineCode(ctx context.Context, tokenURL, code, verifier, redirectURI string) (access, refresh string, err error) {
	reqMap := map[string]string{
		"code":          code,
		"code_verifier": verifier,
		"redirect_uri":  redirectURI,
		"grant_type":    "authorization_code",
		"client_type":   "extension",
	}
	reqBody, err := json.Marshal(reqMap)
	if err != nil {
		return "", "", err
	}
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", err
	}
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenReq.Header.Set("Accept", "application/json")
	for k, v := range providers.ClineIdentityHeaders(updater.CurrentVersion, nil) {
		tokenReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return "", "", err
	}
	defer tokenResp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(tokenResp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if tokenResp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("status %d: %s", tokenResp.StatusCode, truncateTokenBody(respBody))
	}
	var parsed struct {
		Success bool `json:"success"`
		Error   any  `json:"error"`
		Data    struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"data"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", "", err
	}
	if !parsed.Success && parsed.Error != nil && fmt.Sprint(parsed.Error) != "" && fmt.Sprint(parsed.Error) != "<nil>" {
		return "", "", fmt.Errorf("%v", parsed.Error)
	}
	access = parsed.Data.AccessToken
	if access == "" {
		access = parsed.AccessToken
	}
	refresh = parsed.Data.RefreshToken
	if refresh == "" {
		refresh = parsed.RefreshToken
	}
	return access, refresh, nil
}
