package dashboard

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
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/proxy/oauth"
)

// Port of upstream src/app/api/providers/[id]/test/testUtils.js: a real probe
// for one connection (api-key/compatible and OAuth), plus status write-back.
//
// Differences from upstream, kept intentionally:
//   - Token refresh reuses the Go refresher registry (internal/proxy/oauth) and
//     the client IDs in providers.KnownOAuthConfigs, so providers upstream
//     refreshes with configs we do not carry (kimi, kimi-coding, kiro) report
//     "Token expired"/"Token invalid or revoked" instead of silently refreshing.
//   - Upstream's proactive codex "maxRefreshAgeMs" stale-window refresh is not
//     ported; the expiresAt lead window below is used for every provider, at
//     the width that provider's registry entry asks for (providers.RefreshLead)
//     rather than the flat default upstream falls back to.
const (
	// connectionProbeTimeout mirrors upstream's AbortSignal.timeout(15000).
	connectionProbeTimeout = 15 * time.Second
	// connectionProxyProbeTimeout mirrors the proxy pool test timeout.
	connectionProxyProbeTimeout = 5 * time.Second

	codexCLIVersion               = providers.CodexCLIVersion
	grokCLIProbeURL               = "https://cli-chat-proxy.grok.com/v1/user"
	grokCLIProbeUA                = providers.GrokCLIPagerUserAgent
	kimchiProbeURL                = "https://api.cast.ai/v1/llm/openai/supported-providers"
	kilocodeProbeURL              = "https://api.kilo.ai/api/profile"
	clineProbeURL                 = "https://api.cline.bot/api/v1/users/me"
	googleUserinfoURL             = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"
	codexProbeURL                 = "https://chatgpt.com/backend-api/codex/responses"
	cloudCodeAssistProbeURL       = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	cloudCodeAssistProbeBody      = `{"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`
	// Meta's Model API lists its own catalogue for the credential that is
	// being tested, which is the same call the dashboard's live-model import
	// makes — a 401/403 is the only invalid answer. This is the Muse Code
	// subscription's connection probe as well as the pasted API key's: both
	// store the minted Model API key as the bearer token.
	museProbeURL    = "https://api.meta.ai/v1/models"
	museProbeAPIKey = "muse-spark-1.3"
	// v1m has no /models endpoint; the cheapest call that proves a key is a
	// single-token System One evaluation (upstream validateUrl defaults to
	// the provider's chat base URL).
	v1mProbeURL   = "https://v1m.ir/v1/systemone"
	v1mProbeModel = "rev-latest"
	// MiniMax Code publishes its model catalogue on the agent gateway (the
	// same endpoint the dashboard imports from), so a connection probe is a
	// plain bearer call there: a 401/403 is the only invalid answer.
	miniMaxCodeCatalogURL       = "https://agent.minimax.cn/mavis/api/v1/models?region=cn&buildEnv=prod"
	miniMaxCodeGlobalCatalogURL = "https://agent.minimax.io/mavis/api/v1/models?region=en&buildEnv=prod"
	// Upstream accepts any non-401/403 answer as proof the credentials work.
	connectionBrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

// AnthropicValidationModel is the model an Anthropic-compatible endpoint is
// probed with when the connection names none itself — no defaultModel and no
// providerSpecificData.assignedModel — for both the dashboard connection test
// and provider-node validation. Upstream hardcoded "claude-3-haiku-20240307",
// which Anthropic has since retired, so probing a valid API key against it
// now fails. The id is the one the model registry and pricing tables already
// carry ("claude-haiku-4-5-20251001"), so a probe never names a model the rest
// of the app does not know.
const AnthropicValidationModel = "claude-haiku-4-5-20251001"

// connectionProbeData is the connection blob the probe reads, mirroring the
// fields upstream's testUtils.js touches.
type connectionProbeData struct {
	APIKey                 string         `json:"apiKey"`
	AccessToken            string         `json:"accessToken"`
	RefreshToken           string         `json:"refreshToken"`
	ExpiresAt              string         `json:"expiresAt"`
	DefaultModel           string         `json:"defaultModel"`
	ProviderSpecificData   map[string]any `json:"providerSpecificData"`
	ConnectionProxyEnabled bool           `json:"connectionProxyEnabled"`
	ConnectionProxyURL     string         `json:"connectionProxyUrl"`
	ConnectionNoProxy      string         `json:"connectionNoProxy"`
}

// probeOutcome is one connection-test result. valid=false + message mirrors
// upstream's {valid, error}; tokens is set when a refresh happened.
type probeOutcome struct {
	valid     bool
	message   string
	refreshed bool
	tokens    *oauth.TokenResult
}

// connectionProbeDo performs the outbound request of a connection test.
// Package-level so tests can stub the network, mirroring validateProbeDo.
var connectionProbeDo = func(ctx context.Context, client *http.Client, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, connectionProbeTimeout)
	defer cancel()

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if isProxyFailure(err, resp) {
		if resp != nil {
			resp.Body.Close()
		}
		var directReader io.Reader
		if len(body) > 0 {
			directReader = bytes.NewReader(body)
		}
		if directReq, dErr := http.NewRequestWithContext(ctx, method, rawURL, directReader); dErr == nil {
			for k, v := range headers {
				directReq.Header.Set(k, v)
			}
			if directResp, dErr2 := directProbeClient.Do(directReq); dErr2 == nil {
				resp = directResp
				err = nil
			}
		}
	}
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, err
}

