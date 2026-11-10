package executor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// --- P3: session identity -------------------------------------------------

// TestFreePiSessionID_IsStablePerAccountNotPerConnection is the core of the
// single-session design: two dashboard connections for one account, and any
// number of processes, must all compute the SAME id. Minting one per connection
// would occupy a second slot of the account's four for no benefit.
func TestFreePiSessionID_IsStablePerAccountNotPerConnection(t *testing.T) {
	token := freePiTestToken("user_shared")
	other := freePiTestToken("user_other")

	first := freePiSessionIDFor(token, nil)
	second := freePiSessionIDFor(token, nil)

	if first != second {
		t.Errorf("same account produced two session ids: %q vs %q", first, second)
	}
	if first == freePiSessionIDFor(other, nil) {
		t.Error("different accounts collided on one session id")
	}

	// An explicitly stored id (written at login) wins, so an operator can pin it.
	stored := freePiSessionIDFor(token, map[string]any{"freePiSessionId": "pinned-id"})
	if stored != "pinned-id" {
		t.Errorf("stored session id not honoured: %q", stored)
	}
}

func TestFreePiSessionID_FallsBackOffJWTPayload(t *testing.T) {
	// An opaque or malformed credential still needs a stable key; it must not
	// panic or produce an empty header.
	a := freePiSessionIDFor("not-a-jwt", nil)
	b := freePiSessionIDFor("not-a-jwt", nil)
	if a == "" || a != b {
		t.Errorf("opaque token is not stably keyed: %q vs %q", a, b)
	}
	if a == freePiSessionIDFor("different-opaque", nil) {
		t.Error("distinct opaque tokens collided")
	}
}

func TestFreePiAccountKey_PrefersJWTSubject(t *testing.T) {
	if got := freePiAccountKey(freePiTestToken("user_xyz")); got != "sub:user_xyz" {
		t.Errorf("account key = %q, want sub:user_xyz", got)
	}
	if got := freePiAccountKey("opaque"); !strings.HasPrefix(got, "sha256:") {
		t.Errorf("fallback account key = %q, want a sha256 prefix (never the raw secret)", got)
	}
}

// --- P3: 409 recovery -----------------------------------------------------

// freePiRecoveryEnv wires a fake upstream that answers 409 on the first chat
// call, the given status on /session/reset, and 200 afterwards.
type freePiRecoveryEnv struct {
	srv         *httptest.Server
	resetStatus int
	resetBody   string

	mu          sync.Mutex
	chatCalls   int
	resetCalls  int
	lastPathHit []string
}

func newFreePiRecoveryEnv(t *testing.T, resetStatus int, resetBody string) *freePiRecoveryEnv {
	t.Helper()
	env := &freePiRecoveryEnv{resetStatus: resetStatus, resetBody: resetBody}
	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.Lock()
		env.lastPathHit = append(env.lastPathHit, r.URL.Path)
		env.mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/session/reset") {
			env.mu.Lock()
			env.resetCalls++
			env.mu.Unlock()
			w.WriteHeader(env.resetStatus)
			_, _ = io.WriteString(w, env.resetBody)
			return
		}

		env.mu.Lock()
		env.chatCalls++
		n := env.chatCalls
		env.mu.Unlock()

		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"concurrent_session","message":"You've already got 4 free-pi CLI sessions running."}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	t.Cleanup(env.srv.Close)
	return env
}

func (e *freePiRecoveryEnv) counts() (chat, reset int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chatCalls, e.resetCalls
}

func TestFreePiForward_ResetOnceThenRetry(t *testing.T) {
	resetFreePiCooldownsForTest()
	env := newFreePiRecoveryEnv(t, http.StatusOK, `{"released":true}`)

	store := &fakeLeaseStore{}
	req := freePiTestRequest(t, env.srv, freePiTestToken("user_recover"), nil, false)
	req.Leases = store
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}

	chat, reset := env.counts()
	if reset != 1 {
		t.Errorf("reset calls = %d, want exactly 1", reset)
	}
	if chat != 2 {
		t.Errorf("chat calls = %d, want 2 (original + one retry)", chat)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after recovery", rec.Code)
	}
}

