package executor

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/oauth"
	"9router/proxy/internal/translator"
)

// Client identifiers captured from the installed AutoClaw desktop client
// (v2.6.1: KNc appId/appKey and app.getVersion()). They identify the client
// build; the app key is the constant used for the MD5 request signature.
const (
	autoclawAppID       = "100003"
	autoclawVersion     = "2.6.1"
	autoclawOAuthAppKey = "38d2391985e2369a5fb8227d8e6cd5e5"
	autoclawChatPath    = "/autoclaw-proxy/proxy/autoclaw/chat/completions"
	autoclawBaseURL     = "https://autoglm-api.autoglm.ai"
)

// now returns unix seconds; a test seam so the signer can be frozen.
var now = func() int64 { return time.Now().Unix() }

// autoclawExecutorBaseURL is the default; a connection's own data.baseUrl
// (req.Config.BaseURL) takes precedence, so a custom node is honoured.
var autoclawExecutorBaseURL = autoclawBaseURL

// autoclawBase resolves the upstream base for this request. The chat path
// accepts an explicit override (custom node), which must also be passed to
// the refresher so both calls hit the same host.
func autoclawBase(req *Request) string {
	if req != nil && req.Config != nil {
		if base := strings.TrimSpace(req.Config.BaseURL); base != "" {
			base = strings.TrimRight(base, "/")
			// The chat dispatcher normalises a connection's baseUrl to a
			// full chat-completions URL. AutoClaw appends its own paths, so
			// strip that suffix rather than producing a doubled path.
			base = strings.TrimSuffix(base, "/v1/chat/completions")
			return strings.TrimRight(base, "/")
		}
	}
	return autoclawExecutorBaseURL
}

