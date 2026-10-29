package executor

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

// chat.deepseek.com's web client API, ported from OmniRoute
// open-sse/executors/deepseek-web.ts. It speaks the same wire protocol the
// official api.deepseek.com serves, reached with the credential a browser login
// leaves behind instead of an API key:
//
//	userToken --GET /users/current--> short-lived Bearer access token
//	          --POST /chat_session/create--> chat session id
//	          --POST /chat/create_pow_challenge--> solve--> completion
//
// The "provider" is the web site rather than an account on an API plan, so the
// credential is a browser-login token and the quota is the site's own.

const (
	// deepSeekWebBase is the site this credential belongs to. Every sibling
	// endpoint is derived from the connection's configured base URL instead, so
	// this is only the fallback for a turn with no configuration at all.
	deepSeekWebBase = "https://chat.deepseek.com"

	// deepSeekCompletionPath is the completion endpoint, and also the value the
	// proof-of-work challenge is requested for, so the two are defined once.
	deepSeekCompletionPath = "/api/v0/chat/completion"

	// deepSeekUserAgent is defined with the rest of the browser fingerprint in
	// deepseek_web_stealth.go, so the UA and the client-hint headers cannot
	// drift apart.

	// deepSeekAccessTokenTTL mirrors upstream's 3600s access-token cache.
	deepSeekAccessTokenTTL = time.Hour

	// deepSeekPoWTimeout bounds the nonce search. Upstream defaults to 10s and
	// caps at 30s; a gateway request that spends longer in pure computation
	// than a model turn does is a failure the caller should see.
	deepSeekPoWTimeout = 30 * time.Second

	// deepSeekMaxBodyBytes caps any non-streamed upstream response.
	deepSeekMaxBodyBytes = constants.MaxUpstreamBodyBytes

	// deepSeekPowAlgorithm is the only proof-of-work algorithm implemented here.
	deepSeekPowAlgorithm = "DeepSeekHashV1"
)

// deepSeekTurn is the per-request state for one completion: where this
// connection's endpoints are, which client dials them, and how closely the
// request imitates the site's own client.
type deepSeekTurn struct {
	client    *http.Client
	endpoints deepSeekEndpoints
	origin    string
	stealth   deepSeekStealth

	// transport is the browser-fidelity stack. It is nil when the turn goes
	// over plain net/http instead — which is what the test suite runs
	// against, and what a connection that opts out of the stack runs against.
	transport *deepSeekTransport

	// cookie is the site's own session cookie, as the operator configured it.
	cookie string

	// deviceID is the id the site knows this client by. The site generates
	// it per browser and keeps it, and it is what the settings calls carry.
	deviceID string
}

// newDeepSeekTurn resolves the endpoints for one connection's configured base
// URL. A nil client falls back to the default, so the executor is usable
// without one in tests and in the paths that pass nil.
func newDeepSeekTurn(client *http.Client, baseURL string, stealth deepSeekStealth) deepSeekTurn {
	if client == nil {
		client = http.DefaultClient
	}
	return deepSeekTurn{
		client:    client,
		endpoints: deepSeekEndpointsFor(baseURL),
		origin:    deepSeekOrigin(baseURL),
		stealth:   stealth,
	}
}

// withBrowserFingerprint attaches the transport and the credential the turn
// dials the site with.
//
// The cookie is the site's own, not a fabricated one: the WAF reads it, and a
// cookie the site never issued is a request the site does not recognise. A
// turn with no cookie still works — the API's credential is the Bearer token
// — but the browser's own request carries one, so the browser's own request
// shape includes one.
func (t deepSeekTurn) withBrowserFingerprint(cookie, deviceID string) deepSeekTurn {
	t.cookie = cookie
	t.deviceID = deviceID
	if deepSeekWebPlainHTTP() {
		return t
	}
	transport, err := newDeepSeekTransport(t.client, t.origin, cookie)
	if err != nil {
		// The stack is defence in depth: the API endpoints answer the same
		// over Go's own transport. A failure to build it is logged and the
		// turn continues over plain net/http rather than failing outright.
		log.Warn("deepseek-web", "could not build the browser transport, using plain HTTP", "error", err)
		return t
	}
	t.transport = transport
	return t
}

