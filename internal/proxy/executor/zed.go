package executor

// Zed Cloud serving lane.
//
// Zed is not an OpenAI-compatible endpoint. A completion is two calls behind
// two different auth layers:
//
//	layer 1  Authorization: {user_id} {access_token}   POST /client/llm_tokens
//	layer 2  Authorization: Bearer {llm_token}         POST /completions
//
// and the model call takes a Zed envelope that wraps the provider-native body:
//
//	{"provider":"anthropic","model":"claude-haiku-5-5",
//	 "provider_request":{...Anthropic Messages body...},"thread_id":"..."}
//
// The client of this gateway speaks OpenAI chat completions, so the reply is
// translated back: Claude SSE through the translator package, Responses SSE
// through the same Codex bridge every Responses upstream uses.
//
// The account-level calls (token exchange, model catalogue) live in
// internal/providers/zed.go, which the dashboard's live catalogue already
// uses. This file owns only the serving lane, and keeps its own token cache so
// the exchange travels on the connection's own HTTP client (proxy pools, TLS
// fallback) instead of a bare one.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

const (
	// zedDefaultOrigin is the host serving both the account API and the model
	// API. The connection's BaseURL decides the origin when it names one, so a
	// test or a mirror can point elsewhere.
	zedDefaultOrigin = "https://cloud.zed.dev"

	// zedLLMTokenTTL bounds a cached LLM token whose JWT carries no readable
	// expiry. Same conservative window the catalogue client uses.
	zedLLMTokenTTL = 30 * time.Minute

	// zedLLMTokenSkew retires a token slightly before its stated expiry so a
	// request never starts on one that dies mid-flight.
	zedLLMTokenSkew = 60 * time.Second

	// zedDefaultMaxOutputTokens is the output budget a Claude body gets when
	// the client named none. Anthropic requires a positive max_tokens.
	zedDefaultMaxOutputTokens = 4096
)

// ZedWireModel is the (provider, model) pair Zed's envelope carries: the
// provider names the nested body's dialect, the model is the id inside it.
type ZedWireModel struct {
	Provider string
	Model    string
}

// ZedModelOverrides maps a client-facing model id to an explicit wire target.
// A nil map means "resolve from the model id prefix alone". A value with an
// empty Model keeps the client's id and only pins the provider.
type ZedModelOverrides map[string]ZedWireModel

// ResolveZedModel decides the wire provider and inner model id for a requested
// model. Deterministic: claude* speaks Anthropic, gpt*/o-series speak OpenAI's
// Responses API, and anything else is refused rather than guessed.
func ResolveZedModel(requested string, overrides ZedModelOverrides) (ZedWireModel, error) {
	id := zedStripModelPrefix(requested)
	if id == "" {
		return ZedWireModel{}, fmt.Errorf("zed: request carries no model id")
	}
	if wire, ok := overrides[id]; ok {
		if wire.Model == "" {
			wire.Model = id
		}
		if wire.Provider == "" {
			wire.Provider = providers.ZedWireProvider(id)
		}
		if wire.Provider == "" {
			return ZedWireModel{}, fmt.Errorf("zed: model %q has no wire provider", id)
		}
		return wire, nil
	}
	provider := providers.ZedWireProvider(id)
	if provider == "" {
		return ZedWireModel{}, fmt.Errorf("zed: model %q is not served by Zed (unknown provider)", id)
	}
	return ZedWireModel{Provider: provider, Model: id}, nil
}

