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

	// A stored id must NOT win, even one left behind by an older login that
	// persisted the derived value. Honouring it is what made rotation a no-op:
	// every step returned the same id, so a mid-conversation account switch
	// could never open a fresh session.
	for _, junk := range []string{"pinned-id", "", "another-accounts-id"} {
		got := freePiSessionIDFor(token, map[string]any{"freePiSessionId": junk})
		if got != first {
			t.Errorf("stored id %q overrode the derivation: got %q, want %q", junk, got, first)
		}
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

// --- rotation: a conversation locked by another account --------------------

// freePiConversationLockBody is the refusal upstream returns when a conversation
// opened under one account is replayed on another. It is prose, not a code: the
// server states the ownership rule rather than naming an error class.
const freePiConversationLockBody = `{"error":{"message":"This conversation belongs to another user/session. Account rotation is not allowed."}}`

// TestFreePiSessionVariant_DiffersPerRotationAndIsStable proves the two halves
// the rotation needs at once: step N never collides with step 0 or step N+1, and
// the same step always derives the same id so a replay after a restart still
// lands on the conversation it opened.
func TestFreePiSessionVariant_DiffersPerRotationAndIsStable(t *testing.T) {
	token := freePiTestToken("user_rotate")

	base := freePiAccountSessionID(token)
	if got := freePiSessionVariant(token, 0); got != base {
		t.Errorf("step 0 = %q, want the base session %q", got, base)
	}

	seen := map[string]uint64{base: 0}
	for step := uint64(1); step <= 8; step++ {
		id := freePiSessionVariant(token, step)
		if id == "" {
			t.Fatalf("step %d derived an empty session id", step)
		}
		if prev, dup := seen[id]; dup {
			t.Errorf("step %d collided with step %d on %q", step, prev, id)
		}
		seen[id] = step
		if again := freePiSessionVariant(token, step); again != id {
			t.Errorf("step %d is not stable: %q then %q", step, id, again)
		}
	}

	// A different account must not share any of them.
	other := freePiTestToken("user_other")
	for step := uint64(0); step <= 8; step++ {
		if freePiSessionVariant(token, step) == freePiSessionVariant(other, step) {
			t.Errorf("accounts collided on step %d", step)
		}
	}
}

// TestFreePiForward_RotatesSessionOnConversationLock is the regression test for
// the reported failure: a conversation-locked refusal must open a fresh session
// and succeed, rather than replaying the bound id and refusing the account.
func TestFreePiForward_RotatesSessionOnConversationLock(t *testing.T) {
	var (
		mu         sync.Mutex
		sessionIDs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessionIDs = append(sessionIDs, r.Header.Get("x-session-id"))
		n := len(sessionIDs)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, freePiConversationLockBody)
			return
		}
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	defer srv.Close()

	token := freePiTestToken("user_lockrotate")
	req := freePiTestRequest(t, srv, token, nil, false)
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the rotated retry to succeed: %s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sessionIDs) != 2 {
		t.Fatalf("upstream saw %d requests, want 2 (refusal + rotated retry)", len(sessionIDs))
	}
	if sessionIDs[0] == sessionIDs[1] {
		t.Errorf("the retry reused the locked session id %q", sessionIDs[0])
	}
	// The rotated id must be a real rotation of THIS account rather than the
	// base session or an arbitrary value. The step comes from a process-wide
	// counter, so the assertion is that the id is one of the account's rotation
	// variants — reproducible, and distinct from the base.
	base := freePiAccountSessionID(token)
	if sessionIDs[1] == base {
		t.Errorf("rotated session %q is the account's base session, not a rotation", sessionIDs[1])
	}
	variant := false
	for step := uint64(1); step <= 64; step++ {
		if sessionIDs[1] == freePiSessionVariant(token, step) {
			variant = true
			break
		}
	}
	if !variant {
		t.Errorf("rotated session %q is not a derivation of this account", sessionIDs[1])
	}
}

