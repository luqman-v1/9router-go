package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// ForwardFreePi sends an [OI] Chat Completions request to free-pi
// (api.freepi.ai).
//
// The body is forwarded byte-identical: free-pi performs no request-shape
// checks and has no TLS fingerprint gate (hungkevin/free-pi-cli-proxy), so there
// is nothing to rewrite and nothing to cloak. What it does require is two
// per-request headers that proxy.Forward[OI] cannot express, because it only
// knows the registry's static map:
//
//	x-session-id      stable per ACCOUNT (see free_pi_session.go)
//	x-client-version  the server's own `latest`, or the compiled-in floor
//
// It also must NOT be registered on the default passthrough: executor.Default()
// never inspects resp.StatusCode and jsonResponse always writes 200, which would
// turn a 409 `concurrent_session` into a successful turn carrying an error body.
// This executor checks the status itself and returns a real *proxy.UpstreamError,
// exactly as ForwardFreebuff does.
func ForwardFreePi(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	if req.Config == nil {
		return errors.New("free-pi: no provider config")
	}
	token := req.APIKey
	if token == "" {
		return errors.New("free-pi: access token is required")
	}

	// A dead credential is refused before the request goes out. free-pi has no
	// refresh token, so this is terminal and the honest answer is
	// "re-authenticate", not a 401 discovered after a wasted round trip.
	if freePiTokenExpired(token, time.Now()) {
		return freePiExpiredTokenError()
	}

	state := newFreePiSessionState(token, req.ConnData)
	// The stored snapshot is what the connection recorded at login and is never
	// refreshed, so resolve it against the server's current floor here. Without
	// this a server-side `min` raise breaks every request from a container that
	// is already current (see providers/freepi_version.go).
	state.ClientVersion = providers.ResolveFreePiClientVersion(ctx, client, state.ClientVersion)

	// Claim the account for this process. Failing to acquire is not an error:
	// it only means a sibling holds reset rights, so this request must not
	// reset on a 409. The deferred release stops a panic or early return from
	// stranding the claim until its TTL.
	if freePiAcquireLease(req.Leases, state.AccountKey) {
		defer freePiReleaseLease(req.Leases, state.AccountKey)
	}

	resp, err := freePiDoChat(ctx, client, req, state)
	if err != nil {
		// proxy.DoRequest returns any non-200 as an *UpstreamError, so the
		// status is on the error rather than the response. That is also the
		// reason this provider cannot ride executor.Default(): there the same
		// status would never be inspected and a 409 would reach the client as a
		// successful turn carrying an error body.
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) {
			freePiNoteAdGate(err)
			return err
		}

		switch ue.StatusCode {
		case http.StatusUpgradeRequired:
			// 426 is the forced-update gate: the version we sent is below the
			// server's `min`. The cached snapshot is stale by definition at this
			// point, so refresh it and retry once.
			retried := freePiVersionRetry(ctx, client, req, state)
			if retried == nil {
				return err
			}
			resp = retried
		case http.StatusConflict:
			code, _ := freePiErrorCode(ue.Body)
			retried, retryErr := freePiResetAndRetry(ctx, client, req, state, code)
			if retryErr != nil {
				return retryErr
			}
			if retried == nil {
				// Reset suppressed (sibling in flight, cooldown, opt-out) or the
				// reset itself failed: report the real conflict, unchanged, rather
				// than a synthetic error the client cannot act on.
				return ue
			}
			resp = retried
		default:
			freePiNoteAdGate(err)
			return err
		}
	}

	forwardUpstreamResponseHeaders(w, resp.Header)

	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "free-pi")
		defer func() { _ = stallReader.Close() }()
		return execSSEStream(w, stallReader, req)
	}

	defer func() { _ = resp.Body.Close() }()
	return jsonResponse(ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// freePiDoChat performs one upstream attempt with the free-pi header set.
func freePiDoChat(ctx context.Context, client *http.Client, req *Request, state freePiSessionState) (*http.Response, error) {
	// A fresh map per attempt: the registry's StaticHeaders are shared across
	// every request in the process, so the two per-account headers must never
	// be written into that map.
	headers := map[string]string{
		"Authorization":    "Bearer " + req.APIKey,
		"Content-Type":     "application/json",
		"x-session-id":     state.SessionID,
		"x-client-version": state.ClientVersion,
	}
	if req.IsStream {
		headers["Accept"] = "text/event-stream"
	}
	for k, v := range req.Config.StaticHeaders {
		headers[k] = v
	}
	return proxy.DoRequest(ctx, client, http.MethodPost, req.Config.BaseURL, headers, req.Body)
}

// freePiResetAndRetry handles a 409: it decides whether an automatic reset is
// permitted, performs it at most once, and retries once.
//
// Returns (nil, nil) when the reset was suppressed or failed, leaving the caller
// to report the original 409; a non-nil error means the retry itself blew up.
func freePiResetAndRetry(ctx context.Context, client *http.Client, req *Request, state freePiSessionState, code string) (*http.Response, error) {
	if !state.AutoReset {
		log.Debug("free-pi", "auto-reset disabled for this connection; returning 409", "account", state.AccountKey)
		return nil, nil
	}

	if !freePiResetPermitted(req.Leases, state.AccountKey, code) {
		return nil, nil
	}

	if err := freePiResetSession(ctx, client, req.Config, req.APIKey); err != nil {
		log.Warn("free-pi", "session reset failed", "account", state.AccountKey, "error", err)
		return nil, nil
	}

	resp, err := freePiDoChat(ctx, client, req, state)
	if err != nil {
		return nil, fmt.Errorf("free-pi upstream retry: %w", err)
	}
	return resp, nil
}

// freePiVersionRetry handles a 426: the server rejected x-client-version as
// below its published `min`. It refreshes the snapshot, re-resolves the header
// and retries exactly once.
//
// It returns nil when the retry cannot help — the refresh failed, or the
// published floor did not advance past what we already sent — and the caller
// then reports the original 426. Replaying an unchanged header would only draw
// the same answer.
func freePiVersionRetry(ctx context.Context, client *http.Client, req *Request, state freePiSessionState) *http.Response {
	if err := providers.RefreshFreePiClientVersion(ctx, client); err != nil {
		log.Warn("free-pi", "client-version refresh failed after 426",
			"account", state.AccountKey, "sent", state.ClientVersion, "error", err)
		return nil
	}

	next := providers.ResolveFreePiClientVersion(ctx, client, state.ClientVersion)
	if next == state.ClientVersion {
		log.Warn("free-pi", "426 but the published version floor did not advance past what we sent",
			"account", state.AccountKey, "sent", state.ClientVersion)
		return nil
	}

	log.Info("free-pi", "retrying once with the refreshed client version after 426",
		"account", state.AccountKey, "from", state.ClientVersion, "to", next)
	state.ClientVersion = next

	resp, err := freePiDoChat(ctx, client, req, state)
	if err != nil {
		return nil
	}
	return resp
}

// freePiResetPermitted is the reset gate: lease ownership, then cooldown.
//
// The lease check and the cooldown check are separate on purpose. The lease
// answers "is a sibling driving this account right now?"; the cooldown answers
// "did we already try this recently?". Both must pass, and the lease mutex is
// released before the network call so a slow reset cannot block the stripe.
func freePiResetPermitted(store LeaseStore, accountKey, code string) bool {
	mu := freePiStripe(accountKey)
	mu.Lock()
	mayReset := freePiMayReset(store, accountKey)
	mu.Unlock()

	if !mayReset {
		log.Warn("free-pi", "409 while a sibling holds the session lease; not resetting",
			"account", accountKey, "code", code)
		return false
	}

	if !freePiResetAllowed(accountKey, time.Now()) {
		log.Warn("free-pi", "409 inside the reset cooldown; not resetting again",
			"account", accountKey, "code", code)
		return false
	}
	return true
}

// freePiResetSession calls POST /session/reset, which releases the caller's
// OTHER sessions — the same operation free-pi-cli exposes as
// /close-other-session.
//
// `released:false` is success: it means there was nothing to release, which is
// exactly the outcome the caller wanted.
func freePiResetSession(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, token string) error {
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	if cfg != nil {
		for k, v := range cfg.StaticHeaders {
			headers[k] = v
		}
	}
	resp, err := proxy.DoRequest(ctx, client, http.MethodPost, freePiOrigin(cfg)+"/session/reset", headers, nil)
	if err != nil {
		return fmt.Errorf("reset request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
	}
	return nil
}

// freePiOrigin derives the API root from the configured chat endpoint, so an
// operator pointing the provider at a stub or a mirror moves the sibling routes
// (/session/reset, /ads/*, /client-version) with it instead of silently reaching
// production.
func freePiOrigin(cfg *providers.ProviderConfig) string {
	base := ""
	if cfg != nil {
		base = cfg.BaseURL
	}
	if base == "" {
		return providers.FreePiOrigin
	}
	for _, suffix := range []string{"/v1/chat/completions", "/chat/completions", "/v1"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return strings.TrimRight(base, "/")
}

// freePiErrorCode pulls `code` and `message` out of the upstream error
// envelope. Callers branch on the code only: the message text is server-owned
// and changes between deploys (the CLI's own error-notice extension makes the
// same point).
func freePiErrorCode(body []byte) (code, message string) {
	if len(body) == 0 {
		return "", ""
	}
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", ""
	}
	code = env.Code
	if code == "" {
		code = env.Error.Code
	}
	message = env.Message
	if message == "" {
		message = env.Error.Message
	}
	return code, message
}

// freePiExpiredTokenError reports a locally-detected dead credential using the
// same envelope shape as upstream, so the fallback layer and the dashboard both
// read it without a special case.
func freePiExpiredTokenError() error {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    FreePiTokenExpiredCode,
			"message": "free-pi credential has expired; free-pi has no refresh token, so sign in again to get a new one",
			"type":    FreePiTokenExpiredCode,
		},
	})
	// 401 so the existing auth-failure classification applies, with the body's
	// code steering it to the long cooldown rule rather than the 2-minute one.
	return &proxy.UpstreamError{StatusCode: http.StatusUnauthorized, Body: body}
}

// freePiNoteAdGate names the one 403 that has a distinct cause and a distinct
// remedy: upstream will not serve this account until it records an ad
// impression. Logging it explicitly turns an otherwise opaque 403 into an
// actionable line — and never inventing an impression to clear it is the whole
// point (a false impression is a false report to the advertiser whose spend
// funds the inference).
func freePiNoteAdGate(err error) {
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusForbidden {
		return
	}
	_, message := freePiErrorCode(ue.Body)
	if !strings.Contains(strings.ToLower(message), strings.ToLower(providers.FreePiAdGateMessage)) {
		return
	}
	log.Warn("free-pi", "upstream refused the request pending an ad impression",
		"status", ue.StatusCode)
}
