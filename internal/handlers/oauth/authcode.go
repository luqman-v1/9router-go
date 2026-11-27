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
)

// authcodeConfig describes a plain OAuth2 authorization_code provider
// (no PKCE). Mirrors upstream 9router provider modules.
type authcodeConfig struct {
	providers    []string
	clientID     string
	clientEnv    string
	secret       string
	secretEnv    string
	authorizeURL string
	tokenURL     string
	scopes       []string
	extraAuth    map[string]string
	useBasic     bool   // iFlow: Basic(clientId:secret) on exchange
	userInfoURL  string // iFlow: apiKey lives in userinfo, not tokens
	connPrefix   string
	display      string
}

var authcodeProviders = map[string]*authcodeConfig{
	"gemini-cli": {
		providers:    []string{"gemini-cli"},
		clientID:     "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com",
		clientEnv:    "GEMINI_CLI_OAUTH_CLIENT_ID",
		secret:       "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",
		secretEnv:    "GEMINI_CLI_OAUTH_CLIENT_SECRET",
		authorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:     "https://oauth2.googleapis.com/token",
		scopes: []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		userInfoURL: "https://www.googleapis.com/oauth2/v1/userinfo",
		connPrefix:  "gc-",
		display:     "Gemini CLI",
	},
	"iflow": {
		providers:    []string{"iflow"},
		clientID:     "10009311001",
		clientEnv:    "IFLOW_OAUTH_CLIENT_ID",
		secret:       "4Z3YjXycVsQvyGF1etiNlIBB4RsqSDtW",
		secretEnv:    "IFLOW_OAUTH_CLIENT_SECRET",
		authorizeURL: "https://iflow.cn/oauth",
		tokenURL:     "https://iflow.cn/oauth/token",
		extraAuth:    map[string]string{"loginMethod": "phone", "type": "phone"},
		useBasic:     true,
		userInfoURL:  "https://iflow.cn/api/oauth/getUserInfo",
		connPrefix:   "if-",
		display:      "iFlow",
	},
}

func authcodeLookup(provider string) *authcodeConfig {
	if cfg, ok := authcodeProviders[provider]; ok {
		return cfg
	}
	return nil
}

func (c *authcodeConfig) creds() (id, secret string) {
	id, secret = c.clientID, c.secret
	if c.clientEnv != "" {
		if v := os.Getenv(c.clientEnv); v != "" {
			id = v
		}
	}
	if c.secretEnv != "" {
		if v := os.Getenv(c.secretEnv); v != "" {
			secret = v
		}
	}
	return id, secret
}

// HandleAuthCodeAuthorize returns the authorization URL.
// GET /api/oauth/authcode/authorize?provider=gemini-cli|iflow&redirect_uri=...&state=...
func (h *OAuthHandler) HandleAuthCodeAuthorize(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	cfg := authcodeLookup(provider)
	if cfg == nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported provider (want gemini-cli|iflow)")
		return
	}
	clientID, _ := cfg.creds()
	redirectURI := callbackRedirectURI(r)
	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"state":         {state},
	}
	if len(cfg.scopes) > 0 {
		params.Set("scope", strings.Join(cfg.scopes, " "))
		if provider == "gemini-cli" {
			params.Set("access_type", "offline")
			params.Set("prompt", "consent")
		}
	}
	for k, v := range cfg.extraAuth {
		params.Set(k, v)
	}
	if provider == "iflow" {
		params.Set("redirect", redirectURI)
	}
	authURL := cfg.authorizeURL + "?" + params.Encode()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url": authURL, "authUrl": authURL, "state": state,
		"redirectUri": redirectURI, "flowType": "authorization_code", "provider": provider,
	})
}