// TestFreePiForward_ResetIsStillOneRetryWhenNothingWasReleased covers the
// `{released:false}` body: the server had nothing to drop, which is success.
func TestFreePiForward_ResetIsStillOneRetryWhenNothingWasReleased(t *testing.T) {
	resetFreePiCooldownsForTest()
	env := newFreePiRecoveryEnv(t, http.StatusOK, `{"released":false}`)

	req := freePiTestRequest(t, env.srv, freePiTestToken("user_nothing"), nil, false)
	req.Leases = &fakeLeaseStore{}

	if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}
	chat, reset := env.counts()
	if reset != 1 || chat != 2 {
		t.Errorf("reset=%d chat=%d, want 1 and 2", reset, chat)
	}
}

// TestFreePiForward_SiblingHoldBlocksReset is safety rule 1: while another
// process holds the account's lease, a 409 must NOT be repaired by resetting —
// that reset would land on the sibling's live request.
func TestFreePiForward_SiblingHoldBlocksReset(t *testing.T) {
	resetFreePiCooldownsForTest()
	env := newFreePiRecoveryEnv(t, http.StatusOK, `{"released":true}`)

	store := &fakeLeaseStore{holder: "a-different-process-epoch"}
	req := freePiTestRequest(t, env.srv, freePiTestToken("user_sibling"), nil, false)
	req.Leases = store
	rec := httptest.NewRecorder()

	err := ForwardFreePi(rec, req)
	var ue *proxy.UpstreamError
	if !asUpstreamError(err, &ue) || ue.StatusCode != http.StatusConflict {
		t.Fatalf("error = %v, want a 409 upstream error", err)
	}

	chat, reset := env.counts()
	if reset != 0 {
		t.Errorf("reset calls = %d, want 0 while a sibling holds the lease", reset)
	}
	if chat != 1 {
		t.Errorf("chat calls = %d, want 1 (no retry)", chat)
	}
}

// TestFreePiForward_OwnStaleLeasePermitsReset is safety rule 2: a lease already
// stamped with this process's epoch is our own leftover, so repairing it is
// provably safe.
func TestFreePiForward_OwnStaleLeasePermitsReset(t *testing.T) {
	resetFreePiCooldownsForTest()
	env := newFreePiRecoveryEnv(t, http.StatusOK, `{"released":true}`)

	store := &fakeLeaseStore{holder: freePiProcessEpoch}
	req := freePiTestRequest(t, env.srv, freePiTestToken("user_own"), nil, false)
	req.Leases = store

	if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}
	if _, reset := env.counts(); reset != 1 {
		t.Errorf("reset calls = %d, want 1", reset)
	}
}

func TestFreePiForward_ResetCooldownStopsTheLoop(t *testing.T) {
	resetFreePiCooldownsForTest()
	token := freePiTestToken("user_cooldown")

	// First request: 409 → reset → 200.
	first := newFreePiRecoveryEnv(t, http.StatusOK, `{"released":true}`)
	req := freePiTestRequest(t, first.srv, token, nil, false)
	req.Leases = &fakeLeaseStore{}
	if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("first ForwardFreePi: %v", err)
	}
	if _, reset := first.counts(); reset != 1 {
		t.Fatalf("first request reset %d times, want 1", reset)
	}

	// Second request inside the cooldown must not reset again, even though it
	// hits the identical conflict.
	second := newFreePiRecoveryEnv(t, http.StatusOK, `{"released":true}`)
	req2 := freePiTestRequest(t, second.srv, token, nil, false)
	req2.Leases = &fakeLeaseStore{}
	err := ForwardFreePi(httptest.NewRecorder(), req2)

	var ue *proxy.UpstreamError
	if !asUpstreamError(err, &ue) || ue.StatusCode != http.StatusConflict {
		t.Fatalf("second request error = %v, want a 409", err)
	}
	if _, reset := second.counts(); reset != 0 {
		t.Errorf("second request reset %d times inside the cooldown, want 0", reset)
	}
}