// webHeaders is the fingerprint a chat call carries.
//
// The chat calls — the token exchange, the proof-of-work challenge, the
// completion — are issued below the app's request interceptor, so they carry
// no `x-client-*` block. See deepseek_web_headers.go for the capture this
// comes from.
//
// With stealth off it is the minimal set this provider shipped with, so the
// two modes are a real comparison rather than a different flavour of the
// same thing.
//
// Accept-Encoding is deliberately absent from the plain set: Go's transport
// manages compression itself, and setting the header suppresses transparent
// gzip, leaving every response to be decoded by hand.
func (t deepSeekTurn) webHeaders(extra map[string]string) map[string]string {
	var headers map[string]string
	if t.stealth.enabled {
		headers = deepSeekBrowserHeaders(t.origin, extra)
	} else {
		headers = deepSeekPlainHeaders(t.origin, extra)
	}
	return t.withCookie(headers)
}

// withCookie puts the site's own cookie on a header set.
//
// On the browser stack the cookie jar writes the cookie, and the stack
// deletes a hand-written one so the site's current value is the only one on
// the wire. Over plain net/http there is no jar, so the configured cookie is
// written here instead.
func (t deepSeekTurn) withCookie(headers map[string]string) map[string]string {
	if t.transport == nil && t.cookie != "" {
		if _, ok := headers["cookie"]; !ok {
			headers["cookie"] = t.cookie
		}
	}
	return headers
}

// deepSeekTokenCache memoizes userToken -> access token. Upstream keys the same
// pair and expires the entry after an hour; a token is also dropped on any auth
// failure so a rejected credential is never handed out twice.
type deepSeekTokenCache struct {
	mu    sync.RWMutex
	items map[string]deepSeekTokenEntry
}

type deepSeekTokenEntry struct {
	accessToken string
	expiresAt   time.Time
}

// maxDeepSeekCachedTokens bounds the cache the way upstream's CACHE_MAX_SIZE
// does, so a burst of one-off credentials cannot grow it without limit.
const maxDeepSeekCachedTokens = 100

var deepSeekTokens = &deepSeekTokenCache{items: make(map[string]deepSeekTokenEntry)}

// get returns a cached, unexpired access token.
func (c *deepSeekTokenCache) get(userToken string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[userToken]
	if !ok || !time.Now().Before(entry.expiresAt) {
		return "", false
	}
	return entry.accessToken, true
}

// put stores an access token for one TTL, evicting the oldest entry first so a
// bounded eviction never costs the caller a round trip.
func (c *deepSeekTokenCache) put(userToken, accessToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[userToken]; !exists && len(c.items) >= maxDeepSeekCachedTokens {
		c.evictOldestLocked()
	}
	c.items[userToken] = deepSeekTokenEntry{
		accessToken: accessToken,
		expiresAt:   time.Now().Add(deepSeekAccessTokenTTL),
	}
}

// drop removes one cached token. Called on an auth failure so the next attempt
// re-exchanges the userToken instead of replaying a credential the upstream has
// already rejected.
func (c *deepSeekTokenCache) drop(userToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, userToken)
}

func (c *deepSeekTokenCache) evictOldestLocked() {
	var oldestKey string
	var oldestExpiry time.Time
	for key, entry := range c.items {
		if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
			oldestKey, oldestExpiry = key, entry.expiresAt
		}
	}
	if oldestKey != "" {
		delete(c.items, oldestKey)
	}
}

// deepSeekEnvelope is chat.deepseek.com's response shape. Every call answers
// {"code":0,"data":{...}} on success and {"code":<int>,"msg":"..."} on an
// application-level failure — including an auth failure, which this API reports
// as HTTP 200 with code 40003 rather than as a 401.
type deepSeekEnvelope struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Data jsontext.Value `json:"data"`
}

