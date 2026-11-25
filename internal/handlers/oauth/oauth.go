package oauth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	mathRand "math/rand"
	"net/http"
	"net/url"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

var (
	kiroSocialTokenURL   = "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/token"
	kiroSocialHTTPClient = &http.Client{Timeout: 15 * time.Second}
)

// OAuthHandler handles OAuth token import and social auth exchange endpoints.
type OAuthHandler struct {
	Repo      *db.Repo
	callbacks *callbackRelay
}

// NewOAuthHandler initializes an OAuthHandler.
func NewOAuthHandler(repo *db.Repo) *OAuthHandler {
	return &OAuthHandler{
		Repo:      repo,
		callbacks: defaultCallbackRelay,
	}
}

func (h *OAuthHandler) callbackStore() *callbackRelay {
	if h != nil && h.callbacks != nil {
		return h.callbacks
	}
	return defaultCallbackRelay
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

	connID := provider + "-import-" + randomString(12)

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
// Upstream parity (KiroService.buildSocialLoginUrl): the desktop auth service
// (prod.us-east-1.auth.desktop.kiro.dev/login), NOT the Cognito hosted UI —
// Cognito only whitelists the kiro:// protocol and rejects localhost.
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

	idp := "Google"
	if p == "github" {
		idp = "Github"
	}
	redirectURI := "kiro://kiro.kiroAgent/authenticate-success"
	authURL := fmt.Sprintf(
		"https://prod.us-east-1.auth.desktop.kiro.dev/login?idp=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=%s&prompt=select_account",
		idp, url.QueryEscape(redirectURI), codeChallenge, state,
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
// Upstream parity (KiroService.exchangeSocialCode): the desktop auth service
// /oauth/token (JSON contract), not the Cognito form endpoint. The redirect
// URI must match the authorize step or the exchange is rejected.
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
		Provider     string `json:"provider"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Code == "" || req.CodeVerifier == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code or codeVerifier")
		return
	}
	if req.Provider != "" && req.Provider != "google" && req.Provider != "github" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid provider, use 'google' or 'github'")
		return
	}

	exchangePayload, err := json.Marshal(map[string]string{
		"code":          req.Code,
		"code_verifier": req.CodeVerifier,
		"redirect_uri":  "kiro://kiro.kiroAgent/authenticate-success",
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode exchange request")
		return
	}
	tokenReq, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodPost,
		kiroSocialTokenURL,
		bytes.NewReader(exchangePayload),
	)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build exchange request")
		return
	}
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenResp, err := kiroSocialHTTPClient.Do(tokenReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange failed: %v", err))
		return
	}
	defer tokenResp.Body.Close()

	var tokenData struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}
	rawBody, err := io.ReadAll(io.LimitReader(tokenResp.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read token response")
		return
	}
	if tokenResp.StatusCode != http.StatusOK {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange returned %d: %s", tokenResp.StatusCode, truncateForError(rawBody)))
		return
	}
	if err := json.Unmarshal(rawBody, &tokenData); err != nil || tokenData.AccessToken == "" {
		log.Error("oauth", "decode kiro social token response failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to decode token response")
		return
	}

	// Save as kiro provider connection (upstream social-exchange parity).
	connID := "kiro-oauth-" + randomString(12)
	expiresIn := tokenData.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	dataMap := map[string]any{
		"accessToken":  tokenData.AccessToken,
		"refreshToken": tokenData.RefreshToken,
		"expiresAt":    time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339),
		"providerSpecificData": map[string]any{
			"profileArn": tokenData.ProfileArn,
			"authMethod": req.Provider,
			"provider":   titleProvider(req.Provider),
		},
	}
	data, err := json.Marshal(dataMap)
	if err != nil {
		log.Error("oauth", "marshal Kiro social data failed", "error", err)
	} else if h.Repo != nil && h.Repo.RawDB() != nil {
		now := currentTimestamp()
		if _, err := h.Repo.RawDB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)`,
			connID, "kiro", "Kiro Social", string(data), now, now,
		); err != nil {
			log.Error("oauth", "save Kiro social connection failed", "error", err)
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":       connID,
			"provider": "kiro",
		},
	})
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
		connID := "codex-bulk-" + randomString(12)
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

// truncateForError caps upstream bodies in error messages so token endpoints
// that echo the request cannot leak credentials into logs or API responses.
func truncateForError(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
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