func TestFreePiForward_AutoResetOptOut(t *testing.T) {
	resetFreePiCooldownsForTest()
	env := newFreePiRecoveryEnv(t, http.StatusOK, `{"released":true}`)

	req := freePiTestRequest(t, env.srv, freePiTestToken("user_optout"),
		map[string]any{"freePiAutoReset": false}, false)
	req.Leases = &fakeLeaseStore{}

	err := ForwardFreePi(httptest.NewRecorder(), req)
	var ue *proxy.UpstreamError
	if !asUpstreamError(err, &ue) || ue.StatusCode != http.StatusConflict {
		t.Fatalf("error = %v, want a 409", err)
	}
	if _, reset := env.counts(); reset != 0 {
		t.Errorf("reset calls = %d, want 0 when auto-reset is opted out", reset)
	}
}

// TestFreePiForward_FailedResetDoesNotPoisonTheConnection: a reset that errors
// or returns non-200 must surface the original 409, not a synthetic failure, so
// the caller still sees the real reason and the connection is not locked.
func TestFreePiForward_FailedResetDoesNotPoisonTheConnection(t *testing.T) {
	resetFreePiCooldownsForTest()
	env := newFreePiRecoveryEnv(t, http.StatusInternalServerError, `{"error":"boom"}`)

	req := freePiTestRequest(t, env.srv, freePiTestToken("user_failreset"), nil, false)
	req.Leases = &fakeLeaseStore{}

	err := ForwardFreePi(httptest.NewRecorder(), req)
	var ue *proxy.UpstreamError
	if !asUpstreamError(err, &ue) {
		t.Fatalf("error = %v, want *proxy.UpstreamError", err)
	}
	if ue.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want the original 409", ue.StatusCode)
	}
}

func TestFreePiForward_LeaseIsAcquiredAndReleased(t *testing.T) {
	resetFreePiCooldownsForTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	defer srv.Close()

	store := &fakeLeaseStore{}
	req := freePiTestRequest(t, srv, freePiTestToken("user_lease"), nil, false)
	req.Leases = store

	if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}

	holder, acquire, release := store.snapshot()
	if acquire != 1 || release != 1 {
		t.Errorf("acquire=%d release=%d, want 1 and 1", acquire, release)
	}
	if holder != "" {
		t.Errorf("lease still held (%q) after the request finished", holder)
	}
}

// --- P3: cooldown bookkeeping --------------------------------------------

func TestFreePiResetCooldownIsBounded(t *testing.T) {
	resetFreePiCooldownsForTest()

	now := time.Now()
	// Every distinct account is admitted once...
	for i := 0; i < freePiCooldownEntries*2; i++ {
		if !freePiResetAllowed(fmt.Sprintf("acct-%d", i), now) {
			t.Fatalf("account %d was refused on its first attempt", i)
		}
	}
	freePiCooldownMu.Lock()
	size := len(freePiCooldowns)
	freePiCooldownMu.Unlock()

	if size > freePiCooldownEntries {
		t.Errorf("cooldown map grew to %d entries, cap is %d", size, freePiCooldownEntries)
	}

	// The two halves are independent assertions, so the map is emptied between
	// them. Leaving it full made this half non-deterministic: every entry above
	// carries the same `at`, so when the prune at the cap has to evict the
	// oldest it is choosing arbitrarily among ties, and "acct-fresh" could be
	// the one evicted by the prune inside the very call that asserts on it. The
	// lookup then misses, the attempt is allowed, and the test fails.
	resetFreePiCooldownsForTest()

	// ...and a repeat inside the window is refused.
	if !freePiResetAllowed("acct-fresh", now) {
		t.Error("first attempt should be allowed")
	}
	if freePiResetAllowed("acct-fresh", now.Add(time.Second)) {
		t.Error("second attempt inside the cooldown should be refused")
	}
	if !freePiResetAllowed("acct-fresh", now.Add(freePiResetCooldown+time.Second)) {
		t.Error("attempt after the cooldown should be allowed again")
	}
}

// --- helpers --------------------------------------------------------------

// asUpstreamError unwraps to the *proxy.UpstreamError the fallback layer
// branches on. It uses errors.As so a wrapped error still reports its status.
func asUpstreamError(err error, target **proxy.UpstreamError) bool {
	return errors.As(err, target)
}

// --- P3: origin derivation ------------------------------------------------