// zedStripModelPrefix removes a provider prefix a client may echo
// ("zed/claude-…", "zed:claude-…") so the wire model is the bare id.
func zedStripModelPrefix(model string) string {
	id := strings.TrimSpace(model)
	for _, prefix := range []string{"zed/", "zed:", "zed."} {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return id
}

// BuildZedCompletionBody wraps an OpenAI chat-completions request in Zed's
// envelope. threadID, when non-empty, is sent as the top-level thread_id so
// Zed can group the turn with the rest of its session.
func BuildZedCompletionBody(openAIRequest []byte, wire ZedWireModel, threadID string) ([]byte, error) {
	trimmed := bytes.TrimSpace(openAIRequest)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("zed: empty request body")
	}
	if wire.Provider == "" || wire.Model == "" {
		return nil, fmt.Errorf("zed: envelope needs a provider and a model")
	}

	var providerRequest map[string]any
	var err error
	switch wire.Provider {
	case "anthropic":
		providerRequest, err = zedClaudeProviderRequest(trimmed, wire.Model)
	case "open_ai":
		providerRequest, err = zedResponsesProviderRequest(trimmed, wire.Model)
	case "google":
		providerRequest, err = zedGoogleProviderRequest(trimmed, wire.Model)
	default:
		return nil, fmt.Errorf("zed: unsupported wire provider %q", wire.Provider)
	}
	if err != nil {
		return nil, err
	}

	envelope := map[string]any{
		"provider":         wire.Provider,
		"model":            wire.Model,
		"provider_request": providerRequest,
	}
	if threadID = strings.TrimSpace(threadID); threadID != "" {
		envelope["thread_id"] = threadID
	}
	payload, merr := marshalStable(envelope)
	if merr != nil {
		return nil, fmt.Errorf("zed: marshal completion envelope: %w", merr)
	}
	return payload, nil
}

// zedClaudeProviderRequest builds the Anthropic Messages body nested in the
// envelope. The OpenAI→Claude request conversion is the repo's own
// (EnsureClaudeMessagesFor), so tools, tool_choice, system extraction and the
// alternating-role invariant behave exactly as they do on every other
// Claude-shaped lane. The lane id "zed" (not the wire provider "anthropic") is
// the one consulted for the thinking shape: capabilities pins claude-haiku-5-5
// to adaptive thinking on this lane, which another provider's row would not see.
func zedClaudeProviderRequest(openAIBody []byte, model string) (map[string]any, error) {
	claudeBody := openAIBody
	if !zedBodyIsClaude(openAIBody) {
		claudeBody = EnsureClaudeMessagesFor(openAIBody, "zed", model)
	}

	var body map[string]any
	if err := json.Unmarshal(claudeBody, &body); err != nil {
		return nil, fmt.Errorf("zed: parse claude provider request: %w", err)
	}
	body["model"] = model
	// Zed streams every completion; a non-streaming client is served from the
	// aggregated stream, so the upstream is always asked to stream.
	body["stream"] = true
	delete(body, "stream_options")

	if mt := zedAsInt(body["max_tokens"]); mt <= 0 {
		body["max_tokens"] = zedDefaultMaxOutputTokens
	}

	// The Zed Claude lane serves adaptive thinking for claude-haiku-5-5, and
	// Anthropic rejects temperature on a thinking request unless it is exactly
	// 1 ("may only be set to 1 when thinking is enabled or in adaptive mode").
	// Editors send temperature on every chat turn, so the sampling knob is
	// dropped here rather than forwarded as a value the backend refuses.
	delete(body, "temperature")
	// The Messages API requires content to be an array of blocks. The shared
	// converter leaves a plain string alone on the plain-text path, so the
	// array is enforced here — on the envelope that actually goes upstream.
	if messages, ok := body["messages"].([]any); ok {
		for _, raw := range messages {
			if msg, isMap := raw.(map[string]any); isMap {
				zedNormalizeClaudeContent(msg)
			}
		}
	}
	return body, nil
}

// zedBodyIsClaude reports whether a body is already a Claude Messages request
// rather than an OpenAI chat one. A /v1/messages client's body reaches the
// executor untouched, and converting it again would double-encode it.
func zedBodyIsClaude(body []byte) bool {
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	if _, ok := probe["anthropic_version"]; ok {
		return true
	}
	_, ok := probe["system"]
	return ok
}

// zedNormalizeClaudeContent rewrites a message's content into the block array
// the Messages API requires.
func zedNormalizeClaudeContent(msg map[string]any) {
	content, ok := msg["content"]
	if !ok || content == nil {
		return
	}
	switch v := content.(type) {
	case string:
		if v == "" {
			msg["content"] = []any{}
			return
		}
		msg["content"] = []any{map[string]any{"type": "text", "text": v}}
	case []any:
		blocks := make([]any, 0, len(v))
		for _, block := range v {
			if text, isStr := block.(string); isStr {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
				continue
			}
			blocks = append(blocks, block)
		}
		msg["content"] = blocks
	}
}