// deepSeekBizData is the payload once unwrapped from the envelope.
type deepSeekBizData struct {
	Token       string               `json:"token"`
	ChatSession deepSeekChatSession  `json:"chat_session"`
	Challenge   deepSeekPowChallenge `json:"challenge"`
}

type deepSeekChatSession struct {
	ID string `json:"id"`
}

type deepSeekPowChallenge struct {
	Algorithm  string `json:"algorithm"`
	Challenge  string `json:"challenge"`
	Salt       string `json:"salt"`
	Difficulty int    `json:"difficulty"`
	ExpireAt   int64  `json:"expire_at"`
	Signature  string `json:"signature"`
	TargetPath string `json:"target_path"`
}

// deepSeekAPIError is a DeepSeek application-level failure. It is the only way
// this upstream reports auth and rate limiting — both arrive as HTTP 200 with a
// code in the body — so the code is carried out to the status the router acts
// on, rather than collapsing into a bad gateway.
type deepSeekAPIError struct {
	Code    int
	Message string
}

func (e *deepSeekAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("deepseek-web: error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("deepseek-web: error %d", e.Code)
}

const (
	// deepSeekCodeAuthFailed is the "Authorization Failed" envelope the site
	// answers a rejected credential with.
	deepSeekCodeAuthFailed = 40003

	// deepSeekCodeMissingToken is the "Missing Token" envelope: the request
	// carried no credential at all.
	//
	// This code used to be read as a rate limit. It is not: the site's own
	// bundle names 40002 MISSING_TOKEN and 40003 INVALID_TOKEN, and a
	// request that carried no Bearer token was answered with 40002 — which
	// the old mapping turned into a 429, so a broken credential was cooled
	// down and retried against the same account instead of being reported.
	deepSeekCodeMissingToken = 40002

	// deepSeekCodeUserBanned is the "user is banned" envelope: the account
	// itself is refused. It is not a credential problem, and no amount of
	// re-authenticating helps.
	deepSeekCodeUserBanned = 40012

	// deepSeekCodeIPRestricted is the "IP access restricted" envelope: the
	// account is fine and the address is not. Another egress reaches it.
	deepSeekCodeIPRestricted = 40029

	// deepSeekCodeMuted is the account-mute envelope.
	deepSeekCodeMuted = 50006

	// deepSeekCodeRequestEncrypted is the "request decrypt failed" envelope.
	deepSeekCodeRequestEncrypted = 40006
)

// deepSeekUserToken normalizes a pasted credential. DeepSeek stores its token
// in localStorage as {"value":"..."}, so a straight copy out of DevTools arrives
// JSON-wrapped; upstream unwraps it and the dashboard probe must too, or the
// probe and the executor would disagree about what is valid.
func deepSeekUserToken(raw string) string {
	token := strings.TrimSpace(raw)
	if token == "" {
		return ""
	}
	var wrapper struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(token), &wrapper); err == nil && wrapper.Value != "" {
		return strings.TrimSpace(wrapper.Value)
	}
	return token
}

// DeepSeekUserToken is the exported form of deepSeekUserToken, for the
// dashboard probe. Both have to unwrap the same shapes: a probe that accepts a
// credential the executor then rejects — or the reverse — is a credential the
// user cannot get working.
func DeepSeekUserToken(raw string) string { return deepSeekUserToken(raw) }

