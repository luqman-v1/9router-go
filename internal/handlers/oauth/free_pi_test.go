package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/providers"
)

// fakeFreePiAuth is a stand-in for api.freepi.ai's auth surface. It records
// every path it was called with, which is how the consent gate is proven: a
// declined login must not touch the server at all, not even to fetch a device
// code.
type fakeFreePiAuth struct {
	srv *httptest.Server

	mu    sync.Mutex
	paths []string
	// tokenPolls is how many polls return "pending" before granting.
	tokenPolls  int
	pollCount   int
	token       string
	consentCode string // when set, POST /auth/token answers 403 with this code
	resetResult bool
	versionResp string
}

func newFakeFreePiAuth(t *testing.T) *fakeFreePiAuth {
	t.Helper()
	f := &fakeFreePiAuth{
		token:       "jwt-from-device-flow",
		resetResult: true,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/github/device":
			_, _ = w.Write([]byte(`{"session_id":"sess-1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","interval":5,"expires_in":900}`))
		case "/auth/consent":
			_, _ = w.Write([]byte(`{}`))
		case "/auth/token":
			f.mu.Lock()
			f.pollCount++
			polls, grants := f.tokenPolls, f.pollCount > f.tokenPolls
			code, token := f.consentCode, f.token
			f.mu.Unlock()

			if code != "" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"` + code + `"}`))
				return
			}
			if !grants {
				_ = polls
				_, _ = w.Write([]byte(`{"retry_after":7}`))
				return
			}
			_, _ = w.Write([]byte(`{"token":"` + token + `"}`))
		case "/session/reset":
			f.mu.Lock()
			released := f.resetResult
			f.mu.Unlock()
			if released {
				_, _ = w.Write([]byte(`{"released":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"released":false}`))
		case "/client-version":
			if f.versionResp != "" {
				_, _ = w.Write([]byte(f.versionResp))
				return
			}
			_, _ = w.Write([]byte(`{"min":"0.6.2","latest":"0.7","model":"deepseek/deepseek-v4-flash","models":[{"id":"deepseek/deepseek-v4-flash","name":"Flash"},{"id":"deepseek/deepseek-v4-pro","name":"Pro"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	t.Cleanup(f.srv.Close)

	// Point the handler at the fake for the duration of the test.
	prev := freePiAuthBaseURL
	freePiAuthBaseURL = f.srv.URL
	t.Cleanup(func() { freePiAuthBaseURL = prev })

	freePiResetPendingForTest()
	return f
}

func (f *fakeFreePiAuth) calledPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.paths))
	copy(out, f.paths)
	return out
}

func (f *fakeFreePiAuth) called(path string) bool {
	for _, p := range f.calledPaths() {
		if p == path {
			return true
		}
	}
	return false
}