// ForwardAutoclaw signs each request with the app's MD5 scheme, moves the
// model into X-Request-Model (the body's `model` field is ignored upstream),
// forces stream:true when the client asked for a stream, relays the SSE
// response chunk-by-chunk with a flush (so TTFB is the first upstream frame,
// not the stream duration), and folds the event stream into a single
// chat.completion for a non-stream client — the upstream protocol is
// SSE-only, so it answers stream:false with frames too. The access token is
// rotated from the durable refresh_token|device_id|app_key stored in the connection's
// apiKey field via oauth.Refresh, which is dedup-cached so a hot connection
// does not hit the token endpoint on every turn.
func ForwardAutoclaw(w http.ResponseWriter, req *Request) error {
	refreshToken, deviceID, appKey := splitAutoclawCredential(req.APIKey)
	if refreshToken == "" {
		return fmt.Errorf("autoclaw: missing refresh token: sign in with OAuth first")
	}
	// OAuth connections store refresh_token|device_id|app_key; the app key is
	// the desktop client's constant (captured from build 2.6.1). A legacy
	// credential may still supply its own third field, but an empty one now
	// falls back to the constant instead of rejecting the request.
	if appKey == "" {
		appKey = autoclawOAuthAppKey
	}

	result, err := oauth.Refresh(req.Ctx, &oauth.Params{
		Client:               req.Client,
		Provider:             "autoclaw",
		RefreshToken:         refreshToken,
		AccessToken:          "",
		ProviderSpecificData: map[string]string{"device_id": deviceID, "base_url": autoclawBase(req)},
	})
	if err != nil {
		return fmt.Errorf("autoclaw: rotate access token: %w", err)
	}
	accessToken := result.AccessToken
	// The identity gateway rotates refresh_token on every refresh. Persist the
	// new pair so the next request (after the dedup cache expires) does not
	if req.PersistCredentials != nil && result.RefreshToken != "" && result.RefreshToken != refreshToken && req.ConnectionID != "" {
		if err := req.PersistCredentials(req.Ctx, req.ConnectionID, result.AccessToken, result.RefreshToken); err != nil {
			return fmt.Errorf("autoclaw: persist rotated credentials: %w", err)
		}
	}

	body, err := autoclawBody(req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("autoclaw: prepare body: %w", err)
	}
	// The chat path leaves Request.ModelName unset; the body is the source.
	model := autoclawRequestedModel(req)
	if model == "" {
		return fmt.Errorf("autoclaw: request carries no model")
	}
	headers := autoclawHeaders(accessToken, req.IsStream, model, appKey)

	// SendOnce rather than DoRequest: the 401 branch below is this
	// executor's own single forced-refresh retry, so the generic transient
	// replay must not act on the status first, and the caller needs the
	// response (not a wrapped *UpstreamError) to branch on 401. DoRequest
	// converts every non-200 into an error, which made the 401 branch
	// unreachable dead code on the first PR head.
	resp, err := proxy.SendOnce(req.Ctx, req.Client, http.MethodPost, autoclawBase(req)+autoclawChatPath, headers, body)

	if err != nil {
		return fmt.Errorf("autoclaw: upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A 401 means the rotated access token was already stale (the background
	// refresher may not have ticked yet). Force one refresh and retry once
	// instead of surfacing a hard auth error.
	if resp.StatusCode == http.StatusUnauthorized {
		if err := resp.Body.Close(); err != nil {
			return fmt.Errorf("autoclaw: close response body: %w", err)
		}
		result, err = oauth.Refresh(req.Ctx, &oauth.Params{
			Client:               req.Client,
			Provider:             "autoclaw",
			RefreshToken:         refreshToken,
			AccessToken:          accessToken,
			Force:                true,
			ProviderSpecificData: map[string]string{"device_id": deviceID, "base_url": autoclawBase(req)},
		})
		if err != nil {
			return fmt.Errorf("autoclaw: force refresh after 401: %w", err)
		}
		headers = autoclawHeaders(result.AccessToken, req.IsStream, model, appKey)
		resp, err = proxy.SendOnce(req.Ctx, req.Client, http.MethodPost, autoclawBase(req)+autoclawChatPath, headers, body)
		if err != nil {
			return fmt.Errorf("autoclaw: upstream retry: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
	}

	// After the one 401 retry (and for every other non-200), surface the
	// status as an UpstreamError so failover and retry classification see
	// it; a raw relay would label an auth failure as a 200 SSE stream.
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
		if readErr != nil {
			errBody = nil
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header, ReadErr: readErr}
	}

	forwardUpstreamResponseHeaders(w, resp.Header)

	if req.IsStream {
		// SSECopy relays each upstream chunk with a flush, so the first
		// token reaches the client the moment upstream sends it; io.Copy
		// left every byte in the server's write buffer until the stream
		// closed (TTFT == total stream duration). It is the same relay
		// every other OpenAI-lane executor uses, so a bare [DONE] or an
		// in-band provider error is repaired the house way.
		flusher := proxy.WriteSSEHeaders(w)
		var streamUsage *translator.OpenAIUsage
		return proxy.SSECopy(w, resp.Body, flusher, func(chunk []byte) {
			markTTFT(req, req.StartTime)
			if req.Ctx != nil {
				streamUsage = captureUsageFromSSEChunk(req.Ctx, streamUsage, chunk)
			}
		})
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("autoclaw: read non-stream response: %w", err)
	}
	// The upstream protocol is SSE-only, so a stream:false ask still comes
	// back as an event stream. Fold it into one chat.completion before
	// labelling it application/json (the openai.go/codebuddy.go path);
	// raw SSE bytes under a JSON header fail every OpenAI client's parse.
	if proxy.LooksLikeSSE(raw) {
		folded, ok := sseToOpenAIJSON(raw)
		if !ok {
			// SSE-shaped but carrying no completion chunk: relabelling it
			// repeats the bug, and a 502 lets combo fallback move on.
			return proxy.UpstreamFailure(http.StatusBadGateway, proxy.NoCompletionInStream)
		}
		raw = folded
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, writeErr := w.Write(raw)
	return writeErr
}

// autoclawRequestedModel resolves the model id this turn is addressed to.
// The chat dispatcher leaves Request.ModelName unset (the same gap
// gemini_web.go documents), so the body's "model" field is authoritative.
// A provider prefix is stripped for the bare route id. Upstream reads the
// model only from X-Request-Model, so an empty value is a hard error.
func autoclawRequestedModel(req *Request) string {
	model := strings.TrimSpace(req.ModelName)
	if model == "" {
		var probe struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Body, &probe); err == nil {
			model = strings.TrimSpace(probe.Model)
		}
	}
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	return model
}

// splitAutoclawCredential splits "refresh_token|device_id|app_key" into its
// parts. The app key is the desktop client's signing secret: the operator
// supplies it, so this tree carries no third-party secret. A bare refresh
// token (no device id, no app key) is accepted as far as the refresh call,
// but the request cannot be signed without the app key.
func splitAutoclawCredential(cred string) (refreshToken, deviceID, appKey string) {
	parts := strings.SplitN(cred, "|", 3)
	refreshToken = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		deviceID = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		appKey = strings.TrimSpace(parts[2])
	}
	return
}

// autoclawBody normalises the request to the shape a captured AutoClaw 2.6.1
// chat sends. The managed gateway rejects a bare OpenAI body (max_tokens,
// stream:false) with an empty 406; it requires stream:true,
// stream_options.include_usage, store:false, max_completion_tokens and a
// reasoning_effort. Caller-supplied values win; only missing keys are filled.
// tools/messages/model pass through untouched. Stream folding for a
// non-stream client happens in ForwardAutoclaw after the SSE is received.
func autoclawBody(raw []byte, isStream bool) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	envelope["stream"] = true
	if _, ok := envelope["stream_options"]; !ok {
		envelope["stream_options"] = map[string]any{"include_usage": true}
	}
	if _, ok := envelope["store"]; !ok {
		envelope["store"] = false
	}
	if max, ok := envelope["max_completion_tokens"]; !ok || max == nil {
		if legacy, exists := envelope["max_tokens"]; exists {
			envelope["max_completion_tokens"] = legacy
		} else {
			envelope["max_completion_tokens"] = 37500
		}
	}
	delete(envelope, "max_tokens")
	if _, ok := envelope["reasoning_effort"]; !ok {
		envelope["reasoning_effort"] = "max"
	}
	// The managed gateway requires AutoClaw's exact system prompt as the
	// FIRST message. A client-supplied system message (e.g. OpenCode's
	// title-generation prompt) does not satisfy it — that answers 406 with
	// `data: \n\ndata: [DONE]`. Prepend the desktop prompt and keep the
	// client's system message as the next one so its instructions survive.
	desktopSystem := map[string]any{"role": "system", "content": autoclawDesktopSystemPrompt}
	if msgs, ok := envelope["messages"].([]any); ok {
		firstIsDesktop := len(msgs) > 0
		if firstIsDesktop {
			m, _ := msgs[0].(map[string]any)
			firstIsDesktop = m != nil && m["role"] == "system" && m["content"] == autoclawDesktopSystemPrompt
		}
		if !firstIsDesktop {
			envelope["messages"] = append([]any{desktopSystem}, msgs...)
		}
	} else {
		envelope["messages"] = []any{desktopSystem}
	}
	return json.Marshal(envelope)
}

// autoclawHeaders builds the per-request signing envelope. The timestamp and
// MD5 signature change on every call, so they are never StaticHeaders. The
// app key comes from the operator's credential, never from a constant in
// this tree.
func autoclawHeaders(accessToken string, isStream bool, model, appKey string) map[string]string {
	ts := fmt.Sprintf("%d", now())
	sign := md5Sum(autoclawAppID + "&" + ts + "&" + appKey)

	// Header set mirrors a captured successful AutoClaw desktop chat request.
	// Notably the token goes in X-Authorization (not Authorization), the
	// desktop client identifies itself with X-Agent-Id, and it sends
	// x-return-billing / x_trace_id / user-agent: node. Deviating from this
	// set makes the identity gateway answer 406 with an empty body.
	requestID := mustUUID()
	headers := map[string]string{
		"Content-Type":     "application/json",
		"Accept":           "application/json",
		"Accept-Encoding":  "br, gzip, deflate",
		"Accept-Language":  "*",
		"User-Agent":       "node",
		"X-Agent-Id":       "main",
		"X-Authorization":  autoclawBearer(accessToken),
		"x-auth-appid":     autoclawAppID,
		"x-auth-timestamp": ts,
		"x-auth-sign":      sign,
		"x-product":        "autoclaw",
		"x-version":        autoclawVersion,
		"x-tm":             "win",
		"x-client-type":    "pc",
		"x-channel":        "official",
		"x-lang":           "en",
		"x-request-id":     requestID,
		"x-trace-id":       requestID,
		"x-query-id":       mustUUID(),
		"x-session-id":     mustUUID(),
		"x-return-billing": "true",
		"x_trace_id":       "autoclaw-desktop",
	}
	if model != "" {
		headers["X-Request-Model"] = model
	}
	// The desktop client keeps `Accept: application/json` even on streaming
	// calls — the gateway routes on that value and answers 406 with an empty
	// body when it sees text/event-stream here.
	return headers
}

// AutoClaw tokens from the identity gateway already carry the Bearer prefix.
// Sending "Bearer Bearer <jwt>" makes upstream answer "Invalid token".
func autoclawBearer(token string) string {
	if strings.HasPrefix(strings.TrimSpace(token), "Bearer ") {
		return token
	}
	return "Bearer " + token
}

func md5Sum(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// mustUUID returns a random UUID string. uuid is a direct go.mod dependency.
func mustUUID() string {
	return uuid.New().String()
}