// zedResponsesProviderRequest builds the OpenAI Responses body nested in the
// envelope: every item carries a type, and message content uses input_text.
func zedResponsesProviderRequest(openAIBody []byte, model string) (map[string]any, error) {
	var src map[string]any
	if err := json.Unmarshal(openAIBody, &src); err != nil {
		return nil, fmt.Errorf("zed: parse request body: %w", err)
	}

	items := make([]any, 0, len(zedAsSlice(src["messages"])))
	for _, raw := range zedAsSlice(src["messages"]) {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := zedAsString(msg["role"])
		switch role {
		case "tool":
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": zedAsString(msg["tool_call_id"]),
				"output":  zedContentText(msg["content"]),
			})
		case "assistant":
			for _, tcRaw := range zedAsSlice(msg["tool_calls"]) {
				call, isMap := tcRaw.(map[string]any)
				if !isMap {
					continue
				}
				fn, _ := call["function"].(map[string]any)
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   zedAsString(call["id"]),
					"name":      zedAsString(fn["name"]),
					"arguments": zedAsString(fn["arguments"]),
				})
			}
			if text := zedContentText(msg["content"]); text != "" {
				items = append(items, zedResponsesMessage("assistant", "output_text", text))
			}
		default:
			items = append(items, zedResponsesMessage(role, "input_text", zedContentText(msg["content"])))
		}
	}

	body := map[string]any{
		"model":  model,
		"input":  items,
		"stream": true,
	}
	if maxOut := zedAsInt(src["max_completion_tokens"]); maxOut > 0 {
		body["max_output_tokens"] = maxOut
	} else if maxOut := zedAsInt(src["max_tokens"]); maxOut > 0 {
		body["max_output_tokens"] = maxOut
	}
	if tools := zedAsSlice(src["tools"]); len(tools) > 0 {
		body["tools"] = zedResponsesTools(tools)
	}
	for _, key := range []string{"temperature", "top_p"} {
		if v, ok := src[key]; ok && v != nil {
			body[key] = v
		}
	}
	return body, nil
}

// zedAggregateGeminiSSE folds a Gemini event stream into the single document
// a non-streaming client expects.
//
// The text is concatenated across the events, not taken from the last one:
// a thinking model emits the answer first and then a terminal event whose
// part carries no text, so keeping the last event would drop the answer.
func zedAggregateGeminiSSE(raw []byte) []byte {
	var (
		texts    []string
		usage    any
		finish   string
		role     = "model"
		hasParts bool
	)
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || string(payload) == "[DONE]" {
			continue
		}
		var chunk struct {
			Candidates []struct {
				Content struct {
					Role  string `json:"role"`
					Parts []struct {
						Text    string `json:"text"`
						Thought bool   `json:"thought"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata any `json:"usageMetadata"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}
		if chunk.UsageMetadata != nil {
			usage = chunk.UsageMetadata
		}
		for _, c := range chunk.Candidates {
			if c.Content.Role != "" {
				role = c.Content.Role
			}
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
			for _, p := range c.Content.Parts {
				if p.Thought || p.Text == "" {
					continue
				}
				texts = append(texts, p.Text)
				hasParts = true
			}
		}
	}
	if !hasParts {
		return nil
	}
	doc := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role":  role,
					"parts": []any{map[string]any{"text": strings.Join(texts, "")}},
				},
				"finishReason": finish,
			},
		},
	}
	if usage != nil {
		doc["usageMetadata"] = usage
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return out
}

// zedGoogleProviderRequest builds the Google (Gemini) body nested in the
// envelope. Zed's google provider speaks the Gemini dialect — "contents" with
// "parts", and the generation settings under "generationConfig" — which the
// repository's own OpenAI→Gemini converter already produces.
func zedGoogleProviderRequest(openAIBody []byte, model string) (map[string]any, error) {
	converted, err := translator.TranslateOpenAIToGemini(openAIBody)
	if err != nil {
		return nil, fmt.Errorf("zed: convert request to gemini: %w", err)
	}
	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		return nil, fmt.Errorf("zed: parse gemini provider request: %w", err)
	}
	body["model"] = model
	// Zed streams every completion; a non-streaming client is served from the
	// aggregated stream.
	body["stream"] = true

	// The output budget lives in the generation config, not at the top level.
	config, _ := body["generationConfig"].(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	if _, ok := config["maxOutputTokens"]; !ok {
		config["maxOutputTokens"] = zedDefaultMaxOutputTokens
	}
	body["generationConfig"] = config
	return body, nil
}