// TestFreePiOrigin pins the sibling-route derivation. Getting this wrong is
// expensive in a specific way: with the provider pointed at a stub or mirror,
// a hardcoded origin would send POST /session/reset to PRODUCTION while the
// chat calls went somewhere else — resetting a real account's sessions from a
// test or a staging run.
func TestFreePiOrigin(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "canonical chat endpoint", base: "https://api.freepi.ai/v1/chat/completions", want: "https://api.freepi.ai"},
		{name: "bare v1", base: "https://api.freepi.ai/v1", want: "https://api.freepi.ai"},
		{name: "chat path without v1", base: "https://api.freepi.ai/chat/completions", want: "https://api.freepi.ai"},
		{name: "bare origin", base: "https://api.freepi.ai", want: "https://api.freepi.ai"},
		{name: "trailing slash", base: "https://api.freepi.ai/", want: "https://api.freepi.ai"},
		{name: "local stub", base: "http://127.0.0.1:8080/v1/chat/completions", want: "http://127.0.0.1:8080"},
		{name: "empty falls back to the compiled-in origin", base: "", want: providers.FreePiOrigin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &providers.ProviderConfig{BaseURL: tt.base}
			if got := freePiOrigin(cfg); got != tt.want {
				t.Errorf("freePiOrigin(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}

	// A nil config must not panic; the registry default is the answer.
	if got := freePiOrigin(nil); got != providers.FreePiOrigin {
		t.Errorf("freePiOrigin(nil) = %q, want %q", got, providers.FreePiOrigin)
	}
}

// --- P3: dead credential --------------------------------------------------

// freePiExpiringToken builds a JWT-shaped credential with a chosen `exp`.
func freePiExpiringToken(t *testing.T, sub string, exp time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"sub": sub, "exp": exp.Unix()})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return "hdr." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// TestFreePiForward_ExpiredTokenIsRefusedBeforeTheRequest pins the local expiry
// check. free-pi has no refresh token, so an expired credential is terminal:
// spending a round trip to learn that is pointless, and letting the generic
// 401 rule cool the connection for two minutes means retrying a permanently
// dead credential forever while the dashboard shows it as healthy.
func TestFreePiForward_ExpiredTokenIsRefusedBeforeTheRequest(t *testing.T) {
	resetFreePiCooldownsForTest()

	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	defer srv.Close()

	req := freePiTestRequest(t, srv, freePiExpiringToken(t, "user_expired", time.Now().Add(-time.Hour)), nil, false)
	rec := httptest.NewRecorder()

	err := ForwardFreePi(rec, req)
	var ue *proxy.UpstreamError
	if !asUpstreamError(err, &ue) {
		t.Fatalf("error = %v, want *proxy.UpstreamError", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), FreePiTokenExpiredCode) {
		t.Errorf("body = %s, want the %s code so the long-cooldown rule applies", ue.Body, FreePiTokenExpiredCode)
	}
	if called {
		t.Error("the request reached upstream; a dead credential must be refused locally")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("nothing should be written, got %q", rec.Body.String())
	}
}

func TestFreePiForward_LiveTokenStillGoesUpstream(t *testing.T) {
	resetFreePiCooldownsForTest()

	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	defer srv.Close()

	live := freePiExpiringToken(t, "user_live", time.Now().Add(24*time.Hour))
	if err := ForwardFreePi(httptest.NewRecorder(), freePiTestRequest(t, srv, live, nil, false)); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}
	if !called {
		t.Error("a live credential was refused locally")
	}
}

// TestFreePiForward_OpaqueTokenIsNotRefusedLocally: the expiry check must only
// act on a token it can actually read. An unparseable credential has to reach
// upstream, which is the only authority on whether it is valid.
func TestFreePiForward_OpaqueTokenIsNotRefusedLocally(t *testing.T) {
	resetFreePiCooldownsForTest()

	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	defer srv.Close()

	if err := ForwardFreePi(httptest.NewRecorder(), freePiTestRequest(t, srv, "opaque-not-a-jwt", nil, false)); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}
	if !called {
		t.Error("an opaque credential was refused locally instead of being sent upstream")
	}
}