// oauthProbeConfig mirrors OAUTH_TEST_CONFIG in upstream testUtils.js.
type oauthProbeConfig struct {
	url             string
	buildURL        func(token string) string
	method          string
	authHeader      string
	authPrefix      string
	extraHeaders    map[string]string
	body            string
	acceptStatuses  []int
	softFailMessage map[int]string
	checkExpiry     bool
	refreshable     bool
	tokenExists     bool
	noAuth          bool
}

// oauthProbeConfigs is the per-provider OAuth probe matrix.
var oauthProbeConfigs = map[string]oauthProbeConfig{
	"claude": {checkExpiry: true, refreshable: true},
	"codex": {
		url:            codexProbeURL,
		method:         http.MethodPost,
		authHeader:     "Authorization",
		extraHeaders:   map[string]string{"Content-Type": "application/json", "originator": "codex_cli_rs", "User-Agent": providers.CodexCLIUserAgent, "version": providers.CodexCLIVersionHeader},
		body:           `{"model":"gpt-5.3-codex","input":[],"stream":false,"store":false}`,
		acceptStatuses: []int{http.StatusBadRequest},
		refreshable:    true,
	},
	"gemini-cli": {
		url: googleUserinfoURL, method: http.MethodGet,
		authHeader: "Authorization", authPrefix: "Bearer ", refreshable: true,
	},
	"antigravity": {
		url: googleUserinfoURL, method: http.MethodGet,
		authHeader: "Authorization", authPrefix: "Bearer ", refreshable: true,
	},
	"github": {
		url: "https://api.github.com/user", method: http.MethodGet,
		authHeader: "Authorization", authPrefix: "Bearer ",
		extraHeaders: map[string]string{"User-Agent": "9Router", "Accept": "application/vnd.github+json"},
	},
	"iflow": {
		buildURL: func(token string) string {
			return "https://iflow.cn/api/oauth/getUserInfo?accessToken=" + url.QueryEscape(token)
		},
		method: http.MethodGet, noAuth: true,
	},
	"kiro":           {checkExpiry: true, refreshable: true},
	"qoder":          {url: "https://openapi.qoder.sh/api/v1/userinfo", method: http.MethodGet, authHeader: "Authorization", authPrefix: "Bearer "},
	"qoder-cn":       {url: "https://openapi.qoder.com.cn/api/v1/userinfo", method: http.MethodGet, authHeader: "Authorization", authPrefix: "Bearer "},
	"kimi":           {checkExpiry: true, refreshable: true},
	"kimi-coding":    {checkExpiry: true, refreshable: true},
	// MiniMax Code publishes its own catalogue on the agent gateway, so the
	// probe proves the credential the same way the quota read does — and the
	// token is refreshable, unlike the API-key MiniMax providers.
	"minimax-code":        {url: miniMaxCodeCatalogURL, method: http.MethodGet, authHeader: "Authorization", authPrefix: "Bearer ", checkExpiry: true, refreshable: true},
	"minimax-code-global": {url: miniMaxCodeGlobalCatalogURL, method: http.MethodGet, authHeader: "Authorization", authPrefix: "Bearer ", checkExpiry: true, refreshable: true},
	// A Muse Code subscription stores the minted Model API key as its bearer
	// token, so the catalogue call proves the credential exactly the same way
	// it does for a pasted dev.meta.ai key — and there is nothing to refresh,
	// which is why refreshable stays off.
	"muse": {
		url: museProbeURL, method: http.MethodGet,
		authHeader: "Authorization", authPrefix: "Bearer ",
		extraHeaders: map[string]string{
			"Accept":        "application/json",
			"x-api-version": "1.0.0",
		},
	},
	"cursor":         {tokenExists: true},
	"kilocode":       {url: kilocodeProbeURL, method: http.MethodGet, authHeader: "Authorization", authPrefix: "Bearer "},
	"cline":          {refreshable: true},
	"clinepass":      {refreshable: true},
	"freebuff":       {},
	"gitlab":         {url: "https://gitlab.com/api/v4/user", method: http.MethodGet, authHeader: "Authorization", authPrefix: "Bearer "},
	"codebuddy-cn":   {tokenExists: true},
	"codebuddy-intl": {tokenExists: true},
	"zed":            {tokenExists: true},
	"windsurf":       {tokenExists: true},
	"trae":           {tokenExists: true},
	"devin":          {tokenExists: true},
	"devin-cli":      {tokenExists: true},
	"kimchi": {
		url: kimchiProbeURL, method: http.MethodGet,
		authHeader: "Authorization", authPrefix: "Bearer ",
		extraHeaders: map[string]string{"Accept": "application/json", "User-Agent": "kimchi/0.1.40"},
	},
	"grok-cli": {
		url: grokCLIProbeURL, method: http.MethodGet,
		authHeader: "Authorization", authPrefix: "Bearer ",
		extraHeaders: map[string]string{
			"Accept":                   "application/json",
			"User-Agent":               grokCLIProbeUA,
			"x-xai-token-auth":         "xai-grok-cli",
			"x-grok-client-identifier": providers.GrokCLIPagerIdentifier,
			"x-grok-client-version":    providers.GrokCLIVersion,
		},
		refreshable:    true,
		acceptStatuses: []int{http.StatusPaymentRequired},
		softFailMessage: map[int]string{
			http.StatusPaymentRequired: "Connected, but Grok Build credits are exhausted (spending limit). Add credits or upgrade SuperGrok.",
		},
	},
	"xai": {
		url: grokCLIProbeURL, method: http.MethodGet,
		authHeader: "Authorization", authPrefix: "Bearer ",
		extraHeaders: map[string]string{
			"Accept":                   "application/json",
			"User-Agent":               grokCLIProbeUA,
			"x-xai-token-auth":         "xai-grok-cli",
			"x-grok-client-identifier": providers.GrokCLIPagerIdentifier,
			"x-grok-client-version":    providers.GrokCLIVersion,
		},
		refreshable:    true,
		acceptStatuses: []int{http.StatusPaymentRequired},
		softFailMessage: map[int]string{
			http.StatusPaymentRequired: "Connected, but Grok Build credits are exhausted (spending limit). Add credits or upgrade SuperGrok.",
		},
	},
}