// TestFreePiForward_DoesNotRotateOnUnrelatedRefusal keeps the rotation narrow: a
// 403 that is not about conversation ownership must reach the client unchanged,
// with no second upstream call.
func TestFreePiForward_DoesNotRotateOnUnrelatedRefusal(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"ads_not_showing","message":"Ads aren't showing for this account."}`)
	}))
	defer srv.Close()

	req := freePiTestRequest(t, srv, freePiTestToken("user_other403"), nil, false)
	rec := httptest.NewRecorder()

	err := ForwardFreePi(rec, req)
	if err == nil {
		t.Fatal("the unrelated 403 was swallowed")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("upstream calls = %d, want exactly 1 (no retry for an unrelated refusal)", calls)
	}
}

// TestFreePiForward_RotatesEvenWithAStoredSessionID proves rotation is not
// defeated by a value left in connection data. An older login persisted the
// derived id there and every reader treated it as a pin, so freePiRotated…For
// returned it at every step and a mid-conversation account switch could never
// open a fresh session — upstream kept refusing the conversation as owned by
// another account. The retry must now happen exactly once regardless.
func TestFreePiForward_RotatesEvenWithAStoredSessionID(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First attempt refused; the replay on a fresh session succeeds.
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, freePiConversationLockBody)
			return
		}
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	defer srv.Close()

	connData := map[string]any{"freePiSessionId": "stale-persisted-id"}
	req := freePiTestRequest(t, srv, freePiTestToken("user_stale"), connData, false)

	if err := ForwardFreePi(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("the rotated retry did not recover: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("upstream calls = %d, want 2 (one refusal, one rotated replay)", calls)
	}
}

// --- 200-body ownership refusal -------------------------------------------

// freePiOwnershipRefusalBody is the refusal as free-pi actually serves it: an
// ordinary chat.completion whose message content is the notice, with
// finish_reason "stop" and zero usage. The status is 200, which is why the
// status switch in ForwardFreePi cannot see it.
const freePiOwnershipRefusalBody = `{
  "id": "chatcmpl-freepi-abc123",
  "object": "chat.completion",
  "model": "deepseek/deepseek-v4-flash",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "This conversation belongs to another free-pi account. Sharing or rotating accounts isn't allowed; use one account per person."
    },
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 41, "completion_tokens": 0, "total_tokens": 41}
}`

// TestFreePiOwnershipRefusalIsDetected pins the detector against the real body
// shape: the notice is in message.content, the status is 200, and usage is zero.
func TestFreePiOwnershipRefusalIsDetected(t *testing.T) {
	refusal, ok := freePiOwnershipRefusalInBody([]byte(freePiOwnershipRefusalBody))
	if !ok {
		t.Fatal("the 200-body refusal was not detected; the client would receive it as an answer")
	}
	if refusal.status != http.StatusForbidden {
		t.Errorf("status = %d, want 403 so the fallback layer treats it as a refusal", refusal.status)
	}
	if !strings.Contains(refusal.message, "one account per person") {
		t.Errorf("message = %q, want the verbatim upstream notice", refusal.message)
	}
}

// TestFreePiOwnershipRefusalIsNotForwardedAsAnAnswer is the end-to-end half: the
// executor must return an error rather than relaying the notice as a completion.
func TestFreePiOwnershipRefusalIsNotForwardedAsAnAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, freePiOwnershipRefusalBody)
	}))
	defer srv.Close()

	req := freePiTestRequest(t, srv, "free-pi-jwt", nil, false)
	rec := httptest.NewRecorder()

	err := ForwardFreePi(rec, req)
	if err == nil {
		t.Fatal("ForwardFreePi relayed the refusal as a successful turn")
	}
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("error type = %T, want *proxy.UpstreamError", err)
	}
	if ue.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "one account per person") {
		t.Errorf("body = %s, want the upstream notice preserved", ue.Body)
	}
}

// TestFreePiGenuineAnswerOnAccountSharingSurvives is the false-positive guard:
// a real answer that DISCUSSES the refusal spent tokens producing it, so it must
// reach the client untouched. Without this the detector would eat legitimate
// replies about free-pi's policy.
func TestFreePiGenuineAnswerOnAccountSharingSurvives(t *testing.T) {
	body := `{
  "id": "chatcmpl-freepi-real",
  "object": "chat.completion",
  "model": "deepseek/deepseek-v4-flash",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "free-pi says: This conversation belongs to another free-pi account. Sharing or rotating accounts isn't allowed; use one account per person. That is why the request failed."
    },
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 120, "completion_tokens": 47, "total_tokens": 167}
}`

	if _, ok := freePiOwnershipRefusalInBody([]byte(body)); ok {
		t.Error("a genuine answer quoting the notice was flagged as a refusal")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	req := freePiTestRequest(t, srv, "free-pi-jwt", nil, false)
	rec := httptest.NewRecorder()
	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi rejected a genuine answer: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestFreePiOwnershipRefusalRequiresZeroUsage pins the second signal on its own:
// marker text with token spend is a real answer, so the usage counter is not
// optional decoration.
func TestFreePiOwnershipRefusalRequiresZeroUsage(t *testing.T) {
	withSpend := strings.Replace(freePiOwnershipRefusalBody,
		`"completion_tokens": 0`, `"completion_tokens": 12`, 1)
	if _, ok := freePiOwnershipRefusalInBody([]byte(withSpend)); ok {
		t.Error("a body with completion_tokens > 0 was flagged as a refusal")
	}

	// And zero usage with unrelated content is not a refusal either.
	unrelated := strings.Replace(freePiOwnershipRefusalBody,
		"This conversation belongs to another free-pi account. Sharing or rotating accounts isn't allowed; use one account per person.",
		"Here is the weather in Dhaka.", 1)
	if _, ok := freePiOwnershipRefusalInBody([]byte(unrelated)); ok {
		t.Error("zero usage with unrelated content was flagged as a refusal")
	}
}

// TestFreePiSessionIDsNeverCrossAccounts pins the invariant the whole design
// rests on: a session id is a function of the account, so account A can never
// present account B's id — which is precisely what upstream refuses with "This
// conversation belongs to another free-pi account."
//
// Checked across rotation steps too, not just step 0: a rotated id is derived
// from (account, step), and a bug that dropped the account from that tuple would
// make two accounts collide on step 1 while still passing at step 0.
func TestFreePiSessionIDsNeverCrossAccounts(t *testing.T) {
	accounts := []string{
		freePiTestToken("user_alpha"),
		freePiTestToken("user_beta"),
		freePiTestToken("user_gamma"),
		"opaque-not-a-jwt-alpha",
		"opaque-not-a-jwt-beta",
	}

	seen := map[string]string{}
	for _, token := range accounts {
		for step := uint64(0); step <= 8; step++ {
			id := freePiRotatedSessionIDFor(token, nil, step)
			if id == "" {
				t.Fatalf("empty session id for step %d", step)
			}
			if owner, dup := seen[id]; dup {
				t.Errorf("session id collision: %q used by two accounts (%s and %s)",
					id, owner, freePiAccountKey(token))
			}
			seen[id] = freePiAccountKey(token)
		}
	}

	// A stored value in connection data must not let one account borrow another's
	// id — the stale-persistence case that defeated rotation before.
	borrowed := freePiRotatedSessionIDFor(freePiTestToken("user_alpha"),
		map[string]any{"freePiSessionId": freePiAccountSessionID(freePiTestToken("user_beta"))}, 1)
	if borrowed == freePiAccountSessionID(freePiTestToken("user_beta")) {
		t.Error("a stored id let one account present another account's session id")
	}
}