// zedResponsesMessage is one Responses input message item.
func zedResponsesMessage(role, blockType, text string) map[string]any {
	if role == "" {
		role = "user"
	}
	return map[string]any{
		"type": "message",
		"role": role,
		"content": []any{
			map[string]any{"type": blockType, "text": text},
		},
	}
}

// zedResponsesTools flattens chat-completions tools into the Responses shape
// ({type:function, name, description, parameters}).
func zedResponsesTools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		fn, hasFn := tool["function"].(map[string]any)
		if !hasFn {
			out = append(out, tool)
			continue
		}
		flat := map[string]any{"type": "function"}
		for _, key := range []string{"name", "description", "parameters", "strict"} {
			if v, ok := fn[key]; ok {
				flat[key] = v
			}
		}
		out = append(out, flat)
	}
	return out
}

// zedContentText flattens a chat message content value (string or block array)
// into plain text.
func zedContentText(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, block := range v {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			text := zedAsString(blockMap["text"])
			if text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(text)
		}
		return b.String()
	default:
		return fmt.Sprint(v)
	}
}

func zedAsString(v any) string {
	s, _ := v.(string)
	return s
}

func zedAsInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func zedAsSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// zedOrigin derives the account/model API origin from the connection's base
// URL. The registry points BaseURL at /completions; only its origin is used,
// because the two Zed APIs live at fixed paths under one host.
func zedOrigin(cfg *providers.ProviderConfig) string {
	if cfg != nil && strings.TrimSpace(cfg.BaseURL) != "" {
		if u, err := url.Parse(strings.TrimSpace(cfg.BaseURL)); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return zedDefaultOrigin
}

// zedCredentials reads the three providerSpecificData values and the stored
// credential blob off the request.
func zedCredentials(req *Request) providers.ZedCredentials {
	creds := providers.ZedCredentials{AccessToken: req.APIKey}
	if psd := req.ConnData; psd != nil {
		creds.UserID = zedConnString(psd, "userId")
		creds.OrganizationID = zedConnString(psd, "organizationId")
		creds.SystemID = zedConnString(psd, "systemId")
	}
	return creds
}

func zedConnString(psd map[string]any, key string) string {
	if v, ok := psd[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// zedTokenCacheEntry is one minted LLM token and the moment it stops being
// trusted.
type zedTokenCacheEntry struct {
	token     string
	expiresAt time.Time
}

var (
	zedTokenMu    sync.Mutex
	zedTokenCache = map[string]zedTokenCacheEntry{}
)

// zedTokenCacheKey identifies one credential. The access token is hashed so
// the raw credential never becomes a map key.
func zedTokenCacheKey(origin string, creds providers.ZedCredentials) string {
	sum := sha256.Sum256([]byte(creds.AccessToken))
	return origin + "|" + creds.UserID + "|" + creds.OrganizationID + "|" + hex.EncodeToString(sum[:])
}

// zedLLMToken returns a valid layer-2 token, minting one when the cache is
// cold, the cached one expired, or force is set (a 401 says the token we sent
// is not trusted even if its exp claim says otherwise).
func zedLLMToken(ctx context.Context, client *http.Client, origin string, creds providers.ZedCredentials, force bool) (string, error) {
	key := zedTokenCacheKey(origin, creds)
	if !force {
		zedTokenMu.Lock()
		cached, ok := zedTokenCache[key]
		zedTokenMu.Unlock()
		if ok && time.Now().Before(cached.expiresAt) {
			return cached.token, nil
		}
	}

	token, expiresAt, err := zedMintLLMToken(ctx, client, origin, creds)
	if err != nil {
		return "", err
	}
	zedTokenMu.Lock()
	zedTokenCache[key] = zedTokenCacheEntry{token: token, expiresAt: expiresAt}
	zedTokenMu.Unlock()
	return token, nil
}

// zedMintLLMToken exchanges the account token for an LLM token. The exchange
// goes through proxy.DoRequest so it inherits the connection's egress rules,
// the TLS fallback and the transient-retry policy like every other call.
func zedMintLLMToken(ctx context.Context, client *http.Client, origin string, creds providers.ZedCredentials) (string, time.Time, error) {
	payload, err := json.Marshal(map[string]any{"organization_id": creds.OrganizationID})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("zed: marshal llm token request: %w", err)
	}
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": providers.ZedAuthHeader(creds),
	}
	if creds.SystemID != "" {
		headers[providers.ZedSystemIDHeader] = creds.SystemID
	}
	resp, err := proxy.DoRequest(ctx, client, http.MethodPost, origin+providers.ZedLLMTokensPath, headers, payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("zed: llm token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("zed: read llm token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: raw, Header: resp.Header}
	}
	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("zed: parse llm token response: %w", err)
	}
	if parsed.Token == "" {
		return "", time.Time{}, &proxy.UpstreamError{
			StatusCode: http.StatusBadGateway,
			Body:       []byte(`{"error":{"message":"zed: llm token response carried no token","type":"upstream_error","code":502}}`),
			Header:     resp.Header,
		}
	}
	return parsed.Token, zedTokenExpiry(parsed.Token, time.Now()), nil
}