// HandleTestConnection handles POST /api/providers/{id}/test and
// POST /api/connections/{id}/test.
//
// It runs the provider probe for one connection, persists the outcome
// (testStatus/lastError/lastErrorAt, refreshed OAuth tokens) and answers
// {valid, error, refreshed} like upstream's route handler.
func (h *DashboardHandler) HandleTestConnection(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connection id")
		return
	}
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{
			"valid": false,
			"error": "Connection not found",
		})
		return
	}

	raw, _ := decodeConnectionData(conn.Data)
	if raw == nil {
		raw = map[string]any{}
	}
	data := parseConnectionProbeData(raw)

	out := h.testSingleConnection(r.Context(), conn, data, raw)
	h.persistProbeResult(conn, raw, out)

	payload := map[string]any{
		"valid":     out.valid,
		"error":     nil,
		"refreshed": out.refreshed,
	}
	if out.message != "" {
		payload["error"] = out.message
	}
	handlerutil.WriteJSON(w, http.StatusOK, payload)
}

// parseConnectionProbeData decodes the connection blob, keeping the nested
// providerSpecificData map intact while flattening legacy top-level fields.
func parseConnectionProbeData(raw map[string]any) connectionProbeData {
	var data connectionProbeData
	if encoded, err := json.Marshal(raw); err == nil {
		_ = json.Unmarshal(encoded, &data)
	}
	if data.ProviderSpecificData == nil {
		if psd, ok := raw["providerSpecificData"].(map[string]any); ok {
			data.ProviderSpecificData = psd
		}
	}
	return data
}

// testSingleConnection mirrors testSingleConnection(id): resolve the proxy,
// pre-check it, then run the api-key or OAuth probe.
func (h *DashboardHandler) testSingleConnection(ctx context.Context, conn *models.ProviderConnection, data connectionProbeData, raw map[string]any) probeOutcome {
	client, proxyURL := h.probeHTTPClient(data, raw)
	if proxyURL != "" {
		if err := probeProxyURL(ctx, proxyURL); err != nil {
			return probeOutcome{message: err.Error()}
		}
	}

	if conn.AuthType == "apikey" || conn.AuthType == "cookie" || conn.AuthType == "compatible" {
		return h.probeAPIKeyConnection(ctx, conn, data, raw, client)
	}
	return h.probeOAuthConnection(ctx, conn, data, client)
}

