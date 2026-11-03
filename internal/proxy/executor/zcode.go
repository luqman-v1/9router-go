package executor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/proxy"
)

//go:embed zcode_identity_system.json
var zcodeIdentityAssetRaw []byte

// zcodeIdentityAsset holds the fields extracted from zcode_identity_system.json.
type zcodeIdentityAsset struct {
	CliPrefix        string `json:"cliPrefix"`
	StablePrefix     string `json:"stablePrefix"`
	AnthropicVersion string `json:"anthropicVersion"`
	AnthropicBeta    string `json:"anthropicBeta"`
	SDKUserAgent     string `json:"sdkUserAgent"`
}

// zcodeIdentity is the decoded asset plus the reason it could not be decoded.
type zcodeIdentity struct {
	asset zcodeIdentityAsset
	err   error
}

// The asset is decoded once, on first use, instead of in init(): a failure
// here must not take the whole process down (AGENTS.md §4.E, Zero Panic). The
// embedded file is still validated at build time by
// TestZcodeIdentityAssetParses, so a malformed asset fails CI rather than a
// running gateway, and the failure a request sees is an explicit error.
var zcodeIdentityOnce = sync.OnceValue(func() zcodeIdentity {
	var id zcodeIdentity
	if err := json.Unmarshal(zcodeIdentityAssetRaw, &id.asset); err != nil {
		id.err = fmt.Errorf("executor.zcodeIdentity: parse embedded zcode_identity_system.json: %w", err)
	}
	return id
})

// zcodeIdentityValues returns the decoded identity asset. A decode failure
// yields the zero value; the request path checks zcodeIdentityError first.
func zcodeIdentityValues() zcodeIdentityAsset {
	return zcodeIdentityOnce().asset
}

// zcodeIdentityError reports why the embedded identity asset could not be
// decoded, or nil when it is usable.
func zcodeIdentityError() error {
	return zcodeIdentityOnce().err
}

const (
	zcodeAppVersion       = "3.16.3"
	zcodeReleaseChannel   = "production"
	zcodeSessionPrefix    = "session_"
	zcodeDefaultMaxTokens = 8192
)

func zcodeEnvOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func zcodeAppVer() string {
	return zcodeEnvOr("ZCODE_IDENTITY_APP_VERSION", zcodeAppVersion)
}

func zcodeOSCategory() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

func zcodePlatform() string {
	return zcodeEnvOr("ZCODE_IDENTITY_PLATFORM", runtime.GOOS+"-"+runtime.GOARCH)
}

func zcodeSDKUserAgent() string {
	if ua := zcodeIdentityValues().SDKUserAgent; ua != "" {
		return ua
	}
	return "ZCode/" + zcodeAppVer() + " runtime/go"
}

func zcodeRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	s := make([]byte, n*2)
	const hx = "0123456789abcdef"
	for i, byt := range b {
		s[i*2] = hx[byt>>4]
		s[i*2+1] = hx[byt&0xf]
	}
	return string(s)
}

func zcodeUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// zcodeSessionID returns a stable session_<32hex> from conversationKey via HMAC.
// The seed comes from the connection's stored sessionSeed when present, so two
// connections never share a session namespace (the standalone bridge keys the
// same HMAC with a per-account UUID).
func zcodeSessionID(conversationKey, seed string) string {
	if conversationKey == "" {
		return zcodeSessionPrefix + zcodeRandomHex(16)
	}
	seed = strings.TrimSpace(seed)
	if seed == "" {
		seed = zcodeEnvOr("ZCODE_SESSION_SEED", "9router-zcode")
	}
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte{0})
	mac.Write([]byte(conversationKey))
	sum := mac.Sum(nil)
	return zcodeSessionPrefix + fmt.Sprintf("%x", sum)[:32]
}