// zedTokenExpiry reads the JWT's exp claim. A token whose payload cannot be
// read is still cached, but only for the conservative default window.
func zedTokenExpiry(token string, now time.Time) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return now.Add(zedLLMTokenTTL)
	}
	payload, err := zedDecodeTokenSegment(parts[1])
	if err != nil {
		return now.Add(zedLLMTokenTTL)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return now.Add(zedLLMTokenTTL)
	}
	expiry := time.Unix(claims.Exp, 0)
	if !expiry.After(now.Add(zedLLMTokenSkew)) {
		return now.Add(zedLLMTokenSkew)
	}
	return expiry
}

// zedDecodeTokenSegment decodes a JWT segment, accepting both the unpadded
// base64url Zed issues and a padded variant (the token is echoed back from the
// desktop client, which has shipped both).
func zedDecodeTokenSegment(seg string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(seg); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(seg)
}

// ForwardZed serves a Zed Cloud completion: two-stage auth, the Zed envelope,
// and the provider-native reply translated to OpenAI chat completions.
func ForwardZed(w http.ResponseWriter, req *Request) error {
	if req == nil || req.Config == nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, "zed: executor misconfigured")
	}
	creds := zedCredentials(req)
	if creds.AccessToken == "" || creds.UserID == "" {
		return &proxy.UpstreamError{
			StatusCode: http.StatusUnauthorized,
			Body:       []byte(`{"error":{"message":"zed: connection is missing the access token or user id","type":"auth_error","code":401}}`),
		}
	}

	requested := strings.TrimSpace(req.ModelName)
	if requested == "" {
		requested = zedBodyModel(req.Body)
	}
	wire, err := ResolveZedModel(requested, nil)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, err.Error())
	}

	envelope, err := BuildZedCompletionBody(req.Body, wire, req.SessionID)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, err.Error())
	}

	origin := zedOrigin(req.Config)
	token, err := zedLLMToken(req.Ctx, req.Client, origin, creds, false)
	if err != nil {
		return err
	}
	resp, err := zedPostCompletion(req, origin, creds, token, envelope)
	if err != nil {
		// A 401 on the model call means the cached token is dead (revoked, or
		// expired earlier than its exp claim promised): mint once more and
		// replay the call. proxy.DoRequest reports every non-200 as an
		// *UpstreamError, so the status is read off the error, never off a
		// response — and a 401 from the token exchange itself is not replayed,
		// because a rejected account token cannot be fixed by re-minting.
		if !zedIsUnauthorized(err) {
			return err
		}
		token, err = zedLLMToken(req.Ctx, req.Client, origin, creds, true)
		if err != nil {
			return err
		}
		resp, err = zedPostCompletion(req, origin, creds, token, envelope)
		if err != nil {
			return err
		}
	}

	var bodyCloser = resp.Body
	defer func() {
		if bodyCloser != nil {
			_ = bodyCloser.Close()
		}
	}()
	forwardUpstreamResponseHeaders(w, resp.Header)

	if req.IsStream {
		streamBody := io.ReadCloser(resp.Body)
		if !isEventStreamResponse(resp.Header) {
			data, rerr := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
			if rerr != nil {
				return fmt.Errorf("read zed response: %w", rerr)
			}
			if !proxy.LooksLikeSSE(data) && !zedLooksLikeJSONL(data) {
				return zedWriteWhole(w, req, wire, data)
			}
			streamBody = io.NopCloser(bytes.NewReader(data))
		}
		stall := proxy.NewStallReaderWithContext(req.Ctx, streamBody, 0, "zed")
		bodyCloser = stall
		return zedPipeStream(w, req, wire, stall)
	}

	data, rerr := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
	if rerr != nil {
		return fmt.Errorf("read zed response: %w", rerr)
	}
	return zedWriteWhole(w, req, wire, data)
}

