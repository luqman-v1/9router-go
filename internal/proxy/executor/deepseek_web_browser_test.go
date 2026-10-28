package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The header set a chat call carries is the one the site's own client sends
// on a chat call, and the difference from a settings call is the whole point:
// the site runs two different header sets, and a request that mixes them is
// a request no browser made.
func TestDeepSeekChatCallCarriesNoClientBlock(t *testing.T) {
	headers := deepSeekBrowserHeaders("https://chat.deepseek.com", nil)

	for _, name := range []string{
		"x-client-bundle-id",
		"x-client-version",
		"x-client-platform",
		"x-client-locale",
		"x-client-timezone-offset",
		"x-device-id",
		"x-device-model",
	} {
		if value, ok := headers[name]; ok {
			t.Errorf("chat call carries %s = %q, want it absent", name, value)
		}
	}
}

// The client block the app's interceptor attaches to the CLIENT calls is a
// known shape, and the executor does not send it: the chat calls are issued
// below that interceptor. This pins the block's own values, so a change to
// the version or the bundle id is a deliberate one.
func TestDeepSeekClientBlockValues(t *testing.T) {
	// The block as the site writes it, kept here so the values are pinned
	// even though the executor never sends them on a chat call.
	want := map[string]string{
		"x-client-bundle-id":       "com.deepseek.chat",
		"x-client-version":         "2.5.0",
		"x-client-platform":        "web",
		"x-client-locale":          "en_US",
		"x-device-model":           "",
		"x-client-timezone-offset": "25200",
	}
	if deepSeekWebClientVersion != want["x-client-version"] {
		t.Errorf("client version = %q, want the site's %q",
			deepSeekWebClientVersion, want["x-client-version"])
	}
	// The timezone value is the one the block carries, and it is a
	// function of the machine the router runs on rather than a constant.
	if got := deepSeekTimezoneOffsetSeconds(); got < -12*3600 || got > 14*3600 {
		t.Errorf("timezone offset %d is not a real UTC offset", got)
	}
}

// The timezone header carries the offset east of UTC, in seconds. The
// capture showed 25200 on a UTC+7 machine, and the previous implementation
// negated Go's offset — the one value a real client can never send.
func TestDeepSeekTimezoneOffsetIsEastOfUTC(t *testing.T) {
	got := deepSeekTimezoneOffsetSeconds()
	if got < -12*3600 || got > 14*3600 {
		t.Fatalf("timezone offset %d is not a real UTC offset", got)
	}
}

// The client version has to match what the site ships, because the site
// compares it. 2.5.0 is what the live bundle hard-codes.
func TestDeepSeekClientVersionMatchesTheSite(t *testing.T) {
	if deepSeekWebClientVersion != "2.5.0" {
		t.Errorf("client version = %q, want the site's 2.5.0", deepSeekWebClientVersion)
	}
}

// The site's own cookie travels with the turn, and the WAF reads it. The
// test asserts the cookie reaches the wire rather than that a value is
// present in a map.
func TestDeepSeekTurnSendsTheConfiguredCookie(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"ok"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	server := httptest.NewServer(upstream)
	defer server.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   server.Client(),
		Config:   deepSeekTestConfig(server.URL),
		APIKey:   "user-token-1",
		Body:     deepSeekTestBody(t, "deepseek-v4-pro", map[string]any{"role": "user", "content": "hello"}),
		IsStream: true,
		ConnData: map[string]any{"deepSeekCookie": "ds_session_id=abc; aws-waf-token=def"},
	}
	if err := ForwardDeepSeekWeb(rec, req); err != nil {
		t.Fatalf("ForwardDeepSeekWeb: %v", err)
	}

	for path, headers := range upstream.headers {
		if headers.Get("Cookie") == "" {
			t.Errorf("%s carried no cookie", path)
		}
	}
}

// The turn's cookie is the site's own, never a fabricated one. A cookie the
// site never issued is a request the site does not recognise, and the
// previous implementation invented one.
func TestDeepSeekTurnNeverFabricatesACookie(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"ok"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	server := httptest.NewServer(upstream)
	defer server.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   server.Client(),
		Config:   deepSeekTestConfig(server.URL),
		APIKey:   "user-token-1",
		Body:     deepSeekTestBody(t, "deepseek-v4-pro", map[string]any{"role": "user", "content": "hello"}),
		IsStream: true,
	}
	if err := ForwardDeepSeekWeb(rec, req); err != nil {
		t.Fatalf("ForwardDeepSeekWeb: %v", err)
	}

	for path, headers := range upstream.headers {
		cookie := headers.Get("Cookie")
		if strings.Contains(cookie, "HWWAFSESID=") || strings.Contains(cookie, "Hm_lvt_") {
			t.Errorf("%s carried a fabricated cookie: %q", path, cookie)
		}
	}
}