func freePiPostJSON(t *testing.T, h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestFreePiInitiate_DeclinedConsentMakesNoServerCall is the privacy invariant.
// free-pi's consent covers training on prompts and code with no opt-out, so a
// decline must leave no trace upstream — not even a device-code request.
func TestFreePiInitiate_DeclinedConsentMakesNoServerCall(t *testing.T) {
	fake := newFakeFreePiAuth(t)
	h := &OAuthHandler{}

	tests := []struct {
		name string
		body string
	}{
		{name: "field absent", body: `{}`},
		{name: "explicitly false", body: `{"consentAccepted":false}`},
		{name: "malformed body", body: `not json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := freePiPostJSON(t, h.HandleFreePiInitiate, "/api/oauth/free-pi/initiate", tt.body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if paths := fake.calledPaths(); len(paths) != 0 {
				t.Errorf("declining still called upstream: %v", paths)
			}
		})
	}
}

// TestFreePiInitiate_StaleConsentVersionIsRejected: consent is version-specific.
// Accepting v3 is not accepting v4, so a stale assertion is refused rather than
// silently upgraded to whatever the server now records.
func TestFreePiInitiate_StaleConsentVersionIsRejected(t *testing.T) {
	fake := newFakeFreePiAuth(t)
	h := &OAuthHandler{}

	rec := freePiPostJSON(t, h.HandleFreePiInitiate, "/api/oauth/free-pi/initiate",
		`{"consentAccepted":true,"consentVersion":"3"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if paths := fake.calledPaths(); len(paths) != 0 {
		t.Errorf("a stale consent version still called upstream: %v", paths)
	}
}

func TestFreePiInitiate_AcceptedConsentStartsAndRecordsConsent(t *testing.T) {
	fake := newFakeFreePiAuth(t)
	h := &OAuthHandler{}

	rec := freePiPostJSON(t, h.HandleFreePiInitiate, "/api/oauth/free-pi/initiate",
		`{"consentAccepted":true,"consentVersion":"`+providers.FreePiConsentVersion+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		SessionID       string `json:"sessionId"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SessionID != "sess-1" || got.UserCode != "ABCD-1234" {
		t.Errorf("unexpected device payload: %+v", got)
	}
	if got.Interval != 5 {
		t.Errorf("interval = %d, want 5", got.Interval)
	}

	if !fake.called("/auth/github/device") {
		t.Error("device code was never requested")
	}
	if !fake.called("/auth/consent") {
		t.Error("consent was never recorded; the token poll would then 403")
	}
}

func TestFreePiPoll_PendingThenConnectedPersistsConnection(t *testing.T) {
	fake := newFakeFreePiAuth(t)
	fake.tokenPolls = 1 // first poll pending, second grants
	h := &OAuthHandler{}

	// A poll for an unknown session must be refused: the poll takes a
	// caller-supplied id, and forwarding arbitrary ids upstream would make this
	// a proxy for logins it never opened.
	unknown := freePiPostJSON(t, h.HandleFreePiPoll, "/api/oauth/free-pi/poll", `{"sessionId":"nope"}`)
	// 410, not a bare 400: the dashboard must be able to tell "this attempt is
	// dead, start again" from a transient failure worth retrying. A 400 was
	// indistinguishable from one, and the UI spun forever.
	if unknown.Code != http.StatusGone {
		t.Errorf("unknown session status = %d, want 410", unknown.Code)
	}
	if fake.called("/auth/token") {
		t.Error("an unknown session id was forwarded to upstream")
	}

	freePiPostJSON(t, h.HandleFreePiInitiate, "/api/oauth/free-pi/initiate",
		`{"consentAccepted":true,"consentVersion":"`+providers.FreePiConsentVersion+`"}`)

	pending := freePiPostJSON(t, h.HandleFreePiPoll, "/api/oauth/free-pi/poll", `{"sessionId":"sess-1"}`)
	var pendingBody struct {
		Status     string `json:"status"`
		RetryAfter int    `json:"retryAfter"`
	}
	_ = json.Unmarshal(pending.Body.Bytes(), &pendingBody)
	if pendingBody.Status != "pending" {
		t.Errorf("status = %q, want pending", pendingBody.Status)
	}
	if pendingBody.RetryAfter != 7 {
		t.Errorf("retryAfter = %d, want the server's 7", pendingBody.RetryAfter)
	}

	done := freePiPostJSON(t, h.HandleFreePiPoll, "/api/oauth/free-pi/poll", `{"sessionId":"sess-1"}`)
	var doneBody struct {
		Status       string `json:"status"`
		ConnectionID string `json:"connectionId"`
	}
	_ = json.Unmarshal(done.Body.Bytes(), &doneBody)
	if doneBody.Status != "connected" {
		t.Fatalf("status = %q, want connected: %s", doneBody.Status, done.Body.String())
	}
	if !strings.HasPrefix(doneBody.ConnectionID, "fp-") {
		t.Errorf("connection id = %q, want an fp- prefix", doneBody.ConnectionID)
	}

	// The catalogue must have been consulted: models[].id is the value a
	// request has to send, so dropping it is not optional.
	if !fake.called("/client-version") {
		t.Error("client-version was never fetched, so the model catalogue is unknown")
	}

	// A completed login is forgotten, so a replay cannot mint a second connection.
	if ph := freePiPendingKnown("sess-1"); ph {
		t.Error("the login session should be forgotten once it produced a connection")
	}
}

// TestFreePiPoll_ConsentRequiredIsTerminal: a 403 consent_required is a real
// state, not a transient one. Reporting it as "pending" would leave the dashboard
// polling a login that can never succeed.
func TestFreePiPoll_ConsentRequiredIsTerminal(t *testing.T) {
	fake := newFakeFreePiAuth(t)
	fake.consentCode = providers.FreePiCodeConsentRequired
	h := &OAuthHandler{}

	freePiPostJSON(t, h.HandleFreePiInitiate, "/api/oauth/free-pi/initiate",
		`{"consentAccepted":true,"consentVersion":"`+providers.FreePiConsentVersion+`"}`)

	rec := freePiPostJSON(t, h.HandleFreePiPoll, "/api/oauth/free-pi/poll", `{"sessionId":"sess-1"}`)

	var body struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Status != "consent_required" {
		t.Errorf("status = %q, want consent_required", body.Status)
	}
	if body.Message == "" {
		t.Error("no explanation for the user")
	}
	if freePiPendingKnown("sess-1") {
		t.Error("a consent-required session should be dropped, not left polling")
	}
}

func TestFreePiConsent_IsServedVerbatim(t *testing.T) {
	h := &OAuthHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/free-pi/consent", nil)
	rec := httptest.NewRecorder()
	h.HandleFreePiConsent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Version string `json:"version"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != providers.FreePiConsentVersion {
		t.Errorf("version = %q, want %q", body.Version, providers.FreePiConsentVersion)
	}
	// The disclosures that make this consent meaningful, not the wording.
	for _, want := range []string{"used to train models", "receives excerpts of your recent conversation", "no opt-out"} {
		if !strings.Contains(body.Text, want) {
			t.Errorf("consent text lost the %q disclosure", want)
		}
	}
}

// TestFreePiPendingTableIsBounded proves the pending-login map cannot grow
// without bound on a long-running box (AGENTS.md section 4.G).
func TestFreePiPendingTableIsBounded(t *testing.T) {
	freePiResetPendingForTest()

	for i := 0; i < freePiPendingEntries*3; i++ {
		freePiPendingStore(strings.Repeat("s", i%40+1)+string(rune('a'+i%26))+string(rune('0'+i%10)), 5, 300)
	}

	freePiPendingMu.Lock()
	size := len(freePiPending)
	freePiPendingMu.Unlock()

	if size > freePiPendingEntries {
		t.Errorf("pending table grew to %d entries, cap is %d", size, freePiPendingEntries)
	}
}

func TestFreePiSessionReset_RequiresConnection(t *testing.T) {
	newFakeFreePiAuth(t)
	h := &OAuthHandler{}

	rec := freePiPostJSON(t, h.HandleFreePiSessionReset, "/api/oauth/free-pi/session/reset", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a missing connectionId", rec.Code)
	}
}