// do sends one request to the chat.deepseek.com API and unwraps the envelope.
//
// Transport-level retries belong to the shared proxy layer, so this performs
// exactly one attempt and classifies the outcome itself.
func (t deepSeekTurn) do(ctx context.Context, method, url, bearer string, headers map[string]string, payload any) (jsontext.Value, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("deepseek-web: marshal %s request: %w", url, err)
		}
		body = encoded
	}

	allHeaders := t.webHeaders(headers)
	if bearer != "" {
		allHeaders["authorization"] = "Bearer " + bearer
	}
	// The site marks a POST as cross-origin and leaves a GET unmarked: the
	// capture shows `origin` on the POSTs and not on the GETs. A GET that
	// carried one would be a request the site never made.
	if method == http.MethodPost {
		allHeaders["origin"] = t.origin
	} else {
		delete(allHeaders, "origin")
	}

	resp, err := t.send(ctx, method, url, body, allHeaders)
	if err != nil {
		return nil, fmt.Errorf("deepseek-web: %s: %w", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, deepSeekMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("deepseek-web: read %s: %w", url, err)
	}

	var envelope deepSeekEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("deepseek-web: %s returned an unparseable body (HTTP %d): %w", url, resp.StatusCode, err)
	}
	if envelope.Code != 0 {
		return nil, &deepSeekAPIError{Code: envelope.Code, Message: envelope.Msg}
	}
	return envelope.Data, nil
}

// send issues one request and classifies a non-200 answer the way the shared
// proxy layer does.
//
// The browser stack is used when there is one; otherwise the request goes
// over Go's own transport, which the shared layer drives with its retry and
// proxy-fallback rules.
func (t deepSeekTurn) send(ctx context.Context, method, url string, body []byte, headers map[string]string) (*http.Response, error) {
	if t.transport == nil {
		return proxy.DoRequest(ctx, t.client, method, url, headers, body)
	}
	response, err := t.transport.send(ctx, method, url, body, headers)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK {
		return response, nil
	}
	// A non-200 is read here, because the body is the part the caller
	// classifies: the site's refusals arrive inside it.
	errBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	return nil, &proxy.UpstreamError{
		StatusCode: response.StatusCode,
		Body:       errBody,
		Header:     response.Header,
		ReadErr:    readErr,
	}
}

// bizDataOf unwraps the data envelope's biz_data field, which this API nests
// one level deeper than a plain payload.
func deepSeekBizDataOf(data jsontext.Value) (*deepSeekBizData, error) {
	var holder struct {
		BizData *deepSeekBizData `json:"biz_data"`
	}
	if err := json.Unmarshal(data, &holder); err != nil {
		return nil, fmt.Errorf("deepseek-web: unwrap biz_data: %w", err)
	}
	if holder.BizData != nil {
		return holder.BizData, nil
	}
	// Some responses carry the fields directly under data.
	var direct deepSeekBizData
	if err := json.Unmarshal(data, &direct); err != nil {
		return nil, fmt.Errorf("deepseek-web: unwrap data: %w", err)
	}
	return &direct, nil
}

// acquireAccessToken exchanges a browser-login userToken for the short-lived
// Bearer token the API calls authenticate with, memoized for an hour.
func (t deepSeekTurn) acquireAccessToken(ctx context.Context, userToken string) (string, error) {
	if cached, ok := deepSeekTokens.get(userToken); ok {
		return cached, nil
	}

	data, err := t.do(ctx, http.MethodGet, t.endpoints.usersCurrent, userToken, nil, nil)
	if err != nil {
		if isDeepSeekAuthError(err) {
			deepSeekTokens.drop(userToken)
			return "", fmt.Errorf("deepseek-web: userToken is invalid or expired — get a fresh one from chat.deepseek.com localStorage: %w", err)
		}
		return "", err
	}

	biz, err := deepSeekBizDataOf(data)
	if err != nil {
		return "", err
	}
	if biz.Token == "" {
		return "", fmt.Errorf("deepseek-web: users/current returned no access token")
	}

	deepSeekTokens.put(userToken, biz.Token)
	log.Debug("deepseek-web", "access token acquired", "chars", len(biz.Token))
	return biz.Token, nil
}

// createSession opens an upstream chat session. The web UI exposes no
// conversation id a client can thread across turns on its own, so one session
// serves one request.
func (t deepSeekTurn) createSession(ctx context.Context, accessToken string) (string, error) {
	data, err := t.do(ctx, http.MethodPost, t.endpoints.sessionCreate, accessToken, nil, map[string]any{})
	if err != nil {
		return "", err
	}
	biz, err := deepSeekBizDataOf(data)
	if err != nil {
		return "", err
	}
	if biz.ChatSession.ID == "" {
		return "", fmt.Errorf("deepseek-web: chat_session/create returned no session id")
	}
	return biz.ChatSession.ID, nil
}