// probeHTTPClient resolves the connection's proxy the way the chat pipeline
// does (proxy pool first, then the legacy per-connection proxy fields) and
// returns the client a probe should dial with plus the proxy URL when it is a
// plain HTTP proxy worth pre-checking.
//
// A relay pool (vercel/cloudflare/deno) is not dialed as an HTTP proxy, so it
// gets a client that points at the relay host and carries the provider in
// x-relay-target/x-relay-path — the same contract the chat pipeline uses. It
// previously returned nil here, which made every "Test Connection" on a
// relayed connection run straight at the provider from the host's own IP.
func (h *DashboardHandler) probeHTTPClient(data connectionProbeData, raw map[string]any) (*http.Client, string) {
	poolID := psdStr(data.ProviderSpecificData, "proxyPoolId")
	if poolID == "" {
		poolID = psdStr(raw, "proxyPoolId")
	}

	var proxyURLStr string
	edgeRelay := false
	if poolID != "" {
		if pool, err := h.Repo.GetProxyPool(poolID); err == nil && pool != nil && pool.IsActive {
			proxyURLStr = pool.NextURL()
			edgeRelay = pool.IsEdgeRelay()
		}
	}
	if proxyURLStr == "" {
		enabled := data.ConnectionProxyEnabled
		target := data.ConnectionProxyURL
		if !enabled && data.ProviderSpecificData != nil {
			if en, ok := data.ProviderSpecificData["connectionProxyEnabled"].(bool); ok {
				enabled = en
			}
			if u, ok := data.ProviderSpecificData["connectionProxyUrl"].(string); ok {
				target = u
			}
		}
		if enabled && target != "" {
			proxyURLStr = target
		}
	}
	if proxyURLStr == "" {
		return nil, ""
	}
	if edgeRelay {
		return newProbeRelayClient(proxyURLStr, probeUpstreamURL(data, raw)), ""
	}

	parsed, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, ""
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		Timeout:   connectionProbeTimeout,
	}, proxyURLStr
}

// probeUpstreamURL is the provider endpoint a probe should be aimed at, from
// whichever field the connection carries. Empty means the provider's registry
// default is in play and the probe's own URL stands.
func probeUpstreamURL(data connectionProbeData, raw map[string]any) string {
	for _, key := range []string{"baseUrl", "baseURL"} {
		if v := psdStr(data.ProviderSpecificData, key); v != "" {
			return v
		}
		if v := psdStr(raw, key); v != "" {
			return v
		}
	}
	if data.ConnectionProxyURL != "" {
		return data.ConnectionProxyURL
	}
	return ""
}

// probeProxyURL pre-checks a proxy before probing through it (upstream runs
// testProxyUrl and marks the connection broken when the proxy itself is down).
func probeProxyURL(ctx context.Context, proxyURLStr string) error {
	parsed, err := url.Parse(proxyURLStr)
	if err != nil {
		return fmt.Errorf("invalid proxy URL format")
	}
	ctx, cancel := context.WithTimeout(ctx, connectionProxyProbeTimeout)
	defer cancel()

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		Timeout:   connectionProxyProbeTimeout,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.google.com/generate_204", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Proxy test failed: %s", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Proxy test failed with status %d", resp.StatusCode)
	}
	return nil
}

// probeAPIKeyConnection ports testApiKeyConnection: compatible nodes plus the
// provider-specific api-key probes. Providers in the registry are delegated to
// validateProviderKey (the shared probe matrix) with the connection's client.
func (h *DashboardHandler) probeAPIKeyConnection(ctx context.Context, conn *models.ProviderConnection, data connectionProbeData, raw map[string]any, client *http.Client) probeOutcome {
	if conn.AuthType == "compatible" || strings.HasPrefix(conn.Provider, "openai-compatible-") || strings.HasPrefix(conn.Provider, "anthropic-compatible-") {
		return h.probeCompatibleConnection(ctx, conn, data, raw, client)
	}
	if psdStr(data.ProviderSpecificData, "baseUrl") != "" || psdStr(raw, "baseUrl") != "" || psdStr(raw, "baseURL") != "" {
		return h.probeCompatibleConnection(ctx, conn, data, raw, client)
	}
	if h.Repo != nil {
		if node, _, err := h.Repo.GetProviderNodeByID(conn.Provider); err == nil && node != nil {
			return h.probeCompatibleConnection(ctx, conn, data, raw, client)
		}
	}

	provider := providers.ResolveAlias(conn.Provider)
	cfg, known := providers.KnownProviders[provider]
	if !known {
		if _, ok := oauthProbeConfigs[provider]; ok {
			return h.probeOAuthConnection(ctx, conn, data, client)
		}
		return probeOutcome{message: "Provider test not supported"}
	}

	ctx = withProbeClient(ctx, client)
	out := validateProviderKey(ctx, provider, cfg, data.APIKey, data.ProviderSpecificData)
	if !out.supported {
		return probeOutcome{message: "Provider test not supported"}
	}
	if out.valid {
		return probeOutcome{valid: true}
	}
	message := out.message
	if message == "" {
		message = "Invalid API key"
	}
	return probeOutcome{message: message}
}

