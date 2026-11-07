package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// Perch ships no public API (pricing FAQ: direct API access is not included in
// any plan, and perch_surface_required is the published refusal). What it does
// ship is the CLI + web client, which sign in through Perch's own Supabase
// project with a PKCE flow (pi-perchai, PerchAI2API, zoeee1125/perchai2api all
// re-implement the same exchange). The dashboard needs that flow to offer the
// connection at all, so it lives here:
//
//	GET  /api/oauth/perch/authorize?provider=google|github
//	POST /api/oauth/perch/exchange   {code, codeVerifier, provider}

// HandlePerchAuthorize starts the Supabase PKCE sign-in.
// GET /api/oauth/perch/authorize?provider=google|github
func (h *OAuthHandler) HandlePerchAuthorize(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("provider")
	if p != "google" && p != "github" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid provider, use 'google' or 'github'")
		return
	}

	verifier := randomString(64)
	challenge := sha256Base64(verifier)
	state := randomString(32)

	authURL, err := url.Parse(providers.PerchSupabaseURL + "/auth/v1/authorize")
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build authorize URL")
		return
	}
	q := authURL.Query()
	q.Set("provider", p)
	q.Set("redirect_to", providers.PerchOAuthCallback)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL.RawQuery = q.Encode()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url": authURL.String(), "authUrl": authURL.String(),
		"state": state, "codeVerifier": verifier, "codeChallenge": challenge,
		"redirectUri": providers.PerchOAuthCallback, "provider": p,
	})
}

// HandlePerchExchange exchanges the PKCE callback code and stores the
// connection.
// POST /api/oauth/perch/exchange
func (h *OAuthHandler) HandlePerchExchange(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"codeVerifier"`
		Provider     string `json:"provider"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Code == "" || req.CodeVerifier == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code or codeVerifier")
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}
	cfg, err := perchFetchConfig(r.Context(), client)
	if err != nil {
		log.Error("oauth", "perch auth config failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read Perch auth config")
		return
	}

	payload, _ := json.Marshal(map[string]string{"auth_code": req.Code, "code_verifier": req.CodeVerifier})
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		providers.PerchSupabaseURL+"/auth/v1/token?grant_type=pkce", strings.NewReader(string(payload)))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode exchange request")
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("apikey", cfg.SupabaseAnonKey)
	httpReq.Header.Set("User-Agent", providers.PerchCLIUserAgent)

	resp, err := client.Do(httpReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange failed: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read token response")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange returned %d: %s", resp.StatusCode, truncatePerch(out)))
		return
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		ExpiresAt    int64  `json:"expires_at"`
		User         struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(out, &tokenData); err != nil || tokenData.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to decode token response")
		return
	}

	expiresIn := tokenData.ExpiresIn
	if expiresIn <= 0 && tokenData.ExpiresAt > 0 {
		if d := tokenData.ExpiresAt - time.Now().Unix(); d > 0 {
			expiresIn = int(d)
		}
	}
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	connID := "perch-oauth-" + perchShortID(tokenData.User.ID)
	name := req.Name
	if name == "" {
		name = "Perch"
		if tokenData.User.Email != "" {
			name += " (" + tokenData.User.Email + ")"
		}
	}
	dataMap := map[string]any{
		"accessToken":  tokenData.AccessToken,
		"refreshToken": tokenData.RefreshToken,
		"apiKey":       tokenData.AccessToken,
		"expiresAt":    time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339),
		"email":        tokenData.User.Email,
		"providerSpecificData": map[string]any{
			"userId":     tokenData.User.ID,
			"authMethod": req.Provider,
		},
	}
	h.saveSpecialConnection(w, "perch", connID, name, tokenData.User.Email, dataMap, nil)
}

func perchFetchConfig(ctx context.Context, client *http.Client) (*perchConfigFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, providers.PerchAppURL+"/api/perch-terminal/cli-auth/config", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providers.PerchCLIUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("config %d", resp.StatusCode)
	}
	var cfg perchConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSuffix(cfg.SupabaseURL, "/") != providers.PerchSupabaseURL {
		return nil, fmt.Errorf("unexpected Supabase project %q", cfg.SupabaseURL)
	}
	if cfg.SupabaseAnonKey == "" {
		return nil, fmt.Errorf("incomplete config")
	}
	return &cfg, nil
}

type perchConfigFile struct {
	SupabaseURL     string   `json:"supabaseUrl"`
	SupabaseAnonKey string   `json:"supabaseAnonKey"`
	Providers       []string `json:"providers"`
}

func perchShortID(userID string) string {
	if userID == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:6])
}

func truncatePerch(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}
