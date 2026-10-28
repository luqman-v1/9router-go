package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// Error fidelity borrowed from the JS intl executor's parseError: the gateway
// overloads its codes, so the wording — never the code alone — decides whether
// a failure is a rate limit, a channel-verification gate, or an account ban.

func TestClassifyCodebuddyFailure_ConversationIDMissing(t *testing.T) {
	body := []byte(`{"code":11128,"msg":"conversation id missing","displayTips":{"en":"This call failed channel verification"}}`)
	f, ok := classifyCodebuddyFailure(body, true)
	if !ok {
		t.Fatal("11128 conversation-id body not classified")
	}
	if f.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", f.status)
	}
	if !strings.Contains(f.message, "channel verification") {
		t.Errorf("message does not name the gate: %q", f.message)
	}
	if !strings.Contains(f.message, "X-Conversation") {
		t.Errorf("message does not name the required family: %q", f.message)
	}
	if !strings.Contains(f.message, "11128") {
		t.Errorf("message does not carry the upstream code: %q", f.message)
	}
}

func TestClassifyCodebuddyFailure_SystemPromptVariantIsNotTheGate(t *testing.T) {
	// 11128 also reports a body-shape problem this executor already fixes;
	// calling that "channel verification" would send the next reader hunting
	// the wrong cause.
	body := []byte(`{"code":11128,"msg":"first message is not system prompt"}`)
	if f, ok := classifyCodebuddyFailure(body, true); ok {
		t.Errorf("malformed-request 11128 classified as %+v, want pass-through", f)
	}
}

func TestClassifyCodebuddyFailure_UnapprovedChannelWording(t *testing.T) {
	// Recorded upstream shape: the error object arrives as a single-quoted
	// string inside `message`, so `code` never reaches the parsed struct.
	inner := `{'code': 11128, 'msg': 'Illegal API invocation from an unapproved channel', ` +
		`'displayTips': {'en': 'This call failed channel verification, possibly because it was ` +
		`not sent from an official client.'}, 'displayMsg': {'zh': 'blocked'}}`
	body := []byte(`{"message":` + strconv.Quote(inner) + `,"type":"error"}`)
	f, ok := classifyCodebuddyFailure(body, true)
	if !ok {
		t.Fatal("fingerprint rejection not classified")
	}
	if f.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", f.status)
	}
	if !strings.Contains(f.message, "client fingerprint") {
		t.Errorf("message does not name the fingerprint rejection: %q", f.message)
	}
	if strings.Contains(f.message, "X-Conversation") {
		t.Errorf("fingerprint rejection described as a missing header family: %q", f.message)
	}
}

func TestClassifyCodebuddyFailure_EscapedChannelCheck(t *testing.T) {
	inner := `{"code": 11128, "msg": "Illegal API invocation from an unapproved channel"}`
	double := strings.ReplaceAll(`{"message":"X"}`, "X", strings.ReplaceAll(inner, `"`, `\"`))
	for _, body := range []string{
		`{"message":` + strconv.Quote(inner) + `}`,
		double,
	} {
		if f, ok := classifyCodebuddyFailure([]byte(body), true); !ok || f.status != http.StatusBadRequest {
			t.Errorf("escaped gate not classified: %s -> %+v %v", body, f, ok)
		}
	}
}

func TestClassifyCodebuddyFailure_GateNeedsCodeAndWording(t *testing.T) {
	// The same traps the reference implementation pins: a refusal that merely
	// mentions the wording, a request id that looks like the code, and a code
	// that only shares a prefix must all pass through.
	body := `{"code":11128,"msg":"blocked by security policy"}`
	if f, ok := classifyCodebuddyFailure([]byte(body), true); ok {
		t.Errorf("unrelated 11128 classified as %+v, want pass-through", f)
	}
	for _, neg := range []string{
		`{"msg":"channel verification"}`,
		`{"requestId":"11128","msg":"official client"}`,
		`{"code":111280,"msg":"This call failed channel verification"}`,
	} {
		if f, ok := classifyCodebuddyFailure([]byte(neg), true); ok {
			t.Errorf("false positive on %s: %+v", neg, f)
		}
	}
	// A quoted value under a quoted key is still the code.
	quoted := `{"message":"{'code':'11-128','displayTips':{'en':'not sent from an official client'}}"}`
	if f, ok := classifyCodebuddyFailure([]byte(quoted), true); !ok || f.status != http.StatusBadRequest {
		t.Errorf("quoted gate code missed: %s -> %+v %v", quoted, f, ok)
	}
}

