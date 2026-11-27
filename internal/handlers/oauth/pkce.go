package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// pkceConfig describes a standard OAuth2 authorization_code+PKCE provider.
// Mirrors the upstream 9router provider modules (src/lib/oauth/providers/*).
type pkceConfig struct {
	providers      []string
	clientID       string
	authorizeURL   string
	tokenURL       string
	scope          string
	extraAuth      map[string]string
	exchangeJSON   bool
	includeState   bool
	userInfoPath   string // appended to baseURL, e.g. GitLab /api/v4/user
	discoverIssuer string // OIDC issuer for runtime discovery (xAI)
	// fixedRedirectURI pins the callback URL for providers whose public OAuth
	// client only registered one loopback URI (Codex). auth.openai.com
	// validates redirect_uri at the authorize step and answers
	// invalid_authorize_request for anything else, so the dashboard-derived
	// <host>/callback must not be used. Empty means "follow the dashboard".
	fixedRedirectURI string
	// fixedPort is the loopback port fixedRedirectURI lives on. A provider with
	// fixedRedirectURI always needs a local listener bound to it; see
	// codex_proxy.go.
	fixedPort      int
	connPrefix     string
	display        string
	emailFromIDTok bool
}

var pkceProviders = map[string]*pkceConfig{
	"claude": {
		providers:    []string{"claude"},
		clientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
		authorizeURL: "https://claude.ai/oauth/authorize",
		tokenURL:     "https://api.anthropic.com/v1/oauth/token",
		scope:        "org:create_api_key user:profile user:inference",
		extraAuth:    map[string]string{"code": "true"},
		exchangeJSON: true,
		includeState: true,
		connPrefix:   "claude-",
		display:      "Claude",
	},
	"codex": {
		providers:    []string{"codex"},
		clientID:     "app_EMoamEEZ73f0CkXaXp7hrann",
		authorizeURL: "https://auth.openai.com/oauth/authorize",
		tokenURL:     "https://auth.openai.com/oauth/token",
		scope:        "openid profile email offline_access",
		extraAuth: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
			"originator":                 "codex_cli_rs",
		},
		connPrefix:     "cx-",
		display:        "Codex",
		emailFromIDTok: true,
		// Codex CLI's OAuth client has exactly one registered loopback
		// redirect URI. Verified against auth.openai.com: both
		// http://localhost:<dashboardPort>/callback and
		// http://localhost:1455/callback are rejected with
		// invalid_authorize_request; only this literal reaches the login page.
		fixedRedirectURI: codexRedirectURI,
		fixedPort:        codexProxyPort,
	},
	"xai": {
		providers:      []string{"xai"},
		clientID:       "b1a00492-073a-47ea-816f-4c329264a828",
		authorizeURL:   "https://auth.x.ai/oauth2/authorize",
		tokenURL:       "https://auth.x.ai/oauth2/token",
		scope:          "openid profile email offline_access grok-cli:access api:access",
		extraAuth:      map[string]string{"plan": "generic", "referrer": "cli-proxy-api"},
		discoverIssuer: "https://auth.x.ai",
		connPrefix:     "xai-",
		display:        "xAI",
		emailFromIDTok: true,
	},
	"gitlab": {
		providers:    []string{"gitlab"},
		authorizeURL: "https://gitlab.com/oauth/authorize",
		tokenURL:     "https://gitlab.com/oauth/token",
		scope:        "api read_user",
		userInfoPath: "/api/v4/user",
		connPrefix:   "gl-",
		display:      "GitLab",
	},
}

func pkceLookup(provider string) *pkceConfig {
	if cfg, ok := pkceProviders[provider]; ok {
		return cfg
	}
	return nil
}

// HandlePKCEAuthorize returns the authorization URL + PKCE challenge for a provider.
// GET /api/oauth/pkce/authorize?provider=claude|codex|xai|gitlab&redirect_uri=...&state=...
func (h *OAuthHandler) HandlePKCEAuthorize(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	cfg := pkceLookup(provider)
	if cfg == nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported provider (want claude|codex|xai|gitlab)")
		return
	}
	redirectURI := callbackRedirectURIFor(cfg, r)
	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}
	verifier := pkceVerifier()
	challenge := sha256Base64(verifier)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {cfg.scope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	// GitLab: allow self-hosted base + user OAuth app credentials.
	authorizeURL, tokenURL := cfg.authorizeURL, cfg.tokenURL
	if provider == "gitlab" {
		base := strings.TrimSuffix(r.URL.Query().Get("baseUrl"), "/")
		if base == "" {
			base = "https://gitlab.com"
		}
		authorizeURL, tokenURL = base+"/oauth/authorize", base+"/oauth/token"
		if cid := r.URL.Query().Get("clientId"); cid != "" {
			params.Set("client_id", cid)
		}
	}
	for k, v := range cfg.extraAuth {
		params.Set(k, v)
	}
	// xAI: fresh nonce per request + runtime OIDC discovery.
	if provider == "xai" {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err == nil {
			params.Set("nonce", fmt.Sprintf("%x", nonce))
		}
		if disc, err := discoverOIDCEndpoints(cfg.discoverIssuer); err == nil {
			authorizeURL, tokenURL = disc.authorizeURL, disc.tokenURL
		}
	}

	authURL := authorizeURL + "?" + params.Encode()
	_ = tokenURL
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url":           authURL,
		"authUrl":       authURL,
		"state":         state,
		"codeVerifier":  verifier,
		"codeChallenge": challenge,
		"redirectUri":   redirectURI,
		"flowType":      "authorization_code_pkce",
		"provider":      provider,
	})
}