// zcodeLLMHeaders builds the full identity header bundle the start-plan gateway
// inspects. Mirrors identity.js:buildLlmHeaders (g6n face).
func zcodeLLMHeaders(sessionID, requestID, traceID, queryID string) map[string]string {
	asset := zcodeIdentityValues()
	h := map[string]string{
		"http-referer":         "https://zcode.z.ai",
		"user-agent":           zcodeSDKUserAgent(),
		"x-title":              "Z Code@electron",
		"x-release-channel":    zcodeEnvOr("ZCODE_IDENTITY_RELEASE_CHANNEL", zcodeReleaseChannel),
		"x-client-language":    zcodeEnvOr("ZCODE_IDENTITY_LANGUAGE", "en"),
		"x-client-timezone":    zcodeEnvOr("ZCODE_IDENTITY_TIMEZONE", "UTC"),
		"x-zcode-agent":        "glm",
		"x-platform":           zcodePlatform(),
		"x-os-category":        zcodeOSCategory(),
		"x-os-version":         zcodeEnvOr("ZCODE_IDENTITY_OS_VERSION", "unknown"),
		"x-zcode-app-version":  zcodeAppVer(),
		"anthropic-version":    asset.AnthropicVersion,
		"anthropic-beta":       asset.AnthropicBeta,
		"x-zcode-session-type": "main",
		"x-zcode-query-source": "main_turn",
	}
	if sessionID != "" {
		h["x-session-id"] = sessionID
	}
	if requestID != "" {
		h["x-request-id"] = requestID
	}
	if traceID != "" {
		h["x-zcode-trace-id"] = traceID
	}
	if queryID != "" {
		h["x-query-id"] = queryID
	}
	return h
}

