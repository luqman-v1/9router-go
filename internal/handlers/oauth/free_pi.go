package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// free-pi device-flow login.
//
// The flow is the CLI's own, reproduced from dennisonbertram/free-pi-cli
// src/auth-flow.ts so the user never has to install or run free-pi-cli:
//
//	POST /auth/github/device {}                       -> {session_id, user_code, verification_uri, interval}
//	POST /auth/consent {session_id, consent_version}   -> records consent for that login session
//	POST /auth/token {session_id}                      -> {token} once GitHub authorises, else {retry_after}
//
// Two deliberate differences from the reference proxies, both about consent:
//
//   - Consent is NEVER recorded automatically. free-pi's screen discloses that
//     ads receive excerpts of the conversation and that prompts, code, files and
//     responses are saved as traces, used to train models, and may be shared or
//     sold with no opt-out. That is the user's decision to make, so initiate()
//     refuses to call upstream until the caller asserts that this exact user
//     accepted this exact version.
//   - Declining therefore makes ZERO server calls, matching the privacy
//     invariant free-pi-cli itself keeps (src/run.ts).

// freePiAuthBaseURL is the API root for the auth routes. Var so tests can point
// it at a fake upstream.
var freePiAuthBaseURL = providers.FreePiOrigin

// freePiPendingTTL bounds how long a device login may stay pending. It is a
// backstop against abandoned attempts accumulating, NOT a policy on how long a
// user may take: the device code itself lives ~899s (expires_in), and a
// first-time GitHub sign-in with 2FA can easily outlast that. The window is
// therefore the server's lifetime plus a minute of slack, so we never cut a
// login off *before* upstream would.
const freePiPendingTTL = 20 * time.Minute

// freePiPendingEntries caps the pending-login table. Each entry is a few dozen
// bytes and lives at most freePiPendingTTL, so the cap is a backstop rather than
// a working limit (AGENTS.md section 4.G).
const freePiPendingEntries = 64

type freePiPendingLogin struct {
	createdAt time.Time
	interval  int
	// expiresAt is the device code's own deadline (server `expires_in`), so an
	// entry is dropped when upstream would refuse it anyway.
	expiresAt time.Time
}

var (
	freePiPendingMu sync.Mutex
	// freePiPending maps the device-flow session id to its pending state.
	freePiPending = make(map[string]freePiPendingLogin, freePiPendingEntries)
)

// freePiPendingStore records a login awaiting GitHub authorisation. expiresIn is
// the device code's lifetime in seconds as reported by the server; a
// non-positive value falls back to freePiPendingTTL.
func freePiPendingStore(sessionID string, interval, expiresIn int) {
	freePiPendingMu.Lock()
	defer freePiPendingMu.Unlock()

	now := time.Now()
	for k, v := range freePiPending {
		if now.After(v.expiresAt) {
			delete(freePiPending, k)
		}
	}

	deadline := now.Add(freePiPendingTTL)
	if expiresIn > 0 {
		// One minute of slack past the server's own deadline: a poll that lands
		// just after it should get upstream's answer, not ours.
		deadline = now.Add(time.Duration(expiresIn)*time.Second + time.Minute)
	}
	if len(freePiPending) >= freePiPendingEntries {
		var oldestKey string
		var oldest time.Time
		for k, v := range freePiPending {
			if oldestKey == "" || v.createdAt.Before(oldest) {
				oldestKey, oldest = k, v.createdAt
			}
		}
		delete(freePiPending, oldestKey)
	}
	freePiPending[sessionID] = freePiPendingLogin{createdAt: now, interval: interval, expiresAt: deadline}
}

// freePiPendingKnown reports whether the session is one we started and has not
// expired. An unknown session is rejected rather than forwarded: the poll takes a
// caller-supplied id, and forwarding arbitrary ids upstream would make this
// endpoint a proxy for login sessions it never opened.
func freePiPendingKnown(sessionID string) bool {
	freePiPendingMu.Lock()
	defer freePiPendingMu.Unlock()

	v, ok := freePiPending[sessionID]
	if !ok {
		return false
	}
	if time.Now().After(v.expiresAt) {
		delete(freePiPending, sessionID)
		return false
	}
	return true
}

// freePiPendingForget drops a completed or abandoned login.
func freePiPendingForget(sessionID string) {
	freePiPendingMu.Lock()
	defer freePiPendingMu.Unlock()
	delete(freePiPending, sessionID)
}

// freePiResetPendingForTest clears the table so tests stay independent.
func freePiResetPendingForTest() {
	freePiPendingMu.Lock()
	defer freePiPendingMu.Unlock()
	freePiPending = make(map[string]freePiPendingLogin, freePiPendingEntries)
}