// zedPipeStream hands a live upstream stream to the translator for the wire
// dialect: Anthropic events to the Claude bridge, Responses events to the
// Codex one.
//
// Zed frames its stream as JSON Lines, so the events are re-framed as SSE
// before they reach a bridge that only speaks SSE.
func zedPipeStream(w http.ResponseWriter, req *Request, wire ZedWireModel, upstream io.Reader) error {
	framed := zedJSONLToSSE(upstream)
	// Zed's bridges do not set TTFT themselves (the Claude one only does on
	// its TranslateResp passthrough), so the first frame read off the stream
	// is what marks the metric. Without this the request is logged with no
	// time-to-first-token at all.
	if req.TTFT != nil && *req.TTFT == 0 {
		start := req.StartTime
		if start.IsZero() {
			start = time.Now()
		}
		framed = &zedTTFTReader{inner: framed, ttft: req.TTFT, start: start}
	}
	switch wire.Provider {
	case "anthropic":
		return handleClaudeMessagesStream(w, req, framed)
	case "google":
		return zedGeminiStream(w, req, framed)
	default:
		return handleCodexStream(w, req, framed)
	}
}

// zedWriteWhole answers from a complete upstream body, which may be a JSON
// document or a whole stream (Zed streams even for a non-streaming ask).
func zedWriteWhole(w http.ResponseWriter, req *Request, wire ZedWireModel, data []byte) error {
	// Zed streams even for a non-streaming ask, so this path reads the whole
	// body before answering. The metric still belongs to the first upstream
	// byte: without this the request is logged with no time-to-first-token at
	// all, which is indistinguishable from an executor that never tracked it.
	if req.TTFT != nil && *req.TTFT == 0 && len(data) > 0 {
		start := req.StartTime
		if start.IsZero() {
			start = time.Now()
		}
		*req.TTFT = time.Since(start).Milliseconds()
	}
	if zedLooksLikeJSONL(data) {
		// JSON Lines: re-frame as SSE, then fold it into the single document
		// a non-streaming client expects.
		data = readAllFramed(data)
	}
	if wire.Provider == "anthropic" {
		if !proxy.LooksLikeSSE(data) {
			return handleClaudeMessagesNonStream(w, req, bytes.NewReader(data))
		}
		aggregated, err := zedAggregateClaudeSSE(data)
		if err != nil {
			return err
		}
		if usage := translator.ParseClaudeUsage(aggregated); usage != nil {
			translator.SetUsage(req.Ctx, usage)
		}
		return handleClaudeMessagesNonStream(w, req, bytes.NewReader(aggregated))
	}
	if wire.Provider == "google" {
		if proxy.LooksLikeSSE(data) {
			// A streamed Gemini body: fold the events into one document.
			return zedGeminiWhole(w, req, zedAggregateGeminiSSE(data))
		}
		return zedGeminiWhole(w, req, data)
	}
	if proxy.LooksLikeSSE(data) {
		return handleCodexStream(w, req, bytes.NewReader(data))
	}
	converted, err := zedResponsesJSONToChat(data)
	if err != nil {
		return err
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(converted), req.TranslateResp, req.ResponseBuf)
}

