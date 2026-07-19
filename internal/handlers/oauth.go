package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

// HandleOAuthImport saves credentials from CLI token import (Codex, Cursor, GitLab, etc.).
// POST /api/oauth/{provider}/import
func (h *ChatHandler) HandleOAuthImport(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken,omitempty"`
		APIKey       string `json:"apiKey,omitempty"`
		MachineID    string `json:"machineId,omitempty"`
		Name         string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	credential := req.AccessToken
	if credential == "" {
		credential = req.APIKey
	}
	if credential == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing accessToken or apiKey")
		return
	}

	connName := req.Name
	if connName == "" {
		connName = provider + " import"
	}

	connID := fmt.Sprintf("%s-import-%d", provider, len(credential)%10000)

	// Build data JSON with provider-specific fields
	dataFields := map[string]any{
		"apiKey": credential,
	}
	if req.RefreshToken != "" {
		dataFields["refreshToken"] = req.RefreshToken
	}
	if req.MachineID != "" {
		dataFields["providerSpecificData"] = map[string]any{
			"machineId": req.MachineID,
		}
	}

	data, _ := json.Marshal(dataFields)

	now := currentTimestamp()
	_, err = h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'apikey', ?, 1, ?, ?, ?)`,
		connID, provider, connName, string(data), now, now,
	)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("save connection: %v", err))
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":         connID,
		"provider":   provider,
		"name":       connName,
		"connection": connID,
	})
}

// HandleOAuthKiroSocialAuthorize generates Kiro social auth URL with PKCE.
// GET /api/oauth/kiro/social-authorize?provider=google|github
func (h *ChatHandler) HandleOAuthKiroSocialAuthorize(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("provider")
	if p != "google" && p != "github" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid provider, use 'google' or 'github'")
		return
	}

	// Generate PKCE challenge
	codeVerifier := randomString(64)
	codeChallenge := sha256Base64(codeVerifier)
	state := randomString(32)

	// Build Kiro social auth URL (AWS Cognito hosted UI)
	clientID := "38k1nvcot3m5po4oi5f1jt0s46" // Kiro's Cognito client ID
	redirectURI := "kiro://oauth"
	authURL := fmt.Sprintf(
		"https://kiro-auth-pool.auth.us-east-1.amazoncognito.com/oauth2/authorize?identity_provider=%s&response_type=code&client_id=%s&redirect_uri=%s&scope=openid+email+profile&state=%s&code_challenge_method=S256&code_challenge=%s",
		titleProvider(p), clientID, redirectURI, state, codeChallenge,
	)

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"authUrl":       authURL,
		"state":         state,
		"codeVerifier":  codeVerifier,
		"codeChallenge": codeChallenge,
		"provider":      p,
	})
}

// HandleOAuthKiroSocialExchange exchanges auth code for Kiro tokens.
// POST /api/oauth/kiro/social-exchange
func (h *ChatHandler) HandleOAuthKiroSocialExchange(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"codeVerifier"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code")
		return
	}

	// Exchange code for tokens via Cognito token endpoint
	tokenURL := "https://kiro-auth-pool.auth.us-east-1.amazoncognito.com/oauth2/token"
	exchangeBody := fmt.Sprintf(
		"grant_type=authorization_code&client_id=%s&code=%s&redirect_uri=kiro://oauth&code_verifier=%s",
		"38k1nvcot3m5po4oi5f1jt0s46", req.Code, req.CodeVerifier,
	)

	tokenResp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(exchangeBody))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange failed: %v", err))
		return
	}
	defer tokenResp.Body.Close()

	var tokenData map[string]any
	json.NewDecoder(tokenResp.Body).Decode(&tokenData)
	if accessToken, ok := tokenData["access_token"].(string); ok {
		// Save as kiro provider connection
		connID := fmt.Sprintf("kiro-oauth-%d", len(accessToken)%10000)
		dataMap := map[string]any{
			"accessToken": accessToken,
		}
		if idToken, ok := tokenData["id_token"].(string); ok {
			dataMap["idToken"] = idToken
		}
		if refreshToken, ok := tokenData["refresh_token"].(string); ok {
			dataMap["refreshToken"] = refreshToken
		}
		data, _ := json.Marshal(dataMap)
		now := currentTimestamp()
		h.Repo.RawDB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)`,
			connID, "kiro", "Kiro Social", string(data), now, now,
		)
		tokenData["id"] = connID
	}

	handlerutil.WriteJSON(w, http.StatusOK, tokenData)
}

// HandleOAuthCodexBulkImport handles bulk Codex token import.
// POST /api/oauth/codex/bulk-import
func (h *ChatHandler) HandleOAuthCodexBulkImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Tokens []struct {
			AccessToken string `json:"accessToken"`
			Name        string `json:"name,omitempty"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var imported []string
	for _, t := range req.Tokens {
		if t.AccessToken == "" {
			continue
		}
		name := t.Name
		if name == "" {
			name = "Codex import"
		}
		connID := fmt.Sprintf("codex-bulk-%d", len(t.AccessToken)%10000)
		data, _ := json.Marshal(map[string]string{"accessToken": t.AccessToken})
		now := currentTimestamp()
		_, err := h.Repo.RawDB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'codex', 'oauth', ?, 1, ?, ?, ?)`,
			connID, name, string(data), now, now,
		)
		if err == nil {
			imported = append(imported, connID)
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"imported": imported,
		"count":    len(imported),
	})
}

// currentTimestamp returns an RFC3339 timestamp string.
func titleProvider(p string) string {
	if p == "google" {
		return "Google"
	}
	if p == "github" {
		return "GitHub"
	}
	return p
}

func currentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// randomString generates a random alphanumeric string of length n.
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		randInt, _ := cryptoRandInt(len(letters))
		b[i] = letters[randInt%len(letters)]
	}
	return string(b)
}

// cryptoRandInt returns a random int in [0, max).
func cryptoRandInt(max int) (int, error) {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int(b[0]) % max, nil
}

// sha256Base64 returns the base64url-encoded SHA256 digest of input.
func sha256Base64(input string) string {
	h := sha256.Sum256([]byte(input))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
