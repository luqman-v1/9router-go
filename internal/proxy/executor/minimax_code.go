package executor

import (
	"crypto/rand"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// MiniMax Code (mcode) — the credits lane. Anthropic Messages on MiniMax's
// mavis gateway, reached with the OAuth bearer plus the per-request session
// headers the gateway expects. Port of upstream open-sse/executors/minimax-code.js
// (decolua/9router 85bc33ce).

// quotaWordRE matches a body that says the account ran out of credits rather
// than that its credential is bad.
//
// The word list is magpie's minimax plugin answer(); the CJK terms matter as
// much as the English ones, because MiniMax Code answers in the account's own
// language. A 402/403 that does NOT name the balance passes through untouched:
// that one is a real auth refusal and the on-401 refresh path should see it.
var quotaWordRE = regexp.MustCompile(`(?i)insufficient|balance|credit|quota|exhaust|limit|余额|积分|额度|不足|用完|上限`)

// NormalizeQuotaResponse rewrites a credits refusal into the 429 the account
// loop understands.
//
// MiniMax Code reports credit exhaustion as 402/403 with a body that names the
// balance. The account loop treats 401/403 as refresh-and-retry and 429 as
// switch-to-the-next-account, so a credits refusal left at 402/403 would keep
// retrying a half-spent account instead of failing over — or, on 403, spend a
// single-use refresh token per request before finally failing.
//
// A 429 is already the right signal and passes through unchanged.
func NormalizeQuotaResponse(status int, body []byte) (int, []byte) {
	if status != http.StatusPaymentRequired && status != http.StatusForbidden {
		return status, body
	}
	if !quotaWordRE.Match(body) {
		return status, body
	}
	return http.StatusTooManyRequests, quotaRateLimitBody(body)
}

// quotaRateLimitBody renders the normalized 429 body: the upstream message
// preserved inside the rate_limit_error envelope the account loop reads.
func quotaRateLimitBody(body []byte) []byte {
	msg := strings.TrimSpace(string(body))
	if parsed := quotaMessage(body); parsed != "" {
		msg = parsed
	}
	out, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "rate_limit_error", "message": "usage limit reached: " + msg},
	})
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"usage limit reached"}}`)
	}
	return out
}

// quotaMessage digs the human-readable message out of the envelope shapes
// MiniMax uses, falling back to "" so the caller keeps the raw body.
func quotaMessage(body []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message  string `json:"message"`
		Msg      string `json:"msg"`
		BaseResp struct {
			StatusMsg string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	for _, candidate := range []string{env.Error.Message, env.Message, env.Msg, env.BaseResp.StatusMsg} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

// ForwardMinimaxCode serves a request on the mavis Messages endpoint.
//
// The endpoint speaks Claude Messages natively, so a /v1/messages client is
// forwarded in its own wire format and a Chat Completions caller is converted
// the same way every other Claude upstream is.
func ForwardMinimaxCode(w http.ResponseWriter, req *Request) error {
	body := req.Body
	if req.ClaudeClient {
		body = translator.SanitizeClaudePassthrough(body, translator.ClaudeIntentionalPrefill(req.Body))
	} else {
		body = EnsureClaudeMessages(body, req.ModelName)
	}

	resp, err := proxy.DoRequest(reqCtx(req), req.Client, http.MethodPost,
		minimaxCodeMessagesURL(req.Config), minimaxCodeHeaders(req), body)
	if err != nil {
		// DoRequest turns any non-200 into a typed error, so the credits
		// refusal arrives here rather than as a response to inspect.
		return normalizedQuotaError(err)
	}
	defer resp.Body.Close()

	// Forward the upstream's retry and rate-limit headers before any branch
	// below writes a status, exactly like every other executor.
	forwardUpstreamResponseHeaders(w, resp.Header)

	// handleClaudeMessages* are the "translate a Claude reply for a non-Claude
	// client" helpers: their passthrough branch is taken on TranslateResp, which
	// is only ever true for such a client. A Claude client is the opposite case,
	// so its reply is relayed untouched.
	if req.TranslateResp {
		if req.IsStream {
			return handleClaudeMessagesStream(w, req, resp.Body)
		}
		return handleClaudeMessagesNonStream(w, req, resp.Body)
	}
	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, false, req.ResponseBuf)
}

// normalizedQuotaError rewrites a credits refusal carried by an upstream
// error into the 429 the account loop understands. Any other failure — a real
// auth refusal, a 5xx — is passed through with its own status, so the on-401
// refresh path still sees what actually happened.
func normalizedQuotaError(err error) error {
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		return err
	}
	status, body := NormalizeQuotaResponse(ue.StatusCode, ue.Body)
	if status == ue.StatusCode {
		return err
	}
	// A credits refusal must leave as an error, not as a body: writing it to
	// the client would report a served turn and end combo fallback.
	return &proxy.UpstreamError{StatusCode: status, Body: body, Header: ue.Header}
}

// minimaxCodeMessagesURL resolves the endpoint: the registry base URL, or the
// relay host an edge proxy pool fronts with the real destination in its headers
// (BuildEdgeRelayHeaders), which answers 400 without them.
func minimaxCodeMessagesURL(cfg *providers.ProviderConfig) string {
	if cfg == nil {
		return ""
	}
	if target := cfg.StaticHeaders["x-relay-target"]; target != "" {
		if strings.Contains(target, "://") {
			return strings.TrimRight(cfg.BaseURL, "/") + minimaxCodeMessagesPath
		}
		return strings.TrimRight(cfg.BaseURL, "/") + cfg.StaticHeaders["x-relay-path"]
	}
	return cfg.BaseURL
}

// minimaxCodeMessagesPath is appended to an absolute relay target, which is
// only a host.
const minimaxCodeMessagesPath = "/v1/messages"

// minimaxCodeHeaders builds the outbound header set: the registry's static
// teammates (User-Agent, X-Mavis-Agent-Id, anthropic-version), the OAuth
// bearer, the placeholder x-api-key that rides every mavis request beside it,
// and the per-request session/timezone headers the gateway expects.
func minimaxCodeHeaders(req *Request) map[string]string {
	_, offsetSeconds := time.Now().Zone()
	headers := map[string]string{
		"Content-Type": "application/json",
		// The placeholder the gateway requires beside the real bearer.
		"x-api-key":               "sk-xxx",
		"X-Mavis-Timezone-Offset": strconv.Itoa(-offsetSeconds),
		"X-Mavis-Session-Id":      miniMaxCodeSessionID(req),
	}
	if req.IsStream {
		headers["Accept"] = "text/event-stream"
	}
	for k, v := range req.Config.StaticHeaders {
		if v == "" {
			continue
		}
		headers[k] = v
	}
	headers["Authorization"] = "Bearer " + req.APIKey
	return headers
}

// miniMaxCodeSessionID keeps one conversation id per client session, so a
// multi-turn chat stays a single session upstream instead of being reshaped
// into a fresh one on every request.
func miniMaxCodeSessionID(req *Request) string {
	if sid := strings.TrimSpace(req.SessionID); sid != "" {
		return sid
	}
	return randomSessionID()
}

// randomSessionID is the identity for a request that arrived without a client
// session. The value carries no credential — the gateway only checks its shape
// — so the entropy-source fallback below is not a leak.
func randomSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "9router-go-session"
	}
	return hex.EncodeToString(b[:])
}