// HandleAuthCodeExchange exchanges the code and stores the connection.
// POST /api/oauth/authcode/exchange
func (h *OAuthHandler) HandleAuthCodeExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider    string `json:"provider"`
		Code        string `json:"code"`
		RedirectURI string `json:"redirectUri"`
		RedirectUri string `json:"redirect_uri"`
		State       string `json:"state"`
		Name        string `json:"name"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	cfg := authcodeLookup(body.Provider)
	if cfg == nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unsupported provider (want gemini-cli|iflow)")
		return
	}
	if body.Code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
		return
	}
	redirectURI := exchangeRedirectURI(r, body.RedirectURI, body.RedirectUri)
	clientID, clientSecret := cfg.creds()

	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {body.Code},
		"client_id":    {clientID},
		"redirect_uri": {redirectURI},
	}
	if clientSecret != "" && !cfg.useBasic {
		form.Set("client_secret", clientSecret)
	}
	tokenReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, cfg.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "create token request failed")
		return
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.Header.Set("Accept", "application/json")
	if cfg.useBasic {
		tokenReq.SetBasicAuth(clientID, clientSecret)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange failed: %v", err))
		return
	}
	defer tokenResp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(tokenResp.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read token response")
		return
	}
	if tokenResp.StatusCode != http.StatusOK {
		log.Warn("oauth", "authcode exchange non-200", "provider", body.Provider, "status", tokenResp.StatusCode, "bytes", len(respBody))
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange returned status %d: %s", tokenResp.StatusCode, string(respBody)))
		return
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(respBody, &tokens); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to decode token response")
		return
	}
	if tokens.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "missing access_token in token response")
		return
	}

	credential, email, displayName := tokens.AccessToken, "", ""
	if cfg.userInfoURL != "" {
		infoURL := cfg.userInfoURL
		if body.Provider == "iflow" {
			// iFlow: the usable API key comes from userinfo, and it MUST exist.
			infoURL += "?accessToken=" + url.QueryEscape(tokens.AccessToken)
		} else {
			infoURL += "?access_token=" + url.QueryEscape(tokens.AccessToken)
		}
		if info, err := fetchOAuthUserInfo(r.Context(), infoURL, tokens.AccessToken); err == nil {
			if body.Provider == "iflow" {
				var ui struct {
					Success bool `json:"success"`
					Data    struct {
						APIKey   string `json:"apiKey"`
						Email    string `json:"email"`
						Phone    string `json:"phone"`
						Nickname string `json:"nickname"`
						Name     string `json:"name"`
					} `json:"data"`
				}
				if raw2, err := json.Marshal(info); err == nil {
					_ = json.Unmarshal(raw2, &ui)
				}
				if ui.Data.APIKey == "" {
					handlerutil.WriteJSONError(w, http.StatusBadGateway, "empty API key returned from iFlow")
					return
				}
				credential = ui.Data.APIKey
				email = ui.Data.Email
				if email == "" {
					email = ui.Data.Phone
				}
				if email == "" {
					handlerutil.WriteJSONError(w, http.StatusBadGateway, "missing account email/phone in user info")
					return
				}
				displayName = ui.Data.Nickname
				if displayName == "" {
					displayName = ui.Data.Name
				}
			} else if v, _ := info["email"].(string); v != "" {
				email = v
			}
		} else if body.Provider == "iflow" {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("failed to fetch user info: %v", err))
			return
		}
	}

	connName := connectionDisplayName(body.Provider, body.Name, email, displayName)
	if connName == displayName && displayName == "" {
		connName = cfg.display
	}
	connID := cfg.connPrefix + shortHash(credential)
	dataMap := map[string]any{"apiKey": credential, "accessToken": tokens.AccessToken}
	if tokens.RefreshToken != "" {
		dataMap["refreshToken"] = tokens.RefreshToken
	}
	if email != "" {
		dataMap["email"] = email
	}
	if tokens.IDToken != "" {
		dataMap["idToken"] = tokens.IDToken
	}
	if tokens.ExpiresIn > 0 {
		dataMap["expiresAt"] = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	dataBytes, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal connection data")
		return
	}
	if h.Repo != nil {
		existing, err := h.Repo.GetProviderConnectionByID(connID)
		if err == nil && existing != nil {
			if tokens.RefreshToken == "" {
				var old map[string]any
				if jerr := json.Unmarshal([]byte(existing.Data), &old); jerr == nil {
					if rt, ok := old["refreshToken"].(string); ok && rt != "" {
						dataMap["refreshToken"] = rt
					}
				}
			}
			dataBytes, _ = json.Marshal(dataMap)
			err = h.Repo.ReplaceProviderConnectionPayload(r.Context(), connID, connName, string(dataBytes))
		} else {
			priority := h.Repo.ResolveDefaultPriority(r.Context(), body.Provider)
			err = h.Repo.CreateProviderConnectionFull(r.Context(), connID, body.Provider, "oauth", connName, &priority, string(dataBytes))
		}
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
			return
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "authorized", "id": connID, "connectionId": connID,
		"provider": body.Provider, "name": connName, "email": email,
	})
}