// zedPostCompletion runs POST /completions with the layer-2 bearer token.
// The token is passed in rather than minted here so the caller owns the
// refresh-on-401 policy: one mint, one replay, no nested retries.
func zedPostCompletion(req *Request, origin string, creds providers.ZedCredentials, token string, envelope []byte) (*http.Response, error) {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + token,
	}
	if req.IsStream {
		headers["Accept"] = "text/event-stream"
	}
	if creds.SystemID != "" {
		headers[providers.ZedSystemIDHeader] = creds.SystemID
	}
	for k, v := range req.Config.StaticHeaders {
		headers[k] = v
	}
	resp, err := proxy.DoRequest(req.Ctx, req.Client, http.MethodPost, origin+providers.ZedCompletionsPath, headers, envelope)
	if err != nil {
		return nil, fmt.Errorf("zed: completion request: %w", err)
	}
	return resp, nil
}

// zedIsUnauthorized reports whether an upstream failure is a 401, including
// through the wrapping zedPostCompletion adds. A 401 is the only status the
// serving lane replays, so it is tested for separately from the pass-through
// path, which must surface every other status and body verbatim.
func zedIsUnauthorized(err error) bool {
	var upstreamErr *proxy.UpstreamError
	if !errors.As(err, &upstreamErr) {
		return false
	}
	return upstreamErr.StatusCode == http.StatusUnauthorized
}

// zedBodyModel reads the model id out of the request body when the dispatch
// layer did not extract one.
func zedBodyModel(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	return probe.Model
}

// zedAggregateClaudeSSE folds an Anthropic event stream into the single
// Messages document a non-streaming client expects.
func zedAggregateClaudeSSE(raw []byte) ([]byte, error) {
	var (
		id, model, stopReason string
		usage                 = map[string]any{}
		blocks                = map[int]map[string]any{}
		order                 []int
		partials              = map[int]*strings.Builder{}
		sawAny                bool
		errRaw                []byte
	)

	_ = proxy.ScanStream(bytes.NewReader(raw), func(payload []byte) {
		if errRaw != nil {
			return
		}
		var event map[string]any
		if err := json.Unmarshal(payload, &event); err != nil {
			return
		}
		sawAny = true
		switch zedAsString(event["type"]) {
		case "error":
			errRaw = append([]byte(nil), payload...)
		case "message_start":
			msg, _ := event["message"].(map[string]any)
			id = zedAsString(msg["id"])
			model = zedAsString(msg["model"])
			if u, ok := msg["usage"].(map[string]any); ok {
				for k, v := range u {
					usage[k] = v
				}
			}
		case "content_block_start":
			index := zedAsInt(event["index"])
			block, _ := event["content_block"].(map[string]any)
			copied := make(map[string]any, len(block))
			for k, v := range block {
				copied[k] = v
			}
			blocks[index] = copied
			order = append(order, index)
			partials[index] = &strings.Builder{}
		case "content_block_delta":
			index := zedAsInt(event["index"])
			delta, _ := event["delta"].(map[string]any)
			block := blocks[index]
			if block == nil {
				block = map[string]any{"type": zedAsString(delta["type"])}
				blocks[index] = block
				order = append(order, index)
			}
			switch zedAsString(delta["type"]) {
			case "text_delta":
				block["text"] = zedAsString(block["text"]) + zedAsString(delta["text"])
			case "thinking_delta":
				block["thinking"] = zedAsString(block["thinking"]) + zedAsString(delta["thinking"])
			case "signature_delta":
				block["signature"] = zedAsString(delta["signature"])
			case "input_json_delta":
				if partials[index] == nil {
					partials[index] = &strings.Builder{}
				}
				partials[index].WriteString(zedAsString(delta["partial_json"]))
			}
		case "content_block_stop":
			index := zedAsInt(event["index"])
			builder := partials[index]
			if builder == nil || builder.Len() == 0 {
				return
			}
			var input any
			if err := json.Unmarshal([]byte(builder.String()), &input); err == nil {
				blocks[index]["input"] = input
			}
		case "message_delta":
			delta, _ := event["delta"].(map[string]any)
			if s := zedAsString(delta["stop_reason"]); s != "" {
				stopReason = s
			}
			if u, ok := event["usage"].(map[string]any); ok {
				for k, v := range u {
					usage[k] = v
				}
			}
		}
	})

	if errRaw != nil {
		return nil, &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: errRaw}
	}
	if !sawAny {
		return nil, proxy.UpstreamFailure(http.StatusBadGateway, proxy.NoCompletionInStream)
	}

	sort.Ints(order)
	content := make([]any, 0, len(order))
	for _, index := range order {
		block := blocks[index]
		if block == nil {
			continue
		}
		if zedAsString(block["type"]) == "tool_use" && block["input"] == nil {
			block["input"] = map[string]any{}
		}
		content = append(content, block)
	}

	message := map[string]any{
		"id":      id,
		"type":    "message",
		"role":    "assistant",
		"model":   model,
		"content": content,
	}
	if stopReason != "" {
		message["stop_reason"] = stopReason
	}
	if len(usage) > 0 {
		message["usage"] = usage
	}
	aggregated, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("zed: marshal aggregated claude response: %w", err)
	}
	return aggregated, nil
}

