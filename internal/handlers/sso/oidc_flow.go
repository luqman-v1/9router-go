package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// OIDC login flow.
//
// The settings panel could already test an issuer and probe a client secret,
// but the browser flows the login page links to — `/api/auth/oidc/start` and
// `/api/auth/oidc/callback` — were never registered. LoginView.svelte points at
// the first, so pressing "Sign in with SSO" on a configured install answered
// 404 and the operator had no way in.
//
// Authorization Code + PKCE, with the state, nonce and verifier carried in
// short-lived cookies so the callback can validate what it started. Upstream
// reference: src/app/api/auth/oidc/{start,callback}/route.js.

const (
	oidcStateCookie    = "oidc_state"
	oidcNonceCookie    = "oidc_nonce"
	oidcVerifierCookie = "oidc_code_verifier"

	// oidcFlowMaxAge bounds a half-finished login; upstream uses the same ten
	// minutes, and it is long enough for a user to sign in at the IdP.
	oidcFlowMaxAge = 10 * 60
)

// oidcSettings is what the flow reads from the stored settings.
type oidcSettings struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	Scopes       string
}

// readOIDCSettings returns the configured OIDC client, or false when the
// install has not configured one.
func (h *Handler) readOIDCSettings() (oidcSettings, bool) {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		return oidcSettings{}, false
	}
	cfg := oidcSettings{
		IssuerURL:    strings.TrimSpace(handlerutil.GetString(raw, "oidcIssuerUrl")),
		ClientID:     strings.TrimSpace(handlerutil.GetString(raw, "oidcClientId")),
		ClientSecret: handlerutil.GetString(raw, "oidcClientSecret"),
		Scopes:       strings.TrimSpace(handlerutil.GetString(raw, "oidcScopes")),
	}
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return oidcSettings{}, false
	}
	if cfg.Scopes == "" {
		cfg.Scopes = defaultScopes
	}
	return cfg, true
}