func TestClassifyCodebuddyFailure_RequestIllegalIsTheMissingFamily(t *testing.T) {
	// Recorded live: without the X-Conversation-* and trace headers the same
	// credential, host and body answer this; with them, 200.
	body := []byte(`{"code":11128,"msg":"request illegal","requestId":"b72177e2-1420-488b-9173-0f27edb56211",` +
		`"displayMsg":{"en":"Request blocked. Please send it again","zh":"blocked"},` +
		`"displayTips":{"en":"This call failed channel verification, possibly because it was not sent from an official client."}}`)
	f, ok := classifyCodebuddyFailure(body, true)
	if !ok || f.status != http.StatusBadRequest {
		t.Fatalf("classified %+v (ok=%v), want 400", f, ok)
	}
	if !strings.Contains(f.message, "X-Conversation") {
		t.Errorf("missing-family verdict does not name the family: %q", f.message)
	}
	if strings.Contains(f.message, "fingerprint") {
		t.Errorf("missing-family verdict blamed the fingerprint: %q", f.message)
	}
	if !strings.Contains(f.message, "200") {
		t.Errorf("verdict does not state the measured outcome: %q", f.message)
	}
	// The console path's own gate is a body-shape problem, not this one.
	if f, ok := classifyCodebuddyFailure([]byte(`{"code":11128,"msg":"first message is not system prompt"}`), true); ok {
		t.Errorf("system-prompt gate classified as %+v", f)
	}
}

func TestClassifyCodebuddyFailure_AccountRestricted(t *testing.T) {
	body := []byte(`{"code":11140,"msg":"request illegal","requestId":"01a14a5d3926755fbbb57391a5ccfad9","displayMsg":{"en":"Your account has been restricted due to unusual activity"}}`)
	f, ok := classifyCodebuddyFailure(body, true)
	if !ok {
		t.Fatal("11140 ban body not classified")
	}
	if f.status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", f.status)
	}
	for _, want := range []string{"account restricted upstream", "11140", "Your account has been restricted", "requestId=01a14a5d3926755fbbb57391a5ccfad9", "re-sign in or appeal"} {
		if !strings.Contains(f.message, want) {
			t.Errorf("message %q missing %q", f.message, want)
		}
	}
	if strings.HasPrefix(f.message, `{"code"`) {
		t.Errorf("raw gateway JSON used as the message: %q", f.message)
	}
	if f.retryAfter != 0 {
		t.Errorf("a ban must not claim a reset window, got %v", f.retryAfter)
	}
}

func TestClassifyCodebuddyFailure_RequestIllegalWithoutDisplay(t *testing.T) {
	f, ok := classifyCodebuddyFailure([]byte(`{"code":11140,"msg":"request illegal"}`), true)
	if !ok || f.status != http.StatusForbidden {
		t.Fatalf("classified %+v (ok=%v), want 403", f, ok)
	}
	if !strings.Contains(f.message, "11140") || !strings.Contains(f.message, "request illegal") {
		t.Errorf("message = %q, want code and upstream wording", f.message)
	}
}

func TestClassifyCodebuddyFailure_11140ThrottleStaysRetryable(t *testing.T) {
	body := []byte(`{"code":11140,"msg":"The model provider is rate-limiting requests. Resets at 2099-10-17 10:00:00 UTC+8"}`)
	f, ok := classifyCodebuddyFailure(body, true)
	if !ok {
		t.Fatal("11140 throttle not classified")
	}
	if f.status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", f.status)
	}
	if strings.Contains(f.message, "account restricted") {
		t.Errorf("throttling misreported as a ban: %q", f.message)
	}
	if f.retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want the announced reset", f.retryAfter)
	}
}

