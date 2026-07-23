package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	mathRand "math/rand"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// OAuthHandler handles OAuth token import and social auth exchange endpoints.
type OAuthHandler struct {
	Repo *db.Repo
}

// NewOAuthHandler initializes an OAuthHandler.
func NewOAuthHandler(repo *db.Repo) *OAuthHandler {
	return &OAuthHandler{Repo: repo}
}

// HandleOAuthImport saves credentials from CLI token import (Codex, Cursor, GitLab, etc.).
// POST /api/oauth/{provider}/import
func (h *OAuthHandler) HandleOAuthImport(w http.ResponseWriter, r *http.Request) {
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

	data, err := json.Marshal(dataFields)
	if err != nil {
		log.Error("oauth", "marshal import data failed", "provider", provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to process connection data")
		return
	}

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
func (h *OAuthHandler) HandleOAuthKiroSocialAuthorize(w http.ResponseWriter, r *http.Request) {
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
func (h *OAuthHandler) HandleOAuthKiroSocialExchange(w http.ResponseWriter, r *http.Request) {
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
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		log.Error("oauth", "decode token response failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to decode token response")
		return
	}
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
		data, err := json.Marshal(dataMap)
		if err != nil {
			log.Error("oauth", "marshal Kiro social data failed", "error", err)
		} else {
			now := currentTimestamp()
			if _, err := h.Repo.RawDB().Exec(
				`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)`,
				connID, "kiro", "Kiro Social", string(data), now, now,
			); err != nil {
				log.Error("oauth", "save Kiro social connection failed", "error", err)
			}
		}
		tokenData["id"] = connID
	}

	handlerutil.WriteJSON(w, http.StatusOK, tokenData)
}

// HandleOAuthCodexBulkImport handles bulk Codex token import.
// POST /api/oauth/codex/bulk-import
func (h *OAuthHandler) HandleOAuthCodexBulkImport(w http.ResponseWriter, r *http.Request) {
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
		data, err := json.Marshal(map[string]string{"accessToken": t.AccessToken})
		if err != nil {
			log.Error("oauth", "marshal Codex bulk import failed", "error", err)
			continue
		}
		now := currentTimestamp()
		_, err = h.Repo.RawDB().Exec(
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

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		rng := mathRand.New(mathRand.NewSource(time.Now().UnixNano()))
		for i := range b {
			b[i] = letters[rng.Intn(len(letters))]
		}
		return string(b)
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

func sha256Base64(input string) string {
	h := sha256.Sum256([]byte(input))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
