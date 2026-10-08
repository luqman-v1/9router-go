package oauth

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestCallbackRedirectURIFor(t *testing.T) {
	// Codex's OAuth client registered exactly one loopback redirect URI, so
	// the dashboard-derived value must never win for it — that is the bug that
	// made auth.openai.com answer invalid_authorize_request.
	tests := []struct {
		name     string
		cfg      *pkceConfig
		query    string
		want     string
		wantHost string
	}{
		{
			name:  "codex uses the registered loopback URI even when a dashboard one is offered",
			cfg:   pkceProviders["codex"],
			query: "?redirect_uri=" + url.QueryEscape("http://localhost:20130/callback"),
			want:  codexRedirectURI,
		},
		{
			name: "codex ignores the dashboard host entirely",
			cfg:  pkceProviders["codex"],
			want: codexRedirectURI,
		},
		{
			name:     "claude follows the dashboard",
			cfg:      pkceProviders["claude"],
			query:    "?redirect_uri=" + url.QueryEscape("http://localhost:20130/callback"),
			want:     "http://localhost:20130/callback",
			wantHost: "localhost:20130",
		},
		{
			name:     "gitlab derives the callback from the request host",
			cfg:      pkceProviders["gitlab"],
			want:     "http://localhost:20130/callback",
			wantHost: "localhost:20130",
		},
		{
			name:     "nil config falls back to the dashboard",
			cfg:      nil,
			want:     "http://localhost:20130/callback",
			wantHost: "localhost:20130",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/oauth/pkce/authorize"+tt.query, nil)
			req.Host = "localhost:20130"
			got := callbackRedirectURIFor(tt.cfg, req)
			if got != tt.want {
				t.Errorf("callbackRedirectURIFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExchangeRedirectURIFor_matchesAuthorizeLeg(t *testing.T) {
	// The exchange leg must send the same redirect_uri the authorize leg
	// advertised, or the token endpoint rejects the code.
	req := httptest.NewRequest("POST", "/api/oauth/pkce/exchange", nil)
	req.Host = "localhost:20130"

	codex := exchangeRedirectURIFor(pkceProviders["codex"], req, "http://localhost:20130/callback", "")
	if codex != codexRedirectURI {
		t.Errorf("codex exchange redirect = %q, want %q", codex, codexRedirectURI)
	}
	authorizeLeg := callbackRedirectURIFor(pkceProviders["codex"], req)
	if codex != authorizeLeg {
		t.Errorf("codex legs disagree: authorize %q, exchange %q", authorizeLeg, codex)
	}

	claude := exchangeRedirectURIFor(pkceProviders["claude"], req, "http://localhost:20131/callback", "")
	if claude != "http://localhost:20131/callback" {
		t.Errorf("claude exchange redirect = %q, want the client-supplied value", claude)
	}
}

// makeIDToken builds an unsigned JWT whose payload is claims. Only the payload
// is exercised by the decoder, so the header and signature are placeholders.
func makeIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	encode := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return encode([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + encode(raw) + ".sig"
}

func TestCodexAccountInfo(t *testing.T) {
	tests := []struct {
		name          string
		claims        map[string]any
		raw           string
		wantOK        bool
		wantEmail     string
		wantAccountID string
		wantPlanType  string
	}{
		{
			name: "reads the namespaced OpenAI auth block",
			claims: map[string]any{
				"email": "dev@example.com",
				codexAuthClaimKey: map[string]any{
					"chatgpt_account_id": "acct-primary",
					"chatgpt_plan_type":  "pro",
				},
			},
			wantOK:        true,
			wantEmail:     "dev@example.com",
			wantAccountID: "acct-primary",
			wantPlanType:  "pro",
		},
		{
			name: "falls back to root-level account claims",
			claims: map[string]any{
				"email":      "legacy@example.com",
				"account_id": "acct-legacy",
				"plan_type":  "plus",
			},
			wantOK:        true,
			wantEmail:     "legacy@example.com",
			wantAccountID: "acct-legacy",
			wantPlanType:  "plus",
		},
		{
			name: "namespaced block wins over root claims",
			claims: map[string]any{
				codexAuthClaimKey: map[string]any{"chatgpt_account_id": "acct-ns"},
				"account_id":      "acct-root",
			},
			wantOK:        true,
			wantAccountID: "acct-ns",
		},
		{
			name:   "non-string account id is ignored rather than crashing",
			claims: map[string]any{codexAuthClaimKey: map[string]any{"chatgpt_account_id": 42}},
			wantOK: true,
		},
		{name: "empty token", raw: "", wantOK: false},
		{name: "not a jwt", raw: "opaque-token", wantOK: false},
		{name: "payload is not base64", raw: "a.!!!!.c", wantOK: false},
		{name: "payload is not json", raw: "a." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".c", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := tt.raw
			if tt.claims != nil {
				token = makeIDToken(t, tt.claims)
			}
			got := codexAccountInfo(token)
			if !tt.wantOK {
				if got != (codexAccountClaims{}) {
					t.Fatalf("expected empty claims, got %+v", got)
				}
				return
			}
			if got.Email != tt.wantEmail {
				t.Errorf("Email = %q, want %q", got.Email, tt.wantEmail)
			}
			if got.AccountID != tt.wantAccountID {
				t.Errorf("AccountID = %q, want %q", got.AccountID, tt.wantAccountID)
			}
			if got.PlanType != tt.wantPlanType {
				t.Errorf("PlanType = %q, want %q", got.PlanType, tt.wantPlanType)
			}
		})
	}
}

func TestDecodeJWTClaims_acceptsPaddedPayload(t *testing.T) {
	// Some providers pad the payload segment; RawURLEncoding alone would
	// reject those tokens and silently drop the account id.
	payload := base64.URLEncoding.EncodeToString([]byte(`{"email":"padded@example.com"}`))
	got, ok := decodeJWTClaims("h." + payload + ".s")
	if !ok {
		t.Fatal("expected padded payload to decode")
	}
	if email := stringClaim(got, "email"); email != "padded@example.com" {
		t.Errorf("email = %q, want padded@example.com", email)
	}
}

func TestBuildConnectionData_storesCodexAccountClaims(t *testing.T) {
	ex := &pkceExchange{cfg: pkceProviders["codex"], provider: "codex"}
	tokens := &pkceTokens{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		IDToken: makeIDToken(t, map[string]any{
			"email": "dev@example.com",
			codexAuthClaimKey: map[string]any{
				"chatgpt_account_id": "acct-1",
				"chatgpt_plan_type":  "pro",
			},
		}),
	}
	data, email := ex.buildConnectionData(t.Context(), tokens)

	if email != "dev@example.com" {
		t.Errorf("email = %q, want dev@example.com", email)
	}
	if data["accessToken"] != "at-1" || data["refreshToken"] != "rt-1" {
		t.Errorf("tokens not stored: %+v", data)
	}
	if data["idToken"] == nil {
		t.Error("idToken must be kept so the executor can re-derive the account id")
	}
	// chatgpt-account-id is mandatory on every codex call, so an account that
	// was not persisted here is an account that cannot chat.
	psd, ok := data["providerSpecificData"].(map[string]any)
	if !ok {
		t.Fatalf("providerSpecificData missing, got %+v", data)
	}
	if psd["chatgptAccountId"] != "acct-1" {
		t.Errorf("chatgptAccountId = %v, want acct-1", psd["chatgptAccountId"])
	}
	if psd["chatgptPlanType"] != "pro" {
		t.Errorf("chatgptPlanType = %v, want pro", psd["chatgptPlanType"])
	}
}

func TestBuildConnectionData_omitsEmptyProviderSpecificData(t *testing.T) {
	// GitLab shares this path via emailFromIDTok=false; an empty claim set
	// must not write an empty providerSpecificData blob.
	ex := &pkceExchange{cfg: pkceProviders["codex"], provider: "codex"}
	data, _ := ex.buildConnectionData(t.Context(), &pkceTokens{
		AccessToken: "at-1",
		IDToken:     makeIDToken(t, map[string]any{"email": "bare@example.com"}),
	})
	if _, ok := data["providerSpecificData"]; ok {
		t.Errorf("providerSpecificData should be absent when no account claims exist: %+v", data)
	}
}

func TestCodexProxy_strayCallbackKeepsListenerForPendingLogin(t *testing.T) {
	// The port is fixed, so a dead listener is a dead login. A callback that
	// matches no session (a leftover popup, a real codex CLI on the same
	// machine) must be handed to the dashboard without retiring a listener
	// another login is still waiting on.
	p := &codexProxy{port: 0, sessions: map[string]*codexSession{}}
	defer p.stop()
	if err := p.start("20130", func(context.Context, string, *codexSession) (pkceExchangeResult, error) {
		return pkceExchangeResult{}, nil
	}); err != nil {
		t.Fatalf("start loopback: %v", err)
	}
	p.register("state-live", "verifier", codexRedirectURI, "")

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/callback?code=abc&state=state-stray", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("stray callback status = %d, want 302", rec.Code)
	}

	// The in-flight login's callback must still be servable.
	if sess, ok := p.lookup("state-live"); !ok || sess.status != "pending" {
		t.Errorf("pending session disturbed by a stray callback: %+v", sess)
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get("http://" + p.addr + "/auth/callback?code=real&state=state-live")
	if err != nil {
		t.Fatalf("pending login's callback no longer reaches the listener: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("pending login callback status = %d, want 200", resp.StatusCode)
	}
}

func TestCodexProxy_sessionLifecycle(t *testing.T) {
	p := &codexProxy{sessions: map[string]*codexSession{}}
	p.register("state-1", "verifier-1", codexRedirectURI, "work")

	sess, ok := p.lookup("state-1")
	if !ok {
		t.Fatal("session not registered")
	}
	if sess.codeVerifier != "verifier-1" || sess.redirectURI != codexRedirectURI || sess.status != "pending" {
		t.Fatalf("unexpected session: %+v", sess)
	}

	// A completed session must be visible to the poller.
	p.complete("state-1", "cx-abc", "dev@example.com")
	sess, _ = p.lookup("state-1")
	if sess.status != "done" || sess.connectionID != "cx-abc" || sess.email != "dev@example.com" {
		t.Errorf("completion not recorded: %+v", sess)
	}

	// A failed session must surface its reason, not just "error".
	p.register("state-2", "verifier-2", codexRedirectURI, "")
	p.fail("state-2", errors.New("token endpoint said no"))
	sess, _ = p.lookup("state-2")
	if sess.status != "error" || sess.errMsg != "token endpoint said no" {
		t.Errorf("failure not recorded: %+v", sess)
	}

	p.clear("state-1")
	if _, ok := p.lookup("state-1"); ok {
		t.Error("cleared session still readable")
	}
}

func TestCodexProxy_ignoresIncompleteRegistration(t *testing.T) {
	p := &codexProxy{sessions: map[string]*codexSession{}}
	p.register("", "verifier", codexRedirectURI, "") // no state
	p.register("state", "", codexRedirectURI, "")    // no verifier
	if len(p.sessions) != 0 {
		t.Errorf("incomplete registrations stored: %+v", p.sessions)
	}
}

func TestCodexProxy_unknownSessionRedirectsToDashboard(t *testing.T) {
	// A callback with no registered session means the login was started
	// outside this dashboard (e.g. a real codex CLI). Bouncing the code to the
	// dashboard's /callback keeps that flow recoverable instead of dropping it.
	p := &codexProxy{sessions: map[string]*codexSession{}, appPort: "20130"}
	req := httptest.NewRequest("GET", "/auth/callback?code=abc&state=other", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	want := "http://localhost:20130/callback?code=abc&state=other"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestCodexProxy_rejectsNonCallbackPaths(t *testing.T) {
	p := &codexProxy{sessions: map[string]*codexSession{}, appPort: "20130"}
	for _, path := range []string{"/", "/admin", "/auth/callbackX"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestCodexProxy_reportsProviderDenial(t *testing.T) {
	p := &codexProxy{sessions: map[string]*codexSession{}, appPort: "20130"}
	p.register("state-1", "v", codexRedirectURI, "")
	p.exchange = func(context.Context, string, *codexSession) (pkceExchangeResult, error) {
		t.Fatal("exchange must not run when the provider denied the request")
		return pkceExchangeResult{}, nil
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/callback?error=access_denied&error_description=User+said+no&state=state-1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a result page", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "User said no") {
		t.Errorf("result page should explain the denial: %s", rec.Body.String())
	}
	sess, _ := p.lookup("state-1")
	if sess.status != "error" {
		t.Errorf("session status = %q, want error", sess.status)
	}
}

func TestCodexProxy_escapesProviderErrorInResultPage(t *testing.T) {
	// The error text comes from the provider's query string; it must never be
	// written into the result page as markup.
	p := &codexProxy{sessions: map[string]*codexSession{}, appPort: "20130"}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/callback?error=x&error_description="+url.QueryEscape("<script>alert(1)</script>"), nil))

	if strings.Contains(rec.Body.String(), "<script>alert(1)</script>") {
		t.Errorf("provider error injected unescaped: %s", rec.Body.String())
	}
}

func TestHandleCodexStartProxy_validations(t *testing.T) {
	handler := NewOAuthHandler(nil)
	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{
			name:       "missing app_port",
			query:      "?state=s&code_verifier=v&redirect_uri=" + url.QueryEscape(codexRedirectURI),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing state",
			query:      "?app_port=20130&code_verifier=v&redirect_uri=" + url.QueryEscape(codexRedirectURI),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing code_verifier",
			query:      "?app_port=20130&state=s&redirect_uri=" + url.QueryEscape(codexRedirectURI),
			wantStatus: http.StatusBadRequest,
		},
		{
			// Honoring a caller-supplied redirect_uri would just move the
			// invalid_authorize_request failure from the authorize step to
			// the token step, so it is rejected up front.
			name:       "dashboard redirect_uri is refused",
			query:      "?app_port=20130&state=s&code_verifier=v&redirect_uri=" + url.QueryEscape("http://localhost:20130/callback"),
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.HandleCodexStartProxy(rec, httptest.NewRequest("GET", "/api/oauth/codex/start-proxy"+tt.query, nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestHandleCodexPollStatus_lifecycle(t *testing.T) {
	handler := NewOAuthHandler(nil)

	// Missing state is a client bug, not a pending login.
	rec := httptest.NewRecorder()
	handler.HandleCodexPollStatus(rec, httptest.NewRequest("GET", "/api/oauth/codex/poll-status", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	// An unknown state must read as unknown so a stale poll loop self-stops.
	rec = httptest.NewRecorder()
	handler.HandleCodexPollStatus(rec, httptest.NewRequest("GET", "/api/oauth/codex/poll-status?state=nope", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"unknown"`) {
		t.Errorf("unknown state: %d %s", rec.Code, rec.Body.String())
	}

	// A pending login reports pending without leaking the PKCE verifier.
	codexLoopback.register("poll-1", "secret-verifier", codexRedirectURI, "")
	rec = httptest.NewRecorder()
	handler.HandleCodexPollStatus(rec, httptest.NewRequest("GET", "/api/oauth/codex/poll-status?state=poll-1", nil))
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Errorf("pending state: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-verifier") {
		t.Errorf("poll response leaked the code verifier: %s", rec.Body.String())
	}

	// A finished login is reported once and then forgotten, so a reloaded tab
	// cannot replay a stale success.
	codexLoopback.complete("poll-1", "cx-1", "dev@example.com")
	rec = httptest.NewRecorder()
	handler.HandleCodexPollStatus(rec, httptest.NewRequest("GET", "/api/oauth/codex/poll-status?state=poll-1", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"done"`) || !strings.Contains(body, `"connectionId":"cx-1"`) {
		t.Errorf("completed state: %s", body)
	}
	rec = httptest.NewRecorder()
	handler.HandleCodexPollStatus(rec, httptest.NewRequest("GET", "/api/oauth/codex/poll-status?state=poll-1", nil))
	if !strings.Contains(rec.Body.String(), `"status":"unknown"`) {
		t.Errorf("settled session should be cleared after read: %s", rec.Body.String())
	}
}

func TestHandleCodexStopProxy_isIdempotent(t *testing.T) {
	handler := NewOAuthHandler(nil)
	for i := range 2 {
		rec := httptest.NewRecorder()
		handler.HandleCodexStopProxy(rec, httptest.NewRequest("GET", "/api/oauth/codex/stop-proxy", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
			t.Errorf("call %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
}

// TestCodexLoopback_completesLoginEndToEnd drives the whole fixed-port path the
// browser actually takes: register a pending login, hit the real loopback
// listener with an authorization code, and confirm the server swapped it for
// tokens, stored a usable connection, and told the dashboard about it.
func TestCodexLoopback_completesLoginEndToEnd(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	idToken := makeIDToken(t, map[string]any{
		"email": "dev@example.com",
		codexAuthClaimKey: map[string]any{
			"chatgpt_account_id": "acct-e2e",
			"chatgpt_plan_type":  "pro",
		},
	})
	var gotForm url.Values
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token request form: %v", err)
		}
		gotForm = r.PostForm
		raw, _ := json.Marshal(map[string]any{
			"access_token":  "at-e2e",
			"refresh_token": "rt-e2e",
			"expires_in":    3600,
			"id_token":      idToken,
		})
		w.Write(raw)
	}))
	defer tokenSrv.Close()

	original := pkceProviders["codex"]
	pkceProviders["codex"] = &pkceConfig{
		providers:        []string{"codex"},
		clientID:         original.clientID,
		authorizeURL:     original.authorizeURL,
		tokenURL:         tokenSrv.URL,
		scope:            original.scope,
		extraAuth:        original.extraAuth,
		connPrefix:       original.connPrefix,
		display:          original.display,
		emailFromIDTok:   true,
		fixedRedirectURI: original.fixedRedirectURI,
		fixedPort:        original.fixedPort,
	}
	defer func() { pkceProviders["codex"] = original }()

	handler := NewOAuthHandler(db.NewRepo(database))
	// An ephemeral port keeps the test off the real 1455 while still
	// exercising the actual listener rather than a stubbed handler.
	proxy := &codexProxy{port: 0, sessions: map[string]*codexSession{}}
	defer proxy.stop()
	if err := proxy.start("20130", func(ctx context.Context, code string, sess *codexSession) (pkceExchangeResult, error) {
		ex := &pkceExchange{
			cfg: pkceProviders["codex"], provider: "codex", code: code,
			codeVerifier: sess.codeVerifier, redirectURI: sess.redirectURI,
			clientID: pkceProviders["codex"].clientID, tokenURL: pkceProviders["codex"].tokenURL,
		}
		res, fail := handler.completePKCEExchange(ctx, ex)
		if fail != nil {
			return pkceExchangeResult{}, errors.New(fail.message)
		}
		return res, nil
	}); err != nil {
		t.Fatalf("start loopback: %v", err)
	}
	proxy.register("state-e2e", "verifier-e2e", codexRedirectURI, "")

	base := "http://" + proxy.addr
	resp, err := http.Get(base + "/auth/callback?code=code-e2e&state=state-e2e")
	if err != nil {
		t.Fatalf("loopback request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "connected") && !strings.Contains(string(body), "Connected") {
		t.Errorf("result page should report success, got: %s", body)
	}
	// The exchange must send the same redirect_uri the authorize leg advertised.
	if got := gotForm.Get("redirect_uri"); got != codexRedirectURI {
		t.Errorf("token redirect_uri = %q, want %q", got, codexRedirectURI)
	}
	if got := gotForm.Get("code_verifier"); got != "verifier-e2e" {
		t.Errorf("token code_verifier = %q, want verifier-e2e", got)
	}

	var data string
	if err := database.QueryRow("SELECT data FROM providerConnections WHERE provider = 'codex'").Scan(&data); err != nil {
		t.Fatalf("connection not persisted: %v", err)
	}
	if !strings.Contains(data, "chatgptAccountId") || !strings.Contains(data, "acct-e2e") {
		t.Errorf("stored connection is missing the ChatGPT account id: %s", data)
	}

	// The dashboard's poll loop must see the completion exactly once.
	sess, ok := proxy.lookup("state-e2e")
	if !ok || sess.status != "done" {
		t.Fatalf("session not marked done: %+v", sess)
	}
	if sess.connectionID == "" || sess.email != "dev@example.com" {
		t.Errorf("session result incomplete: %+v", sess)
	}
}

func TestIsAddrInUse(t *testing.T) {
	if isAddrInUse(errors.New("some other failure")) {
		t.Error("unrelated errors must not be reported as a busy port")
	}
}