// --- HTTP -----------------------------------------------------------------

// HandleFreePiInitiate starts the GitHub device login.
// POST /api/oauth/free-pi/initiate
func (h *OAuthHandler) HandleFreePiInitiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ConsentAccepted bool   `json:"consentAccepted"`
		ConsentVersion  string `json:"consentVersion"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	}

	// The gate. No consent, no upstream call: not even the device-code request,
	// so declining leaves no trace on free-pi's side.
	if !body.ConsentAccepted {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"free-pi consent must be accepted explicitly before signing in; it covers training on your prompts and code")
		return
	}
	if v := body.ConsentVersion; v != "" && v != providers.FreePiConsentVersion {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("stale consent version %q; the current version is %q — reload and review it again", v, providers.FreePiConsentVersion))
		return
	}

	device, err := freePiStartDeviceFlow(r)
	if err != nil {
		log.Error("oauth", "free-pi device flow start failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	if err := freePiSubmitConsent(r, device.SessionID); err != nil {
		log.Error("oauth", "free-pi consent submission failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	freePiPendingStore(device.SessionID, device.Interval, device.ExpiresIn)

	resp := map[string]any{
		"sessionId":       device.SessionID,
		"userCode":        device.UserCode,
		"verificationUri": device.VerificationURI,
		"interval":        device.Interval,
		"expiresIn":       device.ExpiresIn,
		"consentVersion":  providers.FreePiConsentVersion,
	}
	// verification_uri_complete has the code pre-filled so the user just clicks
	// Authorize instead of typing the code from one window into another.
	if device.VerificationURIComplete != "" {
		resp["verificationUriComplete"] = device.VerificationURIComplete
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleFreePiPoll polls the login until a token is issued, then saves the
// connection.
// POST /api/oauth/free-pi/poll
func (h *OAuthHandler) HandleFreePiPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		SessionID string `json:"sessionId"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	}
	if body.SessionID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "sessionId is required")
		return
	}
	if !freePiPendingKnown(body.SessionID) {
		// 410 rather than a bare 400: the client must be able to tell "this sign-in
		// attempt is dead, start again" from a transient failure it should retry.
		// Without that distinction the dashboard can only spin forever.
		log.Warn("oauth", "free-pi sign-in attempt expired before completing", "session", body.SessionID)
		handlerutil.WriteJSONError(w, http.StatusGone,
			"this free-pi sign-in attempt expired or was never started here; start again")
		return
	}

	outcome, err := freePiPollToken(r, body.SessionID)
	if err != nil {
		log.Error("oauth", "free-pi token poll failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	// State changes are logged at a level the default configuration shows; the
	// repeating "still pending" line stays at debug so a 5s poll does not flood
	// the terminal. Without the state-change lines a stalled sign-in is
	// indistinguishable from a broken one.
	switch outcome.Status {
	case freePiPollGranted:
		log.Info("oauth", "free-pi sign-in authorised", "session", body.SessionID)
	case freePiPollConsentRequired:
		log.Warn("oauth", "free-pi sign-in needs consent", "session", body.SessionID)
	default:
		log.Debug("oauth", "free-pi token poll pending", "session", body.SessionID, "retryAfter", outcome.RetryAfter)
	}

	switch outcome.Status {
	case freePiPollGranted:
		freePiPendingForget(body.SessionID)
		h.saveFreePiConnection(w, r, outcome.Token)
	case freePiPollConsentRequired:
		// The server has no record of consent for this session: a real state,
		// not a transient one, so it gets its own status rather than "pending"
		// and an endless poll.
		freePiPendingForget(body.SessionID)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "consent_required",
			"message": "free-pi did not record consent for this login; start again and accept the terms",
		})
	default:
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status":     "pending",
			"retryAfter": outcome.RetryAfter,
		})
	}
}

// HandleFreePiSessionReset force-releases the account's other sessions on
// demand — the dashboard equivalent of free-pi-cli's /close-other-session.
// POST /api/oauth/free-pi/session/reset
func (h *OAuthHandler) HandleFreePiSessionReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ConnectionID string `json:"connectionId"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	}
	if body.ConnectionID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "connectionId is required")
		return
	}

	conn, err := h.freePiConnection(body.ConnectionID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	token := freePiConnectionToken(conn)
	if token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "connection has no free-pi credential")
		return
	}

	released, err := freePiResetSessions(r, token)
	if err != nil {
		log.Error("oauth", "free-pi session reset failed", "conn", body.ConnectionID, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"released": released,
	})
}