// probeCompatibleConnection ports the compatible branches of
// testApiKeyConnection: GET <base>/models for OpenAI-compatible nodes and a
// minimal POST <base>/v1/messages for Anthropic-compatible nodes (400/529 still
// prove the key was accepted).
func (h *DashboardHandler) probeCompatibleConnection(ctx context.Context, conn *models.ProviderConnection, data connectionProbeData, raw map[string]any, client *http.Client) probeOutcome {
	base := psdStr(data.ProviderSpecificData, "baseUrl")
	if base == "" {
		base = psdStr(raw, "baseUrl")
	}
	if base == "" {
		if _, nodeData, err := h.Repo.GetProviderNodeByID(conn.Provider); err == nil && nodeData != nil {
			base = strings.TrimSpace(nodeData.BaseURL)
		}
	}
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" {
		return probeOutcome{message: "Missing base URL"}
	}

	if strings.HasPrefix(conn.Provider, "anthropic-compatible-") {
		base = strings.TrimSuffix(base, "/messages")
		model := data.DefaultModel
		if model == "" {
			model = psdStr(data.ProviderSpecificData, "assignedModel")
		}
		if model == "" {
			model = AnthropicValidationModel
		}
		body, _ := json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "test"}},
		})
		status, _, err := connectionProbeDo(ctx, client, http.MethodPost, anthropicProbeMessagesURL(base), map[string]string{
			"x-api-key":         data.APIKey,
			"anthropic-version": "2023-06-01",
			"content-type":      "application/json",
			"Authorization":     "Bearer " + data.APIKey,
		}, body)
		if err != nil {
			return probeOutcome{message: err.Error()}
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return probeOutcome{message: "Invalid API key or base URL"}
		}
		return probeOutcome{valid: true}
	}
	status, _, err := connectionProbeDo(ctx, client, http.MethodGet, base+"/models", map[string]string{
		"Authorization": "Bearer " + data.APIKey,
	}, nil)
	if err != nil {
		return probeOutcome{message: err.Error()}
	}
	if status < 200 || status >= 300 {
		return probeOutcome{message: "Invalid API key or base URL"}
	}
	return probeOutcome{valid: true}
}