// pkceExchangeBody is the JSON payload accepted by POST /api/oauth/pkce/exchange.
type pkceExchangeBody struct {
	Provider     string `json:"provider"`
	Code         string `json:"code"`
	CodeVerifier string `json:"codeVerifier"`
	RedirectURI  string `json:"redirectUri"`
	RedirectUri  string `json:"redirect_uri"`
	State        string `json:"state"`
	Name         string `json:"name"`
	BaseURL      string `json:"baseUrl"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// pkceExchange is one resolved authorization_code exchange. Resolving it once
// keeps the HTTP handler and the Codex loopback proxy — which receives the
// callback on a separate listener and has no dashboard request to parse — on
// exactly one exchange code path.
type pkceExchange struct {
	cfg          *pkceConfig
	provider     string
	code         string
	codeVerifier string
	state        string
	name         string
	redirectURI  string
	clientID     string
	clientSecret string
	tokenURL     string
	base         string // self-hosted GitLab base, "" for hosted providers
}

// pkceTokens is the token endpoint's response, normalized across providers.
type pkceTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scope        string
	IDToken      string
}

// pkceExchangeResult reports a completed exchange to HTTP and proxy callers.
type pkceExchangeResult struct {
	ConnectionID string
	Name         string
	Email        string
}

// exchangeError carries the HTTP status a failed exchange step surfaces as.
type exchangeError struct {
	status  int
	message string
}

func (e *exchangeError) Error() string { return e.message }

func exchangeFail(status int, format string, args ...any) *exchangeError {
	return &exchangeError{status: status, message: fmt.Sprintf(format, args...)}
}

// resolvePKCEExchange validates the request and fills in the provider's
// endpoints. The only network I/O is xAI's OIDC discovery; everything else is
// pure validation so a bad request never reaches the token endpoint.
func resolvePKCEExchange(body pkceExchangeBody, r *http.Request) (*pkceExchange, *exchangeError) {
	cfg := pkceLookup(body.Provider)
	if cfg == nil {
		return nil, exchangeFail(http.StatusBadRequest, "unsupported provider (want claude|codex|xai|gitlab)")
	}
	code := body.Code
	if i := strings.Index(code, "#"); i >= 0 { // Claude appends state after #
		code = code[:i]
	}
	if code == "" {
		return nil, exchangeFail(http.StatusBadRequest, "missing code parameter")
	}
	if body.CodeVerifier == "" {
		return nil, exchangeFail(http.StatusBadRequest, "missing codeVerifier parameter")
	}

	ex := &pkceExchange{
		cfg:          cfg,
		provider:     body.Provider,
		code:         code,
		codeVerifier: body.CodeVerifier,
		state:        body.State,
		name:         body.Name,
		redirectURI:  exchangeRedirectURIFor(cfg, r, body.RedirectURI, body.RedirectUri),
		clientID:     cfg.clientID,
		clientSecret: body.ClientSecret,
		tokenURL:     cfg.tokenURL,
	}
	if body.Provider == "gitlab" {
		ex.base = strings.TrimSuffix(body.BaseURL, "/")
		if ex.base == "" {
			ex.base = "https://gitlab.com"
		}
		ex.tokenURL = ex.base + "/oauth/token"
		if body.ClientID != "" {
			ex.clientID = body.ClientID
		}
	}
	if body.Provider == "xai" {
		if disc, err := discoverOIDCEndpoints(cfg.discoverIssuer); err == nil {
			ex.tokenURL = disc.tokenURL
		}
	}
	return ex, nil
}

// requestToken swaps the authorization code for tokens. Providers differ only
// in request encoding (Claude uses a JSON body, everyone else form-encoded)
// and in whether state rides along.
func (ex *pkceExchange) requestToken(ctx context.Context) (*pkceTokens, *exchangeError) {
	var tokenReq *http.Request
	var err error
	if ex.cfg.exchangeJSON { // Claude: JSON body incl. state
		payload := map[string]string{
			"code":          ex.code,
			"grant_type":    "authorization_code",
			"client_id":     ex.clientID,
			"redirect_uri":  ex.redirectURI,
			"code_verifier": ex.codeVerifier,
		}
		if ex.cfg.includeState {
			payload["state"] = ex.state
		}
		raw, _ := json.Marshal(payload)
		if tokenReq, err = http.NewRequestWithContext(ctx, http.MethodPost, ex.tokenURL, bytes.NewReader(raw)); err != nil {
			return nil, exchangeFail(http.StatusInternalServerError, "create token request failed")
		}
		tokenReq.Header.Set("Content-Type", "application/json")
	} else { // codex / xai / gitlab: form body
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {ex.clientID},
			"code":          {ex.code},
			"redirect_uri":  {ex.redirectURI},
			"code_verifier": {ex.codeVerifier},
		}
		if ex.clientSecret != "" {
			form.Set("client_secret", ex.clientSecret)
		}
		if tokenReq, err = http.NewRequestWithContext(ctx, http.MethodPost, ex.tokenURL, strings.NewReader(form.Encode())); err != nil {
			return nil, exchangeFail(http.StatusInternalServerError, "create token request failed")
		}
		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	tokenReq.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return nil, exchangeFail(http.StatusBadGateway, "token exchange failed: %v", err)
	}
	defer tokenResp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(tokenResp.Body, 1<<20))
	if err != nil {
		return nil, exchangeFail(http.StatusBadGateway, "failed to read token response")
	}
	if tokenResp.StatusCode != http.StatusOK {
		log.Warn("oauth", "pkce token exchange non-200", "provider", ex.provider, "status", tokenResp.StatusCode, "bytes", len(respBody))
		return nil, exchangeFail(http.StatusBadGateway, "token exchange returned status %d: %s", tokenResp.StatusCode, string(respBody))
	}
	var wire struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return nil, exchangeFail(http.StatusBadGateway, "failed to decode token response")
	}
	if wire.AccessToken == "" {
		return nil, exchangeFail(http.StatusBadGateway, "missing access_token in token response")
	}
	return &pkceTokens{
		AccessToken:  wire.AccessToken,
		RefreshToken: wire.RefreshToken,
		ExpiresIn:    wire.ExpiresIn,
		Scope:        wire.Scope,
		IDToken:      wire.IDToken,
	}, nil
}

// buildConnectionData maps tokens plus provider-specific claims onto the
// connection's stored data blob. Codex additionally keeps the ChatGPT account
// id and plan from the id_token: the codex endpoint rejects requests that do
// not carry chatgpt-account-id, and plan type drives quota routing.
func (ex *pkceExchange) buildConnectionData(ctx context.Context, tokens *pkceTokens) (map[string]any, string) {
	email, extra := "", map[string]any{}
	if ex.cfg.emailFromIDTok {
		info := codexAccountInfo(tokens.IDToken)
		email = firstNonEmpty(info.Email, extractEmailFromJWT(tokens.IDToken))
		if tokens.IDToken != "" {
			extra["idToken"] = tokens.IDToken
		}
		if info.AccountID != "" || info.PlanType != "" {
			psd := map[string]any{}
			if info.AccountID != "" {
				psd["chatgptAccountId"] = info.AccountID
			}
			if info.PlanType != "" {
				psd["chatgptPlanType"] = info.PlanType
			}
			extra["providerSpecificData"] = psd
		}
	}
	if ex.cfg.userInfoPath != "" && ex.base != "" { // GitLab: fetch user profile
		if u, err := fetchOAuthUserInfo(ctx, ex.base+ex.cfg.userInfoPath, tokens.AccessToken); err == nil {
			if v, _ := u["email"].(string); v != "" {
				email = v
			} else if v, _ := u["public_email"].(string); v != "" {
				email = v
			}
			extra["gitlabUser"] = u
			extra["gitlabBaseUrl"] = ex.base
		}
	}

	dataMap := map[string]any{"apiKey": tokens.AccessToken, "accessToken": tokens.AccessToken}
	if tokens.RefreshToken != "" {
		dataMap["refreshToken"] = tokens.RefreshToken
	}
	if email != "" {
		dataMap["email"] = email
	}
	for k, v := range extra {
		dataMap[k] = v
	}
	if tokens.ExpiresIn > 0 {
		dataMap["expiresAt"] = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return dataMap, email
}

// validateCodexConnectionData fails a codex login whose id_token carried no
// ChatGPT account id. The codex backend rejects every request without the
// chatgpt-account-id header, so persisting such a connection would authorize
// a login that can never complete a single call.
func validateCodexConnectionData(provider string, dataMap map[string]any) *exchangeError {
	if provider != "codex" {
		return nil
	}
	if psd, _ := dataMap["providerSpecificData"].(map[string]any); strVal(psd, "chatgptAccountId") == "" {
		return exchangeFail(http.StatusBadGateway,
			"codex login did not return a ChatGPT account id — please try logging in again")
	}
	return nil
}

// completePKCEExchange runs the token swap and persists the resulting
// connection, returning the outcome shared by the HTTP and proxy paths.
func (h *OAuthHandler) completePKCEExchange(ctx context.Context, ex *pkceExchange) (pkceExchangeResult, *exchangeError) {
	tokens, fail := ex.requestToken(ctx)
	if fail != nil {
		return pkceExchangeResult{}, fail
	}
	dataMap, email := ex.buildConnectionData(ctx, tokens)
	if fail := validateCodexConnectionData(ex.provider, dataMap); fail != nil {
		return pkceExchangeResult{}, fail
	}
	connName := connectionDisplayName(ex.provider, ex.name, email, ex.cfg.display)
	connID := ex.cfg.connPrefix + shortHash(tokens.AccessToken)
	if err := h.persistConnection(ctx, connID, ex.provider, connName, dataMap, tokens.RefreshToken); err != nil {
		return pkceExchangeResult{}, exchangeFail(http.StatusInternalServerError, "failed to save connection: %v", err)
	}
	return pkceExchangeResult{ConnectionID: connID, Name: connName, Email: email}, nil
}

// persistConnection upserts a connection row, keeping the stored refresh token
// when the provider's response omitted one (Codex only rotates it on the
// first exchange).
func (h *OAuthHandler) persistConnection(ctx context.Context, connID, provider, connName string, dataMap map[string]any, refreshToken string) error {
	if h.Repo == nil {
		return nil
	}
	existing, _ := h.Repo.GetProviderConnectionByID(connID)
	if refreshToken == "" && existing != nil {
		var old map[string]any
		if jerr := json.Unmarshal([]byte(existing.Data), &old); jerr == nil {
			if rt, ok := old["refreshToken"].(string); ok && rt != "" {
				dataMap["refreshToken"] = rt
			}
		}
	}
	dataBytes, err := json.Marshal(dataMap)
	if err != nil {
		return fmt.Errorf("marshal connection data: %w", err)
	}
	if existing != nil {
		return h.Repo.ReplaceProviderConnectionPayload(ctx, connID, connName, string(dataBytes))
	}
	priority := h.Repo.ResolveDefaultPriority(ctx, provider)
	return h.Repo.CreateProviderConnectionFull(ctx, connID, provider, "oauth", connName, &priority, string(dataBytes))
}

// HandlePKCEExchange exchanges the authorization code and stores the connection.
// POST /api/oauth/pkce/exchange
func (h *OAuthHandler) HandlePKCEExchange(w http.ResponseWriter, r *http.Request) {
	var body pkceExchangeBody
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	ex, fail := resolvePKCEExchange(body, r)
	if fail != nil {
		handlerutil.WriteJSONError(w, fail.status, fail.message)
		return
	}
	res, fail := h.completePKCEExchange(r.Context(), ex)
	if fail != nil {
		handlerutil.WriteJSONError(w, fail.status, fail.message)
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "authorized", "id": res.ConnectionID, "connectionId": res.ConnectionID,
		"provider": ex.provider, "name": res.Name, "email": res.Email,
	})
}

// pkceVerifier generates a PKCE code verifier (unreserved chars only).
func pkceVerifier() string {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return randomString(64)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

type oidcEndpoints struct{ authorizeURL, tokenURL string }

// discoverOIDCEndpoints fetches OIDC discovery, falling back to static config.
func discoverOIDCEndpoints(issuer string) (oidcEndpoints, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration")
	if err != nil {
		return oidcEndpoints{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return oidcEndpoints{}, fmt.Errorf("discovery status %d", resp.StatusCode)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		return oidcEndpoints{}, fmt.Errorf("bad discovery doc")
	}
	if !strings.HasPrefix(doc.AuthorizationEndpoint, "https://") || !strings.HasPrefix(doc.TokenEndpoint, "https://") {
		return oidcEndpoints{}, fmt.Errorf("non-https discovery endpoints")
	}
	return oidcEndpoints{doc.AuthorizationEndpoint, doc.TokenEndpoint}, nil
}

// fetchOAuthUserInfo GETs a bearer-authenticated userinfo endpoint.
func fetchOAuthUserInfo(ctx context.Context, userInfoURL, accessToken string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