// zedResponsesJSONToChat converts a non-streamed Responses object into an
// OpenAI chat-completion document.
func zedResponsesJSONToChat(raw []byte) ([]byte, error) {
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("zed: parse responses body: %w", err)
	}

	var text strings.Builder
	var reasoning strings.Builder
	var toolCalls []any
	for _, itemRaw := range zedAsSlice(resp["output"]) {
		item, ok := itemRaw.(map[string]any)
		if !ok {
			continue
		}
		switch zedAsString(item["type"]) {
		case "message":
			for _, partRaw := range zedAsSlice(item["content"]) {
				part, isMap := partRaw.(map[string]any)
				if !isMap {
					continue
				}
				text.WriteString(zedAsString(part["text"]))
			}
		case "reasoning":
			for _, partRaw := range zedAsSlice(item["summary"]) {
				part, isMap := partRaw.(map[string]any)
				if !isMap {
					continue
				}
				reasoning.WriteString(zedAsString(part["text"]))
			}
		case "function_call":
			toolCalls = append(toolCalls, map[string]any{
				"id":   zedAsString(item["call_id"]),
				"type": "function",
				"function": map[string]any{
					"name":      zedAsString(item["name"]),
					"arguments": zedAsString(item["arguments"]),
				},
			})
		}
	}

	message := map[string]any{"role": "assistant", "content": text.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	created := time.Now().Unix()
	completion := map[string]any{
		"id":      zedAsString(resp["id"]),
		"object":  "chat.completion",
		"created": created,
		"model":   zedAsString(resp["model"]),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": zedResponsesFinishReason(resp, len(toolCalls) > 0),
		}},
	}
	if usage, ok := resp["usage"].(map[string]any); ok {
		chatUsage := map[string]any{}
		if v := zedAsInt(usage["input_tokens"]); v > 0 {
			chatUsage["prompt_tokens"] = v
		}
		if v := zedAsInt(usage["output_tokens"]); v > 0 {
			chatUsage["completion_tokens"] = v
		}
		if v := zedAsInt(usage["total_tokens"]); v > 0 {
			chatUsage["total_tokens"] = v
		}
		if len(chatUsage) > 0 {
			completion["usage"] = chatUsage
		}
	}
	if id := zedAsString(completion["id"]); id == "" {
		completion["id"] = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if model := zedAsString(completion["model"]); model == "" {
		delete(completion, "model")
	}
	converted, err := json.Marshal(completion)
	if err != nil {
		return nil, fmt.Errorf("zed: marshal chat completion: %w", err)
	}
	return converted, nil
}

// zedResponsesFinishReason maps a Responses status/incomplete reason onto the
// chat-completions finish_reason vocabulary.
func zedResponsesFinishReason(resp map[string]any, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	if zedAsString(resp["status"]) == "incomplete" {
		if details, ok := resp["incomplete_details"].(map[string]any); ok {
			if zedAsString(details["reason"]) == "max_output_tokens" {
				return "length"
			}
		}
		return "length"
	}
	return "stop"
}

// zedTTFTReader stamps the time-to-first-token metric on the first successful
// read of the upstream stream.
type zedTTFTReader struct {
	inner io.Reader
	ttft  *int64
	start time.Time
	done  bool
}

func (r *zedTTFTReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 && !r.done {
		r.done = true
		if *r.ttft == 0 {
			*r.ttft = time.Since(r.start).Milliseconds()
		}
	}
	return n, err
}