// deleteSession closes an upstream chat session. Best effort by design: a
// session left open is invisible to the user, so a failure here must not fail a
// turn that already produced an answer.
//
// The Referer names the conversation's own page, which is the page a person
// deletes a conversation from.
func (t deepSeekTurn) deleteSession(ctx context.Context, accessToken, sessionID string) {
	if sessionID == "" {
		return
	}
	payload := map[string]any{"chat_session_id": sessionID}
	if _, err := t.do(ctx, http.MethodPost, t.endpoints.sessionDelete, accessToken, map[string]string{
		"referer": deepSeekChatPageURL(t.origin, sessionID),
	}, payload); err != nil {
		log.Debug("deepseek-web", "session cleanup failed", "error", err)
	}
}

// attestationHeaders returns the client-attestation headers for a completion
// call, or an empty map when stealth is off.
//
// The values are fetched through the turn itself rather than a bare client,
// so they are collected over the same browser-fidelity transport the
// completion will ride: the attestation hosts are part of the site's
// anti-bot pipeline, and a value a Go TLS handshake collected is a value
// that contradicts the Chrome handshake that presents it.
//
// The fetch is best effort by design: a cached value is used until its TTL
// passes, a failed refresh falls back to the stale one, and a turn with no
// value at all still goes out. Nothing about an attestation host being
// unreachable should cost the caller an answer.
func (t deepSeekTurn) attestationHeaders(ctx context.Context) map[string]string {
	if !t.stealth.enabled {
		return nil
	}
	return deepSeekHif.headers(ctx, t)
}

// deepSeekPowResponse is the proof-of-work answer, base64-encoded into the
// X-Ds-Pow-Response header of the completion call.
type deepSeekPowResponse struct {
	Algorithm  string `json:"algorithm"`
	Challenge  string `json:"challenge"`
	Salt       string `json:"salt"`
	Answer     int    `json:"answer"`
	Signature  string `json:"signature"`
	TargetPath string `json:"target_path"`
}