// HandleOIDCStart serves GET /api/auth/oidc/start: build the authorization URL
// and stash the flow's secrets in cookies.
func (h *Handler) HandleOIDCStart(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.readOIDCSettings()
	if !ok {
		redirectToLoginError(w, r, "oidc_not_configured")
		return
	}
	discovery, err := fetchDiscovery(cfg.IssuerURL)
	if err != nil {
		log.Warn("sso", "oidc discovery failed", "issuer", cfg.IssuerURL, "error", err)
		redirectToLoginError(w, r, "oidc_discovery_failed")
		return
	}
	if discovery.AuthorizationEndpoint == "" {
		redirectToLoginError(w, r, "oidc_no_authorization_endpoint")
		return
	}

	state := randomURLToken(32)
	nonce := randomURLToken(32)
	verifier := randomURLToken(48)
	challenge := pkceChallenge(verifier)
	redirectURI := publicOrigin(r) + oidcCallback

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {cfg.Scopes},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authURL := discovery.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}

	secure := isSecureRequest(r)
	for name, value := range map[string]string{
		oidcStateCookie:    state,
		oidcNonceCookie:    nonce,
		oidcVerifierCookie: verifier,
	} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: value, Path: "/", HttpOnly: true,
			Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: oidcFlowMaxAge,
		})
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleOIDCCallback serves GET /api/auth/oidc/callback: exchange the code,
// verify the id_token, and open a dashboard session.
func (h *Handler) HandleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	// The cookies are single-use whatever happens next.
	defer clearOIDCFlowCookies(w, r)

	if providerErr := r.URL.Query().Get("error"); providerErr != "" {
		redirectToLoginError(w, r, providerErr)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		redirectToLoginError(w, r, "oidc_missing_code")
		return
	}

	storedState, _ := readCookie(r, oidcStateCookie)
	nonce, _ := readCookie(r, oidcNonceCookie)
	verifier, _ := readCookie(r, oidcVerifierCookie)
	// Comparing with constant time would be better, but the values are
	// generated here and compared against the same cookie jar; a mismatch is
	// the whole failure mode and there is no secret to leak by timing.
	if storedState == "" || nonce == "" || verifier == "" || storedState != state {
		redirectToLoginError(w, r, "oidc_invalid_state")
		return
	}

	cfg, ok := h.readOIDCSettings()
	if !ok {
		redirectToLoginError(w, r, "oidc_not_configured")
		return
	}
	discovery, err := fetchDiscovery(cfg.IssuerURL)
	if err != nil {
		redirectToLoginError(w, r, "oidc_discovery_failed")
		return
	}

	redirectURI := publicOrigin(r) + oidcCallback
	token, err := exchangeOIDCCode(r.Context(), discovery.TokenEndpoint, cfg, code, redirectURI, verifier)
	if err != nil {
		log.Warn("sso", "oidc code exchange failed", "error", err)
		redirectToLoginError(w, r, "oidc_token_exchange_failed")
		return
	}
	if token.IDToken == "" {
		redirectToLoginError(w, r, "oidc_no_id_token")
		return
	}

	issuer := discovery.Issuer
	if issuer == "" {
		issuer = cfg.IssuerURL
	}
	claims, err := verifyOIDCIDToken(r.Context(), token.IDToken, issuer, cfg.ClientID, discovery.JwksURI, nonce)
	if err != nil {
		log.Warn("sso", "oidc id_token rejected", "error", err)
		redirectToLoginError(w, r, "oidc_invalid_id_token")
		return
	}

	session, err := auth.SignWith(auth.Secret(), time.Now(), auth.SessionClaims{
		Authenticated: true,
		Oidc:          true,
		OidcEmail:     pickClaim(claims, "email", "preferred_username", "upn"),
		OidcName:      pickClaim(claims, "name", "display_name", "given_name"),
	})
	if err != nil {
		redirectToLoginError(w, r, "oidc_session_failed")
		return
	}
	auth.SetCookie(w, r, session)
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// oidcTokenResponse is the token endpoint's answer.
type oidcTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// exchangeOIDCCode redeems the authorization code.
func exchangeOIDCCode(ctx context.Context, tokenEndpoint string, cfg oidcSettings, code, redirectURI, verifier string) (*oidcTokenResponse, error) {
	if tokenEndpoint == "" {
		return nil, fmt.Errorf("oidc: discovery has no token endpoint")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
	}
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var token oidcTokenResponse
	if err := json.UnmarshalRead(resp.Body, &token); err != nil {
		return nil, fmt.Errorf("oidc: parse token response: %w", err)
	}
	if token.Error != "" {
		return nil, fmt.Errorf("oidc: %s: %s", token.Error, token.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: token endpoint answered %d", resp.StatusCode)
	}
	return &token, nil
}

// oidcHTTPClient bounds every call in this flow.
var oidcHTTPClient = &http.Client{Timeout: 15 * time.Second}

// randomURLToken returns n bytes of entropy as base64url.
func randomURLToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// A flow token that is not random is a session fixation bug, so there
		// is nothing sensible to fall back to.
		panic("sso: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkceChallenge is S256(verifier).
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// clearOIDCFlowCookies removes the three flow cookies.
func clearOIDCFlowCookies(w http.ResponseWriter, r *http.Request) {
	secure := isSecureRequest(r)
	for _, name := range []string{oidcStateCookie, oidcNonceCookie, oidcVerifierCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", HttpOnly: true,
			Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
	}
}

// readCookie returns a cookie value, or "" when absent.
func readCookie(r *http.Request, name string) (string, bool) {
	c, err := r.Cookie(name)
	if err != nil || c == nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// isSecureRequest reports whether the browser reached us over TLS, so the flow
// cookies are only marked Secure where that is true (a local install over plain
// http would otherwise never receive them back).
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// redirectToLoginError sends the browser back to the login page with the reason
// in the query, which is what the SPA reads.
func redirectToLoginError(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(reason), http.StatusFound)
}

// pickClaim returns the first non-empty string claim among the names.
func pickClaim(claims map[string]any, names ...string) string {
	for _, n := range names {
		if v, ok := claims[n].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