func TestClassifyCodebuddyFailure_Ambiguous11140DefaultsToRateLimit(t *testing.T) {
	f, ok := classifyCodebuddyFailure([]byte(`{"code":11140,"msg":"denied"}`), true)
	if !ok || f.status != http.StatusTooManyRequests {
		t.Fatalf("classified %+v (ok=%v), want 429", f, ok)
	}
	if strings.Contains(f.message, "account restricted") {
		t.Errorf("ambiguous 11140 treated as a ban: %q", f.message)
	}
	if f.retryAfter != 0 {
		t.Errorf("retryAfter = %v, want 0 when no reset was named", f.retryAfter)
	}
}

func TestClassifyCodebuddyFailure_FrequencyLimitCarriesReset(t *testing.T) {
	body := []byte(`{"code":6004,"msg":"超出频率限制 2099-10-16 10:00:00 UTC+8"}`)
	f, ok := classifyCodebuddyFailure(body, false)
	if !ok {
		t.Fatal("6004 not classified")
	}
	if f.status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", f.status)
	}
	if !strings.Contains(f.message, "rate limit") {
		t.Errorf("message %q does not match the shared rate-limit rule text", f.message)
	}
	want := time.Date(2099, 10, 16, 10, 0, 0, 0, time.FixedZone("", 8*3600))
	if diff := time.Until(want); f.retryAfter < diff-time.Minute || f.retryAfter > diff+time.Minute {
		t.Errorf("retryAfter = %v, want about %v", f.retryAfter, diff)
	}
}

func TestClassifyCodebuddyFailure_UnknownBodyPassesThrough(t *testing.T) {
	for _, body := range [][]byte{
		nil,
		[]byte(`not json`),
		[]byte(`{"choices":[]}`),
	} {
		if _, ok := classifyCodebuddyFailure(body, true); ok {
			t.Errorf("body %q classified as a failure", body)
		}
	}
	// The 11140 wording rules are intl-only: the CN lane must not reinterpret
	// its own codes through them.
	if _, ok := classifyCodebuddyFailure([]byte(`{"code":11140,"msg":"request illegal"}`), false); ok {
		t.Error("CN lane applied the intl 11140 rules")
	}
}

func TestApplyCodebuddyFailure_RewritesStatusMessageAndRetryAfter(t *testing.T) {
	ue := &proxy.UpstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"code":11140,"msg":"The model provider is rate-limiting requests. Resets at 2099-10-17 10:00:00 UTC+8"}`)}
	applyCodebuddyFailure(ue, true)
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d", ue.StatusCode)
	}
	if ue.Header.Get("Retry-After") == "" {
		t.Error("Retry-After not set from the announced reset")
	}
	if !strings.Contains(string(ue.Body), "rate limit") {
		t.Errorf("body message = %q", ue.Body)
	}

	ban := &proxy.UpstreamError{StatusCode: http.StatusForbidden, Body: []byte(`{"code":11140,"msg":"request illegal","displayMsg":{"en":"account has been restricted"}}`)}
	applyCodebuddyFailure(ban, true)
	if ban.StatusCode != http.StatusForbidden {
		t.Errorf("ban status = %d, want 403", ban.StatusCode)
	}
	if ban.Header.Get("Retry-After") != "" {
		t.Error("a ban must not advertise a Retry-After")
	}
	if !strings.Contains(string(ban.Body), "account restricted upstream") {
		t.Errorf("ban body = %q", ban.Body)
	}
	if !strings.Contains(string(ban.Body), "upstream") {
		t.Errorf("ban body dropped the raw upstream payload: %q", ban.Body)
	}
}

func TestCodebuddyRetryAfter_ZoneForms(t *testing.T) {
	base := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		msg   string
		zone  int
		want0 bool
	}{
		{"频率限制 2099-01-02 03:04:05 UTC+8", 8 * 3600, false},
		{"limit 2099-01-02 03:04:05 UTC+05:30", 5*3600 + 1800, false},
		{"limit 2099-01-02 03:04:05", 8 * 3600, false},
		{"limit without any timestamp", 0, true},
		{"limit 1999-01-02 03:04:05 UTC+8", 0, true},
	} {
		got := codebuddyRetryAfter(tc.msg)
		if tc.want0 {
			if got != 0 {
				t.Errorf("%q: retryAfter = %v, want 0", tc.msg, got)
			}
			continue
		}
		loc := time.FixedZone("", tc.zone)
		want := time.Until(time.Date(base.Year(), base.Month(), base.Day(), 3, 4, 5, 0, loc))
		if got < want-time.Minute || got > want+time.Minute {
			t.Errorf("%q: retryAfter = %v, want about %v", tc.msg, got, want)
		}
	}
}

// wireIntlError drives ForwardCodebuddyCN against a fake intl gateway and
// returns the UpstreamError the request path sees.
func wireIntlError(t *testing.T, status int, body string) *proxy.UpstreamError {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer ts.Close()

	target, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	req := &Request{
		Ctx:      context.Background(),
		Client:   &http.Client{Transport: codebuddyHostRewriteTransport{target: target}},
		Config:   &providers.ProviderConfig{BaseURL: "https://www.workbuddy.ai/v2/chat/completions"},
		APIKey:   "test-token",
		IsStream: true,
		Body:     []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"ping"}]}`),
	}
	err = ForwardCodebuddyCN(httptest.NewRecorder(), req)
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want *proxy.UpstreamError", err)
	}
	return ue
}