// solveDeepSeekPow answers one challenge, bounded by deepSeekPoWTimeout so a
// pathological difficulty cannot pin a request handler indefinitely.
func solveDeepSeekPow(ctx context.Context, challenge deepSeekPowChallenge) (string, error) {
	if challenge.Algorithm != deepSeekPowAlgorithm {
		return "", fmt.Errorf("deepseek-web: unsupported pow algorithm %q", challenge.Algorithm)
	}

	prefix := challenge.Salt + "_" + strconv.FormatInt(challenge.ExpireAt, 10) + "_"
	nonce, err := findDeepSeekPowNonceWithContext(ctx, prefix, challenge.Challenge, challenge.Difficulty, deepSeekPoWTimeout)
	if err != nil {
		return "", err
	}
	if nonce < 0 {
		return "", fmt.Errorf("deepseek-web: no proof-of-work nonce within %d candidates", challenge.Difficulty)
	}

	encoded, err := json.Marshal(deepSeekPowResponse{
		Algorithm:  challenge.Algorithm,
		Challenge:  challenge.Challenge,
		Salt:       challenge.Salt,
		Answer:     nonce,
		Signature:  challenge.Signature,
		TargetPath: challenge.TargetPath,
	})
	if err != nil {
		return "", fmt.Errorf("deepseek-web: marshal pow response: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

// requestPow fetches and answers a fresh challenge for one completion attempt.
// The challenge is single-use upstream, so every attempt needs a new one.
//
// The challenge is requested for one conversation, and the browser requests
// it from that conversation's page, so the Referer names the page rather
// than the site root.
func (t deepSeekTurn) requestPow(ctx context.Context, accessToken, chatSessionID string) (string, error) {
	payload := map[string]any{"target_path": deepSeekCompletionPath}
	data, err := t.do(ctx, http.MethodPost, t.endpoints.powChallenge, accessToken, map[string]string{
		"referer": deepSeekChatPageURL(t.origin, chatSessionID),
	}, payload)
	if err != nil {
		return "", err
	}
	biz, err := deepSeekBizDataOf(data)
	if err != nil {
		return "", err
	}
	if biz.Challenge.Challenge == "" {
		return "", fmt.Errorf("deepseek-web: create_pow_challenge returned no challenge")
	}
	return solveDeepSeekPow(ctx, biz.Challenge)
}

// deepSeekCompletionRequest is the payload of one completion call.
//
// `parent_message_id` is an integer, not a string: it is the upstream's own
// message id, and the site threads a follow-up turn on it. Sending a string
// there is a protocol error, which is why the first turn of a conversation
// sends null — the field is a pointer so "absent" and "0" stay distinct.
type deepSeekCompletionRequest struct {
	ChatSessionID   string   `json:"chat_session_id"`
	ParentMessageID *int64   `json:"parent_message_id"`
	ModelType       string   `json:"model_type"`
	Prompt          string   `json:"prompt"`
	RefFileIDs      []string `json:"ref_file_ids"`
	ThinkingEnabled bool     `json:"thinking_enabled"`
	SearchEnabled   bool     `json:"search_enabled"`
	Stream          bool     `json:"stream"`
	Preempt         bool     `json:"preempt"`
}

// completion performs one completion attempt against a session and returns the
// still-open response body.
//
// Unlike do this hands back the response instead of unwrapping it: the
// completion endpoint streams, so holding an open body is the only way to avoid
// buffering a whole answer.
func (t deepSeekTurn) completion(ctx context.Context, accessToken string, request deepSeekCompletionRequest) (*http.Response, error) {
	powAnswer, err := t.requestPow(ctx, accessToken, request.ChatSessionID)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("deepseek-web: marshal completion: %w", err)
	}

	allHeaders := t.webHeaders(map[string]string{
		"x-ds-pow-response": powAnswer,
		// The completion is issued from the conversation's own page, and a
		// browser writes that page as the Referer on every fetch it makes
		// there.
		"referer": deepSeekChatPageURL(t.origin, request.ChatSessionID),
	})
	// The attestation headers belong to a completion call, not to the session
	// or challenge calls: that is where the browser attaches them, and sending
	// them everywhere would be its own kind of fingerprint.
	for name, value := range t.attestationHeaders(ctx) {
		allHeaders[name] = value
	}
	allHeaders["authorization"] = "Bearer " + accessToken
	// The completion is a POST, and the site marks its POSTs with the origin.
	allHeaders["origin"] = t.origin

	resp, err := t.send(ctx, http.MethodPost, t.endpoints.completion, payload, allHeaders)
	if err != nil {
		if status, ok := deepSeekUpstreamStatus(err); ok &&
			(status == http.StatusUnauthorized || status == http.StatusForbidden) {
			return nil, &deepSeekAPIError{
				Code:    deepSeekCodeAuthFailed,
				Message: "access token expired — re-paste your userToken from chat.deepseek.com localStorage",
			}
		}
		return nil, fmt.Errorf("deepseek-web: completion: %w", err)
	}
	return resp, nil
}

// isDeepSeekAuthError reports whether err is an authentication failure, whether
// it arrived as an HTTP status or as DeepSeek's 200-with-code-40003 envelope.
func isDeepSeekAuthError(err error) bool {
	var apiErr *deepSeekAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == deepSeekCodeAuthFailed
	}
	if status, ok := deepSeekUpstreamStatus(err); ok {
		return status == http.StatusUnauthorized || status == http.StatusForbidden
	}
	return false
}

// deepSeekUpstreamStatus extracts the HTTP status the shared proxy layer
// wrapped an upstream failure with.
func deepSeekUpstreamStatus(err error) (int, bool) {
	var upstream *proxy.UpstreamError
	if errors.As(err, &upstream) {
		return upstream.StatusCode, true
	}
	return 0, false
}