// anthropicProbeMessagesURL derives the Messages endpoint from a compatible
// node's base URL. Upstream always appends "/v1/messages", which doubles the
// version segment for the "/v1" bases this dashboard stores (its own node modal
// default is https://api.anthropic.com/v1), so bases already ending in /v1 get
// just "/messages".
func anthropicProbeMessagesURL(base string) string {
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

// probeOAuthConnection ports testOAuthConnection, including the gemini-cli /
// antigravity cloud-code probe and cline's users/me probe.
func (h *DashboardHandler) probeOAuthConnection(ctx context.Context, conn *models.ProviderConnection, data connectionProbeData, client *http.Client) probeOutcome {
	provider := providers.ResolveAlias(conn.Provider)
	cfg, ok := oauthProbeConfigs[provider]
	if !ok {
		token := data.AccessToken
		if token == "" {
			token = data.APIKey
		}
		if token == "" {
			token = data.RefreshToken
		}
		if token != "" {
			return probeOutcome{valid: true}
		}
		return probeOutcome{message: "No access token"}
	}
	if data.AccessToken == "" && data.APIKey != "" {
		data.AccessToken = data.APIKey
	}
	if data.AccessToken == "" {
		return probeOutcome{message: "No access token"}
	}
	if cfg.tokenExists {
		return probeOutcome{valid: true}
	}
	accessToken := data.AccessToken
	refreshed := false
	var tokens *oauth.TokenResult

	tokenExpired := connectionTokenExpired(provider, data)
	if cfg.refreshable && tokenExpired && data.RefreshToken != "" {
		tokens = h.refreshConnectionToken(ctx, provider, data, client)
		if tokens == nil {
			return probeOutcome{message: "Token expired and refresh failed"}
		}
		accessToken = tokens.AccessToken
		refreshed = true
	}

	if cfg.checkExpiry {
		if refreshed {
			return probeOutcome{valid: true, refreshed: true, tokens: tokens}
		}
		if tokenExpired {
			return probeOutcome{message: "Token expired"}
		}
		return probeOutcome{valid: true}
	}

	switch provider {
	case "gemini-cli", "antigravity":
		attempt := probeCloudCodeAssist(ctx, client, provider, accessToken)
		if attempt.valid {
			return probeOutcome{valid: true, refreshed: refreshed, tokens: tokens}
		}
		if attempt.status == http.StatusUnauthorized && cfg.refreshable && !refreshed && data.RefreshToken != "" {
			retryTokens := h.refreshConnectionToken(ctx, provider, data, client)
			if retryTokens == nil || retryTokens.AccessToken == "" {
				return probeOutcome{message: "Token invalid or revoked"}
			}
			retry := probeCloudCodeAssist(ctx, client, provider, retryTokens.AccessToken)
			if retry.valid {
				return probeOutcome{valid: true, refreshed: true, tokens: retryTokens}
			}
			return probeOutcome{message: retry.message, refreshed: true, tokens: retryTokens}
		}
		return probeOutcome{message: attempt.message, refreshed: refreshed, tokens: tokens}
	case "cline", "clinepass":
		return h.probeCline(ctx, data, client, accessToken, refreshed, tokens)
	case "freebuff":
		return h.probeFreebuff(ctx, conn, data, client)
	}

	return h.probeOAuthEndpoint(ctx, provider, cfg, client, accessToken, refreshed, tokens, data)
}

// probeOAuthEndpoint runs the generic configured probe plus the 401 retry path.
func (h *DashboardHandler) probeOAuthEndpoint(ctx context.Context, provider string, cfg oauthProbeConfig, client *http.Client, accessToken string, refreshed bool, tokens *oauth.TokenResult, data connectionProbeData) probeOutcome {
	attempt := runOAuthProbe(ctx, cfg, client, accessToken)
	if attempt.valid {
		return probeOutcome{valid: true, message: attempt.message, refreshed: refreshed, tokens: tokens}
	}
	if attempt.status == http.StatusUnauthorized && cfg.refreshable && !refreshed && data.RefreshToken != "" {
		retryTokens := h.refreshConnectionToken(ctx, provider, data, client)
		if retryTokens != nil {
			retry := runOAuthProbe(ctx, cfg, client, retryTokens.AccessToken)
			if retry.valid {
				return probeOutcome{valid: true, message: retry.message, refreshed: true, tokens: retryTokens}
			}
		}
		return probeOutcome{message: "Token invalid or revoked"}
	}
	return probeOutcome{message: attempt.message, refreshed: refreshed, tokens: tokens}
}

// probeCline ports the cline branch: probe users/me, refresh on 401, retry.
func (h *DashboardHandler) probeCline(ctx context.Context, data connectionProbeData, client *http.Client, accessToken string, refreshed bool, tokens *oauth.TokenResult) probeOutcome {
	try := func(token string) probeOutcome {
		// JWT-only workos: prefix (parity with upstream
		// open-sse/shared/clineAuth.js): ClinePass API keys ride plain.
		authVal := strings.TrimSpace(token)
		if !strings.HasPrefix(authVal, "workos:") && isClineWorkOSJWT(authVal) {
			authVal = "workos:" + authVal
		}
		status, _, err := connectionProbeDo(ctx, client, http.MethodGet, clineProbeURL, map[string]string{
			"Authorization": "Bearer " + authVal,
			"Accept":        "application/json",
		}, nil)
		if err != nil {
			return probeOutcome{message: err.Error()}
		}
		switch {
		case status >= 200 && status < 300:
			return probeOutcome{valid: true}
		case status == http.StatusUnauthorized:
			return probeOutcome{message: "Token invalid or revoked", refreshed: refreshed, tokens: tokens}
		case status == http.StatusForbidden:
			return probeOutcome{message: "Access denied", refreshed: refreshed, tokens: tokens}
		default:
			return probeOutcome{message: fmt.Sprintf("API returned %d", status), refreshed: refreshed, tokens: tokens}
		}
	}

	initial := try(accessToken)
	if initial.valid || initial.message != "Token invalid or revoked" || data.RefreshToken == "" {
		return initial
	}
	retryTokens := h.refreshConnectionToken(ctx, "cline", data, client)
	if retryTokens == nil || retryTokens.AccessToken == "" {
		return probeOutcome{message: "Token invalid or revoked"}
	}
	out := try(retryTokens.AccessToken)
	out.refreshed = true
	out.tokens = retryTokens
	return out
}

// isClineWorkOSJWT reports whether token looks like a Cline OAuth WorkOS JWT
// (base64url "eyJ…" header + dot). Non-JWT ClinePass API keys ride plain
// Bearer (upstream parity: open-sse/shared/clineAuth.js getClineAccessToken).
func isClineWorkOSJWT(token string) bool {
	if strings.HasPrefix(token, "workos:") || !strings.HasPrefix(token, "eyJ") {
		return false
	}
	dot := strings.IndexByte(token, '.')
	if dot <= 3 {
		return false
	}
	for i := 0; i < dot; i++ {
		c := token[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (h *DashboardHandler) probeFreebuff(ctx context.Context, conn *models.ProviderConnection, data connectionProbeData, client *http.Client) probeOutcome {
	token := data.AccessToken
	if token == "" {
		token = data.APIKey
	}
	if token == "" {
		token = psdStr(data.ProviderSpecificData, "authToken")
	}
	if token == "" {
		return probeOutcome{message: "No access token"}
	}

	reqURL := "https://www.codebuff.com/api/v1/freebuff/session"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return probeOutcome{message: fmt.Sprintf("create probe request failed: %v", err)}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "codebuff-cli/0.0.138")
	req.Header.Set("Accept", "application/json")

	resp, err := executor.DoFreebuffHTTP(ctx, client, req)
	if err != nil {
		return probeOutcome{message: fmt.Sprintf("connection failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return probeOutcome{valid: true}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return probeOutcome{message: "Invalid token or expired"}
	}
	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var refusal struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(bytes.TrimSpace(body), &refusal)
		if strings.ToLower(strings.TrimSpace(refusal.Status)) == "banned" {
			return probeOutcome{message: "Account banned"}
		}
		return probeOutcome{valid: true, message: "Region flagged: country not allowed"}
	}
	return probeOutcome{valid: true}
}

// oauthProbeAttempt is one classified probe answer.
type oauthProbeAttempt struct {
	valid   bool
	status  int
	message string
}

// runOAuthProbe performs the configured request and classifies the answer the
// way upstream's classifyOAuthProbeResult does.
func runOAuthProbe(ctx context.Context, cfg oauthProbeConfig, client *http.Client, accessToken string) oauthProbeAttempt {
	target := cfg.url
	if cfg.buildURL != nil {
		target = cfg.buildURL(accessToken)
	}
	if target == "" {
		return oauthProbeAttempt{message: "Provider test not supported"}
	}
	if cfg.method == "" {
		cfg.method = http.MethodGet
	}

	headers := map[string]string{}
	for k, v := range cfg.extraHeaders {
		headers[k] = v
	}
	if !cfg.noAuth {
		header := cfg.authHeader
		if header == "" {
			header = "Authorization"
		}
		headers[header] = cfg.authPrefix + accessToken
	}

	var body []byte
	if cfg.body != "" {
		body = []byte(cfg.body)
	}
	status, _, err := connectionProbeDo(ctx, client, cfg.method, target, headers, body)
	if err != nil {
		return oauthProbeAttempt{message: err.Error()}
	}
	return classifyOAuthProbe(status, cfg)
}

// classifyOAuthProbe ports classifyOAuthProbeResult.
func classifyOAuthProbe(status int, cfg oauthProbeConfig) oauthProbeAttempt {
	ok := status >= 200 && status < 300
	accepted := ok
	for _, s := range cfg.acceptStatuses {
		if s == status {
			accepted = true
			break
		}
	}
	if !accepted {
		switch status {
		case http.StatusUnauthorized:
			return oauthProbeAttempt{status: status, message: "Token invalid or revoked"}
		case http.StatusForbidden:
			return oauthProbeAttempt{status: status, message: "Access denied"}
		default:
			return oauthProbeAttempt{status: status, message: fmt.Sprintf("API returned %d", status)}
		}
	}
	// Soft success only when the provider configured a message for this status
	// (e.g. Grok CLI 402 spending limit): valid, but with a warning.
	if !ok {
		if message, exists := cfg.softFailMessage[status]; exists {
			return oauthProbeAttempt{valid: true, status: status, message: message}
		}
	}
	return oauthProbeAttempt{valid: true, status: status}
}

// probeCloudCodeAssist ports probeCloudCodeAssistAccess.
func probeCloudCodeAssist(ctx context.Context, client *http.Client, provider, accessToken string) oauthProbeAttempt {
	userAgent := "google-api-nodejs-client/9.15.1 gemini-cli/0.34.0"
	if provider == "antigravity" {
		userAgent = "google-api-nodejs-client/9.15.1 vscode-antigravity/1.107.0"
	}
	status, body, err := connectionProbeDo(ctx, client, http.MethodPost, cloudCodeAssistProbeURL, map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Content-Type":  "application/json",
		"User-Agent":    userAgent,
	}, []byte(cloudCodeAssistProbeBody))
	if err != nil {
		return oauthProbeAttempt{message: err.Error()}
	}
	if status >= 200 && status < 300 {
		return oauthProbeAttempt{valid: true, status: status}
	}
	return oauthProbeAttempt{status: status, message: probeProviderErrorMessage(body, fmt.Sprintf("API returned %d", status))}
}

// probeProviderErrorMessage ports parseProviderErrorMessage.
func probeProviderErrorMessage(body []byte, fallback string) string {
	if len(body) == 0 {
		return fallback
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err == nil {
		if errObj, ok := parsed["error"].(map[string]any); ok {
			if message, ok := errObj["message"].(string); ok && strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
		}
		if message, ok := parsed["message"].(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
		if message, ok := parsed["error"].(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return trimmed
	}
	return fallback
}

// refreshConnectionToken refreshes a connection's OAuth token through the
// shared refresher registry. A nil result means "cannot refresh", which the
// callers surface exactly like upstream's failed refresh.
func (h *DashboardHandler) refreshConnectionToken(ctx context.Context, provider string, data connectionProbeData, client *http.Client) *oauth.TokenResult {
	if data.RefreshToken == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: connectionProbeTimeout}
	}
	result, err := oauth.Refresh(ctx, &oauth.Params{
		Client:               client,
		Provider:             providers.ResolveAlias(provider),
		RefreshToken:         data.RefreshToken,
		AccessToken:          data.AccessToken,
		ProviderSpecificData: oauth.StringMap(data.ProviderSpecificData),
	})
	if err != nil || result == nil || result.AccessToken == "" {
		return nil
	}
	return result
}

// connectionTokenExpired mirrors shouldRefreshCredentials' expiresAt window.
// An absent/unknown expiresAt means "not expired" (upstream returns false).
func connectionTokenExpired(provider string, data connectionProbeData) bool {
	if data.ExpiresAt == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, data.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(expiresAt.Add(-providers.RefreshLead(provider)))
}

// persistProbeResult writes the probe outcome back to the connection row:
// testStatus, lastError/lastErrorAt and any refreshed credentials.
func (h *DashboardHandler) persistProbeResult(conn *models.ProviderConnection, raw map[string]any, out probeOutcome) {
	if out.valid {
		raw["testStatus"] = "active"
		if out.message != "" {
			// Soft success (e.g. grok-cli 402): keep the connection active and
			// surface the message as a warning, like upstream.
			raw["lastError"] = out.message
			raw["lastErrorAt"] = time.Now().UTC().Format(time.RFC3339)
		} else {
			raw["lastError"] = nil
			raw["lastErrorAt"] = nil
		}
	} else if out.message == "Provider test not supported" {
		// Do not mark connection as broken in DB when probe is unsupported
		if curStatus, _ := raw["testStatus"].(string); curStatus == "" || curStatus == "error" {
			raw["testStatus"] = "active"
		}
		if curErr, _ := raw["lastError"].(string); curErr == "Provider test not supported" {
			raw["lastError"] = nil
			raw["lastErrorAt"] = nil
		}
	} else {
		raw["testStatus"] = "error"
		raw["lastError"] = out.message
		raw["lastErrorAt"] = time.Now().UTC().Format(time.RFC3339)
	}

	if out.refreshed && out.tokens != nil {
		if out.tokens.AccessToken != "" {
			raw["accessToken"] = out.tokens.AccessToken
		}
		if out.tokens.RefreshToken != "" {
			raw["refreshToken"] = out.tokens.RefreshToken
		}
		if out.tokens.ProjectID != "" {
			raw["projectId"] = out.tokens.ProjectID
		}
		if out.tokens.ExpiresIn > 0 {
			raw["expiresIn"] = out.tokens.ExpiresIn
			raw["expiresAt"] = time.Now().Add(time.Duration(out.tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		}
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return
	}
	_ = h.Repo.UpdateConnectionData(conn.ID, string(encoded))
}