func TestForwardCodebuddyIntl_AccountBanReachesTheCaller(t *testing.T) {
	ue := wireIntlError(t, http.StatusForbidden, `{"code":11140,"msg":"request illegal","requestId":"rid-1","displayMsg":{"en":"Your account has been restricted due to unusual activity"}}`)
	if ue.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "account restricted upstream") {
		t.Errorf("body = %q, want the canonical ban wording the cooldown rule matches", ue.Body)
	}
	if !strings.Contains(string(ue.Body), "rid-1") {
		t.Errorf("body = %q, want the upstream requestId preserved", ue.Body)
	}
	if ue.Header.Get("Retry-After") != "" {
		t.Errorf("ban advertised Retry-After %q", ue.Header.Get("Retry-After"))
	}
}

func TestForwardCodebuddyIntl_RateLimitKeepsReset(t *testing.T) {
	ue := wireIntlError(t, http.StatusTooManyRequests, `{"code":6004,"msg":"超出频率限制 2099-10-16 10:00:00 UTC+8"}`)
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", ue.StatusCode)
	}
	if ue.Header.Get("Retry-After") == "" {
		t.Error("Retry-After missing on an announced rate limit")
	}
	if !strings.Contains(string(ue.Body), "rate limit") {
		t.Errorf("body = %q", ue.Body)
	}
}

// The pool classifies from the error *body*, but the log line comes from the
// returned error's text. A wrapper built before applyCodebuddyFailure rewrote
// the body keeps the raw gateway JSON in its snapshot, so a recognised 11140 ban
// was logged as an opaque blob even though the cooldown rule saw the canonical
// wording.
func TestForwardCodebuddyIntl_ErrorTextIsCanonical(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":11140,"msg":"request illegal","requestId":"rid-9","displayMsg":{"en":"Your account has been restricted due to unusual activity"}}`))
	}))
	defer ts.Close()

	target, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	req := &Request{
		Ctx:      context.Background(),
		Client:   &http.Client{Transport: codebuddyHostRewriteTransport{target: target}},
		Config:   &providers.ProviderConfig{BaseURL: "https://www.workbuddy.ai/v2/chat/completions"},
		APIKey:   "test-token",
		IsStream: true,
		Body:     []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"ping"}]}`),
	}
	fwdErr := ForwardCodebuddyCN(httptest.NewRecorder(), req)
	if fwdErr == nil {
		t.Fatal("expected an upstream error")
	}
	text := fwdErr.Error()
	if !strings.Contains(text, "account restricted upstream") {
		t.Errorf("log text lost the canonical ban wording: %s", text)
	}
	if strings.Contains(text, `upstream returned 403: {"code":11140`) {
		t.Errorf("log text still snapshots the raw gateway JSON: %s", text)
	}
	// The chain must still expose the status and the canonical body.
	var ue *proxy.UpstreamError
	if !errors.As(fwdErr, &ue) || ue.StatusCode != http.StatusForbidden {
		t.Errorf("rewritten error not reachable through errors.As: %v", fwdErr)
	}
}