// zcodeNormalizeSystem collapses the client system field (string or array) into
// a plain string. Port of system-prompt.js:normalizeClientSystem.
func zcodeNormalizeSystem(system any) string {
	if system == nil {
		return ""
	}
	switch v := system.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, item := range v {
			switch iv := item.(type) {
			case string:
				if s := strings.TrimSpace(iv); s != "" {
					parts = append(parts, s)
				}
			case map[string]any:
				if t, ok := iv["text"].(string); ok {
					if s := strings.TrimSpace(t); s != "" {
						parts = append(parts, s)
					}
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n\n"))
	}
	return ""
}

// zcodeSystemBlocks builds the mandatory 3-block system the gateway
// content-inspects. Without the exact cliPrefix and stablePrefix, the gateway
// returns 405 / code 3012 ("unusual activity").
//
// Port of system-prompt.js:buildSystem.
func zcodeSystemBlocks(clientSystem any) []any {
	dynamic := zcodeNormalizeSystem(clientSystem)
	dynamicText := ""
	if dynamic != "" {
		dynamicText = "\n\n" + dynamic
	}
	asset := zcodeIdentityValues()
	return []any{
		map[string]any{
			"type": "text",
			"text": asset.CliPrefix,
		},
		map[string]any{
			"type":          "text",
			"text":          asset.StablePrefix,
			"cache_control": map[string]any{"type": "ephemeral"},
		},
		map[string]any{
			"type":          "text",
			"text":          dynamicText,
			"cache_control": map[string]any{"type": "ephemeral"},
		},
	}
}

// zcodeMetadataUserID builds the JSON metadata.user_id string.
// Port of system-prompt.js:metadataUserId.
func zcodeMetadataUserID(deviceID, sessionID, accountUUID string) string {
	b, _ := json.Marshal(map[string]string{
		"device_id":    deviceID,
		"account_uuid": accountUUID,
		"session_id":   sessionID,
	})
	return string(b)
}

// zcodeShapePlanBody rewrites the request body for the start-plan endpoint:
// injects identity system blocks and metadata.user_id, sets max_tokens default.
// Port of transport.js:shapePlanBody.
func zcodeShapePlanBody(body []byte, sessionID, deviceID, accountUUID string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	m["system"] = zcodeSystemBlocks(m["system"])

	meta, _ := m["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["user_id"] = zcodeMetadataUserID(deviceID, sessionID, accountUUID)
	m["metadata"] = meta

	if m["max_tokens"] == nil {
		m["max_tokens"] = zcodeDefaultMaxTokens
	}
	return json.Marshal(m)
}

// zcodeResolveJWT returns the plan JWT for the request. The dashboard stores it
// as the connection's data.apiKey, which the dispatcher passes as req.APIKey
// (same lane as every other oauth executor); ConnData carries the PSD copy and
// ZCODE_PLAN_JWT is the manual escape hatch.
func zcodeResolveJWT(apiKey string, connData map[string]any) string {
	if s := strings.TrimSpace(apiKey); s != "" {
		return s
	}
	if connData != nil {
		for _, field := range []string{"apiKey", "accessToken"} {
			if v, ok := connData[field].(string); ok {
				if s := strings.TrimSpace(v); s != "" {
					return s
				}
			}
		}
	}
	return strings.TrimSpace(os.Getenv("ZCODE_PLAN_JWT"))
}

func zcodeDeviceID(connData map[string]any) string {
	if connData != nil {
		if id, ok := connData["deviceId"].(string); ok && id != "" {
			return id
		}
	}
	return zcodeUUID()
}

// zcodeAccountUUID reads the z.ai user id for metadata.user_id. The dashboard
// stores it in providerSpecificData (seedConnIdentity), but a hand-written
// connection may carry it top level, so accept both.
func zcodeAccountUUID(connData map[string]any) string {
	return zcodeConnString(connData, "accountUUID")
}

// zcodeConnString reads one string field from providerSpecificData, falling
// back to the top-level copy. The dispatcher hands the executor the PSD map
// (ChatHandler.executeRequest), so both shapes have to resolve.
func zcodeConnString(connData map[string]any, key string) string {
	if connData == nil {
		return ""
	}
	if v, ok := connData[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if psd, ok := connData["providerSpecificData"].(map[string]any); ok {
		if v, ok := psd[key].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ForwardZcode is the executor for the "zcode" provider.
//
// The ZCode start-plan gateway requires:
//   - Auth: plan JWT (device-flow OAuth), stored in connection.apiKey.
//   - Body: Anthropic Messages format + mandatory 3-block identity system.
//   - Headers: full identity bundle (session/request/trace IDs, OS/platform).
//   - Response: Claude Messages SSE or JSON.
//
// Connection data schema (providerConnections.data JSON):
//
//	{
//	  "apiKey":       "<plan JWT>",
//	  "refreshToken": "<device-flow refresh token>",  // optional, for auto-refresh
//	  "deviceId":     "<stable UUID>",                // optional, defaults to random
//	  "accountUUID":  "<z.ai user_id>",               // optional, for metadata
//	}
func ForwardZcode(w http.ResponseWriter, req *Request) error {
	// The identity asset drives every header and system block. If it did not
	// decode, the request would reach the gateway with empty cliPrefix and
	// stablePrefix and come back as an opaque 3012; fail here with the reason
	// instead (§4.E: surface an error, never panic in proxy execution).
	if err := zcodeIdentityError(); err != nil {
		return err
	}
	jwt := zcodeResolveJWT(req.APIKey, req.ConnData)
	if jwt == "" {
		return &zcodeNoJWTError{}
	}
	deviceID := zcodeDeviceID(req.ConnData)
	accountUUID := zcodeAccountUUID(req.ConnData)
	sessionID := zcodeSessionID(req.SessionID, zcodeConnString(req.ConnData, "sessionSeed"))
	requestID := zcodeUUID()

	shapedBody, err := zcodeShapePlanBody(req.Body, sessionID, deviceID, accountUUID)
	if err != nil {
		return fmt.Errorf("zcode: %w", err)
	}

	resp, err := zcodePostWithRetry(req, jwt, shapedBody, sessionID, requestID)
	if err != nil {
		return fmt.Errorf("zcode: upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The plan gateway reports drain conditions (1005 "exceed quota limit",
	// 1113 grant spent, 3012 identity gate) as a JSON envelope with HTTP 200.
	// Handing that to the Claude translator turns a rotate-worthy fault into a
	// generic "no completion" 502, so surface it as an upstream error first.
	if envBody, code, ok := zcodeEnvelopeError(resp); ok {
		return &proxy.UpstreamError{StatusCode: code, Body: envBody, Header: resp.Header}
	} else if envBody != nil {
		resp.Body = io.NopCloser(bytes.NewReader(envBody))
	}

	forwardUpstreamResponseHeaders(w, resp.Header)

	var stall io.ReadCloser
	if req.IsStream {
		stall = proxy.NewStallReaderWithContext(req.Ctx, io.NopCloser(resp.Body), 0, "zcode")
		defer func() { _ = stall.Close() }()
	}

	// The upstream answers in Claude Messages on every lane, so the reply
	// always flows through handleClaudeMessages*: a /v1/messages client
	// (TranslateResp=true) gets the frames passed through, an OpenAI
	// /v1/chat/completions client (TranslateResp=false) gets each event
	// rewritten into chat.completion chunks — the same two-way translation
	// the standalone zcode bridge does in server.js:pipeStream.
	if req.IsStream {
		return handleClaudeMessagesStream(w, req, stall)
	}
	return handleClaudeMessagesNonStream(w, req, resp.Body)
}

// zcodeDoRequest sends one shaped Anthropic-format request to the plan endpoint.
func zcodeDoRequest(ctx context.Context, client *http.Client, baseURL, jwt string, body []byte, isStream bool, sessionID, requestID string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(requestID) < 8 {
		requestID = zcodeUUID()
	}
	traceID := zcodeUUID()
	// The desktop client's query id shares its first four characters with the
	// request id ("01a1…" is the epoch/counter prefix it emits). Mirroring the
	// shape keeps the bundle internally consistent for the gateway's identity
	// checks; the value itself is opaque.
	queryID := "01a1" + requestID[4:]

	headers := zcodeLLMHeaders(sessionID, requestID, traceID, queryID)
	headers["authorization"] = "Bearer " + jwt
	headers["content-type"] = "application/json"
	if isStream {
		headers["accept"] = "text/event-stream"
	} else {
		headers["accept"] = "application/json"
	}
	return proxy.DoRequest(ctx, client, "POST", baseURL, headers, body)
}

// zcodeNoJWTError is returned when no plan JWT is available.
type zcodeNoJWTError struct{}

func (e *zcodeNoJWTError) Error() string {
	return "no ZCode plan JWT available — add a zcode connection via the dashboard (Login button) or set ZCODE_PLAN_JWT"
}

// zcodeConcurrencyRetries bounds the in-place retries for the gateway's
// model-concurrency answer (3009). The standalone bridge treats that code as
// transient: another account cannot help, because the slot is per model, and
// switching connections only burns the fallback chain.
const zcodeConcurrencyRetries = 3

// zcodePostWithRetry sends the shaped request, retrying 3009 in place with a
// short backoff. Any other status is returned untouched so the caller's
// account rotation and error classification keep working.
func zcodePostWithRetry(req *Request, jwt string, body []byte, sessionID, requestID string) (*http.Response, error) {
	var lastResp *http.Response
	for attempt := 0; attempt < zcodeConcurrencyRetries; attempt++ {
		resp, err := zcodeDoRequest(req.Ctx, req.Client, req.Config.BaseURL, jwt, body, req.IsStream, sessionID, requestID)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		// The body has to be read to look at the envelope, and it cannot be
		// replayed: buffer it so the final attempt (or a non-3009 answer) can
		// still be handed back intact.
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return resp, nil
		}
		if !zcodeIsConcurrencyEnvelope(raw) {
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			return resp, nil
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		lastResp = resp
		if attempt == zcodeConcurrencyRetries-1 {
			break
		}
		wait := time.Duration(1<<attempt) * time.Second
		select {
		case <-req.Ctx.Done():
			return resp, nil
		case <-time.After(wait):
		}
	}
	return lastResp, nil
}

// zcodeIsConcurrencyEnvelope reports whether a 429 body carries the gateway's
// model-concurrency code (3009). 3008 (user concurrency) is account-wide: the
// desktop client holds the slot, so the same connection cannot answer and the
// caller has to rotate instead of waiting.
func zcodeIsConcurrencyEnvelope(body []byte) bool {
	var env struct {
		Code float64 `json:"code"`
	}
	if json.Unmarshal(body, &env) != nil {
		return false
	}
	return env.Code == 3009
}

// zcodeEnvelopeError inspects a 200 response for the gateway's business-error
// envelope ({code,msg,logid}) and maps it onto the HTTP status the rest of the
// pipeline classifies: a drained grant or a rejected identity is a credential
// fault (429/403 → rotate + cooldown), not a successful empty completion.
//
// Returns the buffered body (so a non-error response can be replayed), the
// status to report, and whether an error was found.
func zcodeEnvelopeError(resp *http.Response) ([]byte, int, bool) {
	if resp == nil {
		return nil, 0, false
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return body, resp.StatusCode, resp.StatusCode >= 400
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, false
	}
	var env struct {
		Code float64 `json:"code"`
		Msg  string  `json:"msg"`
	}
	if json.Unmarshal(body, &env) != nil || env.Code == 0 {
		return body, resp.StatusCode, false
	}
	status := http.StatusForbidden
	switch env.Code {
	case 1005, 1113: // quota / grant spent
		status = http.StatusTooManyRequests
	case 3009: // model concurrency: another request holds the model slot
		status = http.StatusTooManyRequests
	case 3008: // account concurrency: the desktop client holds the slot
		status = http.StatusTooManyRequests
	case 3012:
		// Identity gate. Reported as 403 rather than the 405 the gateway
		// actually sends: 403 is already account-scoped in RetryableStatusCodes,
		// so the connection cools down and rotation moves on without teaching
		// every other provider that 405 can mean a credential fault.
		status = http.StatusForbidden
	}
	return body, status, true
}