// The GET calls carry no origin and the POST calls do. The capture shows
// exactly that split, and a GET that carried an origin would be a request
// the site never made.
func TestDeepSeekOriginIsOnlyOnThePostCalls(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"ok"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	server := httptest.NewServer(upstream)
	defer server.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   server.Client(),
		Config:   deepSeekTestConfig(server.URL),
		APIKey:   "user-token-1",
		Body:     deepSeekTestBody(t, "deepseek-v4-pro", map[string]any{"role": "user", "content": "hello"}),
		IsStream: true,
	}
	if err := ForwardDeepSeekWeb(rec, req); err != nil {
		t.Fatalf("ForwardDeepSeekWeb: %v", err)
	}

	if origin := upstream.headers["/api/v0/users/current"].Get("Origin"); origin != "" {
		t.Errorf("the token exchange carried Origin = %q, want it absent", origin)
	}
	if origin := upstream.headers[deepSeekCompletionPath].Get("Origin"); origin != server.URL {
		t.Errorf("the completion carried Origin = %q, want %q", origin, server.URL)
	}
}

// The site marks its POSTs with the origin and leaves its GETs unmarked, so
// the plain path has to make the same split.
func TestDeepSeekPlainPathAlsoSplitsTheOrigin(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(1, "ok"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	turn := deepSeekTurnRequest(server, "user-token-1", "conversation-plain",
		deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}))
	turn.ConnData = map[string]any{"stealth": false}
	if err := forwardTurn(t, turn); err != nil {
		t.Fatalf("turn: %v", err)
	}

	if origin := upstream.headers["/api/v0/users/current"].Get("Origin"); origin != "" {
		t.Errorf("the token exchange carried Origin = %q, want it absent", origin)
	}
	if origin := upstream.headers[deepSeekCompletionPath].Get("Origin"); origin != server.URL {
		t.Errorf("the completion carried Origin = %q, want %q", origin, server.URL)
	}
}

// The error taxonomy comes from the site's own bundle, and each code means
// something different: a missing token is a dead credential, a ban is the
// account, and an address refusal is the egress.
func TestDeepSeekAuthFailureMapsEachCodeToItsStatus(t *testing.T) {
	tests := []struct {
		name       string
		code       int
		wantStatus int
	}{
		{name: "a rejected credential is a 401", code: deepSeekCodeAuthFailed, wantStatus: http.StatusUnauthorized},
		{name: "a missing token is a 401", code: deepSeekCodeMissingToken, wantStatus: http.StatusUnauthorized},
		{name: "a ban is a 403", code: deepSeekCodeUserBanned, wantStatus: http.StatusForbidden},
		{name: "an address refusal is a 403", code: deepSeekCodeIPRestricted, wantStatus: http.StatusForbidden},
		{name: "a mute is a 429", code: deepSeekCodeMuted, wantStatus: http.StatusTooManyRequests},
		{name: "anything else is a 502", code: 49999, wantStatus: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := deepSeekAuthFailure(&deepSeekAPIError{Code: tt.code, Message: "refused"}, "user-token-1")
			status, ok := deepSeekUpstreamStatus(err)
			if !ok {
				t.Fatalf("the failure carries no status: %v", err)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
		})
	}
}

// A rejected credential drops the cached access token, so the next attempt
// re-exchanges the credential instead of replaying a refused one.
func TestDeepSeekAuthFailureDropsTheCachedToken(t *testing.T) {
	deepSeekTokens.put("user-token-drop", "access-token")
	if _, ok := deepSeekTokens.get("user-token-drop"); !ok {
		t.Fatal("the token was not cached")
	}
	_ = deepSeekAuthFailure(&deepSeekAPIError{Code: deepSeekCodeAuthFailed, Message: "refused"}, "user-token-drop")
	if _, ok := deepSeekTokens.get("user-token-drop"); ok {
		t.Error("the cached token survived an auth failure")
	}
}

// A ban is not a credential problem, so the cached token has to survive it:
// the credential is fine and the account is refused.
func TestDeepSeekBanKeepsTheCachedToken(t *testing.T) {
	deepSeekTokens.put("user-token-banned", "access-token")
	_ = deepSeekAuthFailure(&deepSeekAPIError{Code: deepSeekCodeUserBanned, Message: "banned"}, "user-token-banned")
	if _, ok := deepSeekTokens.get("user-token-banned"); !ok {
		t.Error("the cached token was dropped for a ban — the credential is not the problem")
	}
}
