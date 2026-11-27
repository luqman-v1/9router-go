package chat

import (
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/observ"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/thinking"
	"9router/proxy/internal/tracing"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	internalproxy "9router/proxy/internal/proxy"
)

// forwardRequestParams bundles tryForwardWithConnection inputs. Ten positional
// params (including two adjacent bools) made call sites unreadable and
// extension error-prone; named fields fix the call sites while the function
// body intentionally keeps short local aliases.
type forwardRequestParams struct {
	Ctx               context.Context
	W                 http.ResponseWriter
	Provider          string
	Model             string
	RequestedModel    string
	ComboName         string
	Attempts          int
	Protocol          string
	StartedAt         time.Time
	ConnectionID      string
	ConnName          string // human-readable account name, for logs
	ConnEmail         string
	ConnData          *ConnectionData
	Body              []byte
	IsStream          bool
	TranslateResponse bool
	Endpoint          string
}

// streamNoteWriter wraps an io.Writer and stamps each non-empty write with the
// current time via note, so every streamed chunk is timed for gap/rate metrics.
// A nil note is a no-op.
type streamNoteWriter struct {
	w    io.Writer
	note func(time.Time)
}

func (s *streamNoteWriter) Write(p []byte) (int, error) {
	if s == nil || s.w == nil {
		return 0, nil
	}
	n, err := s.w.Write(p)
	if n > 0 && s.note != nil {
		s.note(time.Now())
	}
	return n, err
}

func (h *ChatHandler) tryForwardWithConnection(f forwardRequestParams) error {
	ctx, w := f.Ctx, f.W
	provider, model := f.Provider, f.Model
	connectionID, connData := f.ConnectionID, f.ConnData
	// authType is a DB column, not part of the data blob; ForwardMinimaxCode
	// needs it to tell a desktop-app lease connection from an OAuth one.
	authType := ""
	if connData != nil {
		authType = connData.AuthType
	}
	body, isStream := f.Body, f.IsStream
	translateResponse, endpoint := f.TranslateResponse, f.Endpoint
	// A custom JSON business endpoint may already have performed its operation
	// when it failed, so the answer is final: no account rotation, no combo
	// fallback, and no zero-content probe that would replay an empty answer.
	customJSON := isCustomJSONConn(connData)
	// Per-model admission for custom providers, here rather than only in
	// handleAccountFallback: the combo dispatch paths (combo.go's OpenAI and
	// Messages loops, combo_fusion.go's panel calls) reach the upstream through
	// this one function, so a cap on a combo member is enforced on the combo
	// path too — the member fails over to the next one instead of serving.
	// A refusal carries Local=true, so failover moves on without cooling the
	// credential — it was never exercised.
	if err := h.admitPerModel(ctx, provider, model); err != nil {
		return err
	}
	// Unified thinking normalisation, inbound half: capture the client's
	// reasoning intent before any converter rewrites the body. model stays
	// the requested id for logs/usage; wireModel is suffix-free.
	body, wireModel, thinkingIntent := h.prepareInboundThinking(ctx, provider, model, body, f.Endpoint == "/v1/v1/messages" || f.Endpoint == "/v1/messages")
	ctx = translator.WithUsageCapture(ctx)

	providerCfg, err := h.getProviderConfig(provider, connData)
	if err != nil {
		return fmt.Errorf("get config for %s/%s: %w", provider, model, err)
	}
	// anthropic-compatible nodes fronting a Claude model need Anthropic-Beta flags (parity #3797)
	if (provider == "claude" || strings.HasPrefix(provider, "anthropic-compatible-") || strings.HasPrefix(provider, "anthropic")) && strings.HasPrefix(model, "claude-") {
		if providerCfg.StaticHeaders == nil {
			providerCfg.StaticHeaders = make(map[string]string)
		}
		if _, ok := providerCfg.StaticHeaders["Anthropic-Beta"]; !ok {
			providerCfg.StaticHeaders["Anthropic-Beta"] = "prompt-caching-scope-2026-01-05, context-management-2025-06-27"
		}
	}

	// OAuth connections (e.g. Claude subscription logins) must authenticate
	// with "Authorization: Bearer" against the beta endpoint, matching the
	// Next.js dashboard (open-sse/executors/default.js + registry/claude.js).
	// Sending the OAuth access token via x-api-key makes api.anthropic.com
	// return 401 "API key is invalid" even after a successful token refresh.
	// A connection is OAuth when it only carries an accessToken (no apiKey).
	isAnthropic := isAnthropicUpstream(provider, providerCfg)
	// OAuth = connection carries only an accessToken, OR the token itself is
	// an Anthropic OAuth token (some connections store sk-ant-oat in APIKey).
	isOAuth := isAnthropic && connData != nil &&
		((connData.APIKey == "" && connData.AccessToken != "") ||
			strings.Contains(connData.APIKey, "sk-ant-oat"))
	if isOAuth {
		providerCfg.AuthHeader = "Authorization"
		providerCfg.AuthScheme = "bearer"
		if providerCfg.StaticHeaders != nil && providerCfg.StaticHeaders["x-relay-path"] != "" {
			providerCfg.StaticHeaders["x-relay-path"] = appendBetaQuery(providerCfg.StaticHeaders["x-relay-path"])
		} else {
			providerCfg.BaseURL = appendBetaQuery(providerCfg.BaseURL)
		}
	}

	apiKey := resolveProviderAuthToken(provider, connData, extractAPIKey(connData))
	if apiKey == "" {
		if providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		} else if !customJSONKeyOptional(connData) && provider != "agy-acp" {
			return &upstreamError{StatusCode: http.StatusUnauthorized, Body: []byte(`{"error":{"message":"no API key found","type":"auth_error","code":401}}`)}
		}
	}

	if connectionID != "" && !customJSON {
		rekey, _, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
		if err == nil {
			apiKey = rekey
		} else {
			log.Warn("fallback", "OAuth token refresh error", "conn", connectionID, "error", err)
		}
	}
	apiKey = NormalizeProviderToken(provider, apiKey)

	// Token savers + provider-format normalization:
	// - /v1/messages (claudeNative): body stays Claude format; savers inject
	//   into top-level "system".
	// - /v1/chat/completions to an Anthropic upstream: the body is OpenAI
	//   format and would otherwise be forwarded raw; convert it to a
	//   spec-compliant Claude Messages payload (top-level system, tools,
	//   merged roles) — same conversion the dashboard applies server-side.
	// claudeNative indicates the CLIENT body is in Claude Messages format.
	// Requires an Anthropic upstream too: chat.go converts /v1/messages
	// requests for non-Anthropic providers (DeepSeek, OpenAI-compatible) to
	// OpenAI format before the fallback, and passing endpoint "/v1/v1/messages"
	// alone would wrongly inject a top-level "system" the upstream ignores.
	//
	// A /v1/messages client is only converted away from Claude format when the
	// upstream cannot answer in it. That is true of two kinds of provider: one
	// routing some of its models to a Messages endpoint (opencode-zen's Claude
	// and Qwen lanes), and one whose own endpoint speaks Messages outright
	// (minimax-code on MiniMax's mavis gateway). Both keep the client's own
	// wire format end to end — upstream resolveTransport picks the
	// sourceFormat-matched transport and skips translation.
	messagesClient := endpoint == "/v1/v1/messages" || endpoint == "/v1/messages"
	// A Messages-speaking provider answers a Messages client in its own wire
	// format, whichever of the two ways it earns that: a model routed to a
	// Messages endpoint (opencode-zen's Claude and Qwen lanes), or an endpoint
	// that speaks Messages outright (minimax-code on the mavis gateway).
	// Anthropic's own two provider ids are the third way, and they follow the
	// same rule chat.go applies when it decides whether to convert at all: it
	// keeps the body in Claude format for provider "claude"/"anthropic"
	// whatever base URL the connection carries. Mirrored here, because this
	// flag is also what says "the body arriving is Claude-shaped" — without
	// the name check a relay or proxy override made isAnthropic false, the
	// outbound thinking pass ran with target "openai", and
	// FilterToOpenAIFormatJSON rewrote a Claude body's tools into
	// {type:"function",function:{…}} on its way to a Messages API.
	claudeNative := messagesClient && (isAnthropic ||
		provider == "claude" || provider == "anthropic" ||
		executor.ServesMessagesEndpoint(provider, model) ||
		servesClaudeMessages(provider, providerCfg))

	// upstreamClaude is what the executor needs to translate the Claude reply
	// back for a Chat Completions client. Anthropic's own API and a third-party
	// Messages endpoint both answer in Claude, but only Anthropic's wants the
	// beta query, the OAuth cloaking and the beta-flag merge below.
	upstreamClaude := isAnthropic || servesClaudeMessages(provider, providerCfg)
	compressStart := time.Now()
	pipedBody, origTokens, savedTokens, savedPct := h.applyTokenSavers(ctx, body, claudeNative)
	compressDurMs := int(time.Since(compressStart).Milliseconds())
	if compressDurMs <= 0 {
		compressDurMs = 1
	}
	// The mirror of the conversion below. A /v1/messages combo converts the
	// client's body once, for its FIRST member: when that member speaks Claude
	// the body is still Messages-shaped by the time a later member that does not
	// (cx/gpt-6.x, deepseek, …) is tried. Handing tool_use/tool_result blocks to
	// such an upstream loses them — buildResponsesBody keeps only blocks carrying
	// a "text" key — so the model never saw the output of its own tool call and
	// re-issued the same command forever.
	pipedBody = claudeBodyForNonClaudeUpstream(pipedBody, claudeNative, provider, model)
	var claudeToolMap map[string]string
	if upstreamClaude {
		if !claudeNative || bodySpeaksOpenAIChat(pipedBody) {
			// Raw OpenAI-format body would be invalid at the Messages API:
			// convert to a spec-compliant Claude payload (top-level system,
			// tools, merged roles) — what the dashboard does server-side.
			//
			// The body can be OpenAI-shaped even on a Claude-native provider:
			// a combo whose first member speaks Chat (cx/gpt-6-astra) converts
			// the client's Claude body once, for every member, so a later
			// Claude member inherits tools as {type:"function",function:{…}}
			// and the Messages API answers 400 "Input tag 'function'". The
			// provider name says nothing about the shape on the wire; the body
			// does.
			pipedBody = executor.EnsureClaudeMessagesFor(pipedBody, provider, model)
		}
		// The OpenAI→Claude conversion merges turns, so a client body whose
		// last message was a tool result can still land on an assistant turn.
		// A prefill is the client's own choice, so it is detected on the
		// pre-conversion body (upstream ensureTrailingUserTurn, claude.js:345-349).
		prefill := translator.ClaudeIntentionalPrefill(body)
		pipedBody = translator.EnsureTrailingUserTurnBody(pipedBody, prefill)

		// OAuth connections (or sk-ant-oat tokens) require Claude-Code-shaped requests:
		// billing-header system block + metadata.user_id + cloaked tools,
		// or the API 429s (anti-abuse fingerprinting).
		if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
			pipedBody = applyClaudeCloaking(pipedBody, apiKey, handlerutil.GetSessionID(ctx))
			var reqMap map[string]any
			if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
				claudeToolMap = cloakClaudeTools(reqMap)
				if out, err := json.Marshal(reqMap, json.Deterministic(true)); err == nil {
					pipedBody = out
				}
			}
			pipedBody = translator.AnchorClaudeCacheWithTTL(pipedBody, h.claudeCacheTTL())
		}
	}

	if isAnthropic || upstreamClaude {
		pipedBody = executor.SanitizeClaudeThinkingBody(pipedBody)
	}

	// A body that asked for thinking summaries must not also carry the
	// redact-thinking beta: it asks Anthropic for signature-only thinking blocks
	// and would blank exactly what the client requested. The header map is
	// shared with the registry, so this edits a request-local copy.
	if isAnthropic && thinking.BodyMentionsThinking(pipedBody) && executor.WantsThinkingSummaries(pipedBody) {
		providerCfg.StaticHeaders = providers.WithoutBetaFlag(
			providerCfg.StaticHeaders, providers.AnthropicBetaRedactThinking,
		)
	}

	if isAnthropic {
		// The caller's own beta flags are merged into the upstream request
		// instead of being dropped: a client asking for a beta the gateway does
		// not list would be refused without ever being told why. The registry
		// header map is shared, so this edits a request-local copy.
		if clientBeta := handlerutil.GetClientAnthropicBeta(ctx); clientBeta != "" {
			merged := providers.MergeAnthropicBeta(providerCfg.StaticHeaders["Anthropic-Beta"], clientBeta)
			if merged != "" {
				providerCfg.StaticHeaders = providers.WithAnthropicBeta(providerCfg.StaticHeaders, merged)
			}
		}

		// Claude OAuth wants x-claude-code-session-id to agree with the
		// metadata.user_id the cloak generates, otherwise the API sees two
		// different sessions for one request.
		if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
			if sid := claudeSessionIDFromBody(pipedBody); sid != "" {
				if providerCfg.StaticHeaders["x-claude-code-session-id"] == "" {
					providerCfg.StaticHeaders = providers.WithHeader(
						providerCfg.StaticHeaders, "x-claude-code-session-id", sid,
					)
				}
			}
		}
	}

	// The caller's own anthropic-version takes precedence over the registry
	// default on Anthropic's own providers: a client that pins a version keeps it
	// instead of being rewritten to 2023-06-01. Gated on the provider id (not
	// isAnthropicUpstream, which keys off the resolved BaseURL) so the merge
	// applies even when a connection points at a non-anthropic.com upstream, and
	// so minimax-code (provider id "minimax-code", which pins its own version)
	// stays isolated.
	if provider == "claude" || provider == "anthropic" {
		if clientVersion := handlerutil.GetClientAnthropicVersion(ctx); clientVersion != "" {
			providerCfg.StaticHeaders = providers.WithHeader(providerCfg.StaticHeaders, "anthropic-version", clientVersion)
		}
	}

	// Sanitize tool schemas for all OpenAI-compatible providers (opencode, gemini-openai, etc.)
	// Fixes misplaced `required` inside `properties` and missing `items` for arrays.
	if sanitized, err := translator.SanitizeOpenAITools(pipedBody); err == nil && sanitized != nil && string(sanitized) != string(pipedBody) {
		log.Debug("fallback", "sanitized tools", "provider", provider, "model", model, "conn", connectionID[:min(8, len(connectionID))], "beforeBytes", len(pipedBody), "afterBytes", len(sanitized))
		pipedBody = sanitized
	} else if err != nil {
		log.Warn("fallback", "sanitize failed", "provider", provider, "model", model, "error", err)
	}
	// DeepSeek answers 400 "Tool names must be unique" for a request that
	// declares the same tool twice, so same-name definitions are collapsed at
	// the last point before dispatch — after every conversion (OpenAI →
	// Claude for an Anthropic upstream included), like upstream
	// dedupeTools in open-sse/handlers/chatCore.js. Scoped by model id, so no
	// other provider's tool array is touched.
	if deduped := translator.DedupeToolsDeepSeek(pipedBody, model); len(deduped) != len(pipedBody) {
		log.Debug("fallback", "deduped duplicate tool names", "provider", provider, "model", model)
		pipedBody = deduped
	}
	// DeepSeek (and Kimi) thinking mode requires every assistant message echoed
	// back with a reasoning_content field whenever the request declares tools;
	// OpenAI-shaped clients drop it when replaying history, and the API answers
	// 400 ("The reasoning_content in the thinking mode must be passed back to
	// the API") without it. A placeholder satisfies the validator; reasoning the
	// client really sent is preserved. Port of upstream
	// utils/reasoningContentInjector.js, run at the same last-point-before-
	// dispatch stage as the tool collapse above. Internal provider/model rules
	// decide applicability (any provider serving a deepseek/kimi model id), so
	// the call itself is unconditional on non-Claude lanes.
	if !upstreamClaude {
		pipedBody = executor.InjectReasoningContent(pipedBody, provider)
		if provider == "deepseek" {
			// DeepSeek V4 models think by default and reject required/named
			// tool_choice while thinking is on ("Thinking mode does not
			// support this tool_choice"); downgrade the pin to auto so the
			// turn is served instead of failing with 400.
			pipedBody = executor.NormalizeDeepSeekThinkingToolChoice(pipedBody)
		}
	}

	// Fit tool names exceeding MaxToolNameLength (64 chars) to prevent upstream HTTP 400.
	fittedBody, fittedToolMap := translator.FitToolNamesForProvider(pipedBody, provider)
	if len(fittedToolMap) > 0 {
		log.Debug("fallback", "fitted long tool names", "provider", provider, "model", model, "count", len(fittedToolMap))
		pipedBody = fittedBody
		w = executor.NewToolNameRestoringWriter(w, fittedToolMap)
		ctx = translator.WithToolNameMap(ctx, fittedToolMap)
		if claudeToolMap == nil {
			claudeToolMap = make(map[string]string, len(fittedToolMap))
		}
		for k, v := range fittedToolMap {
			claudeToolMap[k] = v
		}
	}

	// G11: a target whose wire format cannot fetch a remote image would receive
	// the URL as a dead string. Done here rather than inside a translator
	// because this is the one place that knows both the target provider and the
	// client's own body shape. Fail-open: any refusal leaves the body as-is.
	pipedBody = prefetchImagesForTarget(ctx, provider, model, providerCfg, pipedBody)

	// Codex Fast intent: the account's stored preference is the fallback, and a
	// body that carries its own intent wins. Applied after every body rewrite
	// above so a saver or a prefetch cannot reorder or drop the service tier.
	var accountFastOpt *bool
	if connData != nil && connData.ProviderSpecificData != nil {
		if v, ok := connData.ProviderSpecificData["codexFastMode"].(bool); ok {
			accountFastOpt = &v
		}
	}
	if fastBody, modified := ApplyCodexFastMode(ctx, pipedBody, provider, accountFastOpt); modified {
		pipedBody = fastBody
	}

	// Unified thinking normalisation, outbound half (port of open-sse
	// applyThinking and filterToOpenAIFormat). Runs last on the wire body so
	// no converter can resurrect a dialect the target rejects. Claude-native
	// clients author their own Claude thinking and keep their existing
	// handling.
	//
	// The target format is the wire the upstream speaks, not the shape the
	// client sent: a Responses-native upstream (opencode, muse, a custom
	// node on a /responses endpoint) reads reasoning:{effort,summary} and
	// rejects a top-level reasoning_effort, so the pass runs there too —
	// upstream applies the thinking pass for every target format
	// (open-sse/translator/index.js:137). The mandatory-reasoning clamp is
	// part of the writer, so the Responses lane gets it as well.
	if !claudeNative {
		target := "openai"
		switch {
		case executor.UpstreamSpeaksResponses(provider, wireModel, providerCfg):
			target = "openai-responses"
		case executor.IsCopilotResponsesModel(provider, wireModel):
			target = "openai-responses"
		case isAnthropic || upstreamClaude:
			// A provider whose own endpoint speaks the Messages format
			// (minimax-code on the mavis gateway) gets the Claude wire: its
			// body was converted above, and the OpenAI filter would strip the
			// tool_use blocks and rewrite its tools into a shape the Messages
			// API rejects.
			target = "claude"
		}
		pipedBody = thinking.ApplyJSON(pipedBody, target, provider, wireModel, thinkingIntent)
		if target == "openai" && !providerCfg.IsGeminiNative() {
			filtered, filterErr := translator.FilterToOpenAIFormatJSON(pipedBody, translator.FilterOptions{
				PreserveCacheControl:    providers.PreservesCacheControl(provider) || translator.PreservesOpenRouterAnthropicCacheControl(provider, wireModel),
				StrictReasoningRemoving: providers.StrictReasoningRemoving(provider),
				FlattenContent:          provider == "cloudflare-ai",
			})
			if filterErr != nil {
				// The request lost every message. The upstream answers an
				// empty messages array with a 400, so the gateway says so
				// itself instead of spending the call.
				return &upstreamError{
					StatusCode: http.StatusBadRequest,
					Body:       []byte(`{"error":{"type":"invalid_request_error","message":"the request carries no messages"}}`),
				}
			}
			pipedBody = filtered

			// Cloudflare's OpenAI-compatible endpoint rejects tool-call rounds for
			// models it does not declare tool-capable (e.g. llama-3.1-8b-instruct-fp8-fast).
			// The gateway's default capability is Tools:true for unknown models, so
			// ask the static registry instead of GetCapabilitiesForModel: only models
			// explicitly marked Tools:true keep their tool definitions and turns.
			// Everything else gets the same prose flattening combo fusion already
			// uses for panel models, preserving context without emitting tool_calls.
			if provider == "cloudflare-ai" && !providers.ExplicitToolSupport(provider, wireModel) {
				pipedBody = stripToolsForNonToolModel(pipedBody)
			}
		}
	}

	// A custom openai-compatible node answers 400 "max_tokens and
	// max_completion_tokens cannot both be set" on a streaming request that
	// carries both (repro: bansos/gpt-6-luna on api.sakoo.biz.id — whose
	// backend is GitHub models). A client may spell either field; when both
	// are present the explicit max_completion_tokens value wins and
	// max_tokens is dropped, and when the client sent neither the upstream's
	// own default is honoured.
	//
	// The condition is the wire the body is in, not the client endpoint that
	// reached here: a /v1/responses client is converted to Chat Completions
	// before this point when the target speaks Chat (a custom
	// openai-compatible node), and that conversion keeps both fields as sent.
	// Gating on the endpoint alone left the pair on the wire for exactly the
	// lane this fix came from. A Claude body and a Responses body carry
	// neither field, so the normalizer is a no-op there.
	if !claudeNative && !upstreamClaude && !executor.UpstreamSpeaksResponses(provider, wireModel, providerCfg) {
		pipedBody = normalizeChatTokenFields(pipedBody)
		// Operator-declared extraBody for an OpenRouter custom model: merged
		// after every rewrite above so none of them can drop it.
		pipedBody = h.applyCustomExtraBody(pipedBody, provider, wireModel)
	}

	// Strict pattern validation: strip regex lookarounds and malformed patterns
	// that OpenAI, DeepSeek, and strict JSON Schema validators reject with HTTP 400.
	if !claudeNative && !upstreamClaude {
		pipedBody = sanitizeOutboundToolSchemas(pipedBody, provider)
	}

	start := time.Now()
	metrics := &streamMetrics{}
	var fwdErr error

	// The names behind this in-flight request. The forwarder carries both: for
	// a combo request RequestedModel is the combo the client asked for and
	// ComboName marks that a combo served it, while model/provider name the
	// member that is doing the work. The dashboard animates exactly that hop.
	requestedName := f.RequestedModel
	if requestedName == "" {
		requestedName = translator.RequestedModelFromContext(ctx)
	}
	comboName := f.ComboName
	usagetracker.GetTracker().TrackPendingRequested(requestedName, comboName, model, provider, connectionID, true, false)
	defer func() {
		hasErr := fwdErr != nil
		usagetracker.GetTracker().TrackPendingRequested(requestedName, comboName, model, provider, connectionID, false, hasErr)
	}()

	// A connection bound to a pool that cannot serve traffic must fail before
	// anything is sent: the first request is the one that would otherwise
	// escape from the real IP while the error only shows on the next attempt.
	httpClient, clientErr := h.getClientForConnection(connData)
	if clientErr != nil {
		// Marshalled, not concatenated: the pool name is user-supplied and a
		// quote in it produced a body that no longer parsed as JSON, so both
		// the client and the diagnostics reader saw "request failed" instead
		// of why the egress refused the request.
		errBody, mErr := json.Marshal(map[string]any{
			"error": map[string]string{"type": "proxy_error", "message": clientErr.Error()},
		})
		if mErr != nil {
			errBody = []byte(`{"error":{"type":"proxy_error","message":"proxy pool unavailable"}}`)
		}
		fwdErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: errBody}
		// Failing closed on an unusable pool is a failed upstream attempt, and
		// upstream records it: the same condition throws out of proxyFetch.js
		// ("Proxy required but failed"), inside executor.execute, so chatCore.js
		// catches it and writes both the requestDetails error row and the
		// isError TrackPending. Assigning fwdErr before returning is also what
		// makes the deferred TrackPending above report isError=true, which is
		// how the topology card learns this provider failed.
		connName, connEmail := identityNames(h.connIdentityKVOr(f, connectionID))
		h.LogFailure(
			ctx,
			&UsageLogInfo{
				Provider:              provider,
				Model:                 model,
				ConnectionID:          connectionID,
				ConnName:              connName,
				ConnEmail:             connEmail,
				Endpoint:              endpoint,
				Egress:                resolveEgress(connData, providerCfg).LogValue(),
				OriginalInputTokens:   origTokens,
				SavedTokens:           savedTokens,
				SavedPercent:          savedPct,
				CompressionDurationMs: compressDurMs,
			},
			nil,
			fwdErr,
			time.Since(start).Milliseconds(),
			body,
			metrics,
		)
		log.Warn("fallback", "upstream blocked by proxy egress", "provider", provider, "model", model, "error", clientErr)
		return fwdErr
	}
	// The client carries the strict-proxy marker itself, but the executors'
	// DoRequest path sees only ctx, so the decision travels on both. A
	// connection with nothing proxied at all is left unmarked: strict there
	// means "never replay this request directly", not "a proxy must exist".
	if connData != nil && (connData.ProxyPoolID != "" || connData.ConnectionProxyEnabled || connData.StrictProxy) {
		ctx = internalproxy.WithStrictProxy(ctx, true)
	}
	if providers.IsLocalByDesign(provider) {
		ctx = internalproxy.WithAllowLoopback(ctx)
	}
	// Per-connection opt-in for private-network dials (issue #840): a connection
	// pointed at a local gateway or LAN peer carries allowPrivateNetworking.
	// The flag rides on ctx so the dial guard honors it for this request only.
	if connData != nil && connData.AllowPrivateNetworking {
		ctx = internalproxy.WithAllowPrivateNetworking(ctx)
	}
	sessionID := handlerutil.GetSessionID(ctx)

	// The outbound guardrail tap goes here, not inside a response handler:
	// this is the one point every dispatch path shares, so a provider with a
	// registered executor is covered by exactly the same policy as one
	// without. Placing it per handler left every executor-backed provider —
	// which is most of the catalog — completely unfiltered, while the unit
	// tests, which exercise a provider with no executor, still passed.
	//
	// The resolved policy goes on the context so the executors pick it up
	// where they write, and a stream is tapped at the writer itself, because a
	// buffered body could still be judged whole but a stream cannot be taken
	// back once its first byte is out.
	ctx = h.withOutboundPolicy(ctx)
	gtapWriter, gtap := guardrailTap(ctx, w, endpoint, isStream)
	if gtap != nil {
		// Closed on the way out so the held window is discharged on the error
		// paths below as well as the success one.
		w = gtapWriter
		defer gtap.Close()
	}

	// Provider-scope capacity limits (docs/provider-limits-plan.md, PR 2). A custom
	// provider is one upstream endpoint shared by every connection under it, so its
	// RPM/TPM/concurrency are enforced here — once per attempt, against the body
	// about to be sent — rather than in a middleware: the provider is only known
	// after model resolution, and a refusal must fail over to the next candidate
	// instead of ending the request.
	//
	// The slot is released when this attempt ends, on every path including
	// failover. Holding it for the rest of the request would let one pass lock the
	// node out of the concurrency it was granted (D6).
	providerSlot, providerRefusal := h.acquireProviderAttempt(ctx, provider, model, connectionID, pipedBody)
	if providerRefusal != nil {
		return providerRefusal
	}
	if providerSlot != nil {
		defer providerSlot.Release()
	}

	// Hand the usage meter a way to reconcile the limiter's TPM reservations —
	// the caller's own and this provider's — against what the turn actually cost.
	//
	// Both are read back off the context rather than recomputed: the limiter
	// estimated from the body it had already read, and estimating twice from a
	// body that may have been rewritten since would settle the charge against a
	// number that was never charged.
	reserved, keyReserved := middleware.ReservedTokensFromContext(ctx)
	if (keyReserved || providerSlot != nil) && h.RateLimiter != nil {
		clientKey := requestKeyFromContext(ctx)
		ctx = middleware.WithTokenSettler(ctx, func(actual int) {
			if keyReserved && clientKey != nil {
				if limit := h.RateLimiter.EffectiveTPM(h.rateLimitDefaults(), clientKey); limit > 0 {
					h.RateLimiter.SettleTokenUsage(clientKey.ID, limit, reserved, actual)
				}
			}
			// The provider's TPM is charged prompt-side and settled with the
			// turn's prompt+completion total, which is the accounting the plan
			// chose (D7, decision 3).
			providerSlot.Settle(actual)
		})
	}

	// Per-model admission control. The provider breaker above answers "is this
	// provider healthy at all?", which is the wrong question when one model in
	// a healthy provider starts failing deterministically: opening the provider
	// parks every sibling with it. This gate is scoped to the pair, so a bad
	// model sits out its cooldown while the rest of the provider keeps serving.
	//
	// Placed here, after the connection and the body are resolved and before
	// the dispatch writes a single byte: a model that is out of rotation must
	// not queue behind the concurrency slot above, and must not leave a partial
	// response behind for the caller to retry. The refusal is an upstream error
	// the same shape a failing model would have produced, so the caller's
	// fallover, locking and usage-accounting paths all handle it unchanged.
	//
	// A probe (POST /api/models/test) is exempt: it exists to report what the
	// model actually does right now, and answering "cooling down" would tell the
	// operator about the breaker instead of the model they asked to test.
	if !handlerutil.IsProbeContext(ctx) && !modelBreakerAllowed(provider, model) {
		retryAfter, _ := modelBreakerRetryAfter(provider, model)
		fwdErr = modelBreakerUnavailable(retryAfter)
		log.Warn("fallback", "model breaker refused", append([]any{
			"provider", provider, "model", model, "conn", connectionID, "retryAfter", retryAfter.String(),
		}, h.connIdentityKVOr(f, connectionID)...)...)
		return fwdErr
	}

	// Inject served route response headers (x-9router-provider, x-9router-model, etc.)
	// before any executor or forwarding function writes headers to w.
	injectRouteHeaders(w, provider, model, f.ComboName, f.Attempts)

	// The context budget of the request that is about to be served, so a client
	// can see how much of the window this turn used.
	injectContextHeaders(w, resolveContextBudget(ctx, provider, model, pipedBody))

	if exec := executorFor(provider, connData); exec != nil {
		execReq := &executor.Request{
			Ctx:          ctx,
			Client:       httpClient,
			Config:       providerCfg,
			Provider:     provider,
			APIKey:       apiKey,
			Body:         pipedBody,
			IsStream:     isStream,
			ConnectionID: connectionID,
			AuthType:     authType,
			APIKeyID: func() string {
				if k := requestKeyFromContext(ctx); k != nil {
					return k.ID
				}
				return ""
			}(),
			SessionID:      sessionID,
			ToolNameMap:    claudeToolMap,
			ClaudeClient:   endpoint == "/v1/v1/messages" || endpoint == "/v1/messages",
			TranslateResp:  translateResponse,
			UpstreamClaude: upstreamClaude && !claudeNative,
			ResponseBuf:    &streamNoteWriter{w: &metrics.ResponseBuf, note: metrics.NoteOutput},
			StartTime:      start,
			TTFT:           &metrics.TTFT,
		}
		if provider == "agy-acp" {
			acpData := map[string]any{}
			if connData != nil {
				if raw, jerr := json.Marshal(connData); jerr == nil {
					_ = json.Unmarshal(raw, &acpData)
				}
			}
			execReq.ACPCredentials = func(ctx context.Context, _ map[string]any, cid string) (string, string, time.Time, error) {
				return h.resolveACPCredentials(ctx, acpData, cid)
			}
		}
		// client_id cloaking: hand the connection's providerSpecificData
		// (fingerprintId) to executors that mimic an official client.
		if connData != nil && len(connData.ProviderSpecificData) > 0 {
			execReq.ConnData = connData.ProviderSpecificData
		}
		// Cross-process session coordination (e.g. Freebuff leases): nil
		// when no repo (tests), so executors fall back to memory only.
		if h.Repo != nil {
			execReq.Leases = h.Repo
		}
		fwdErr = exec(w, execReq)
		execReq.Release()
	} else if providerCfg.IsGeminiNative() {
		fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics, httpClient, start)
	} else {
		fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics, httpClient, provider, upstreamClaude, claudeNative)
	}

	// A tap that cut the stream must win over whatever the dispatch returned:
	// the dispatch succeeded as far as it knew, so a blocked stream would
	// otherwise be logged, billed, and cooled down as a served turn.
	fwdErr = guardrailStreamOutcome(gtap, fwdErr)

	var ue *upstreamError
	// An account that is already parked for a dead grant never gets a forced
	// refresh: the refresh has already been refused, and re-spending the
	// single-use token on the same request would only log the rejection again
	// and re-park. The 401 stands and the account loop below moves on.
	// A custom JSON business endpoint has no OAuth token to refresh, so it is
	// excluded from the reactive refresh as well. credentialRejected widens the
	// trigger from a bare 401 to a 403 whose body names the token, which is how
	// Kiro reports an expired access token.
	if errors.As(fwdErr, &ue) && credentialRejected(ue) && connectionID != "" && !customJSON && !connectionParkedForReauth(h, connectionID) {
		refreshedKey, _, rErr := h.forceRefreshOAuthToken(connectionID)
		if rErr == nil && refreshedKey != "" && refreshedKey != apiKey {
			log.Info("fallback", "reactive 401 token refresh success, retrying request", "conn", connectionID)
			apiKey = NormalizeProviderToken(provider, refreshedKey)
			if exec := executorFor(provider, connData); exec != nil {
				retryReq := &executor.Request{
					Ctx:           ctx,
					Client:        httpClient,
					Config:        providerCfg,
					Provider:      provider,
					APIKey:        apiKey,
					Body:          pipedBody,
					IsStream:      isStream,
					ClaudeClient:  endpoint == "/v1/v1/messages" || endpoint == "/v1/messages",
					TranslateResp: translateResponse,
					ConnectionID:  connectionID,
					AuthType:      authType,
					APIKeyID: func() string {
						if k := requestKeyFromContext(ctx); k != nil {
							return k.ID
						}
						return ""
					}(),
					SessionID:      sessionID,
					ToolNameMap:    claudeToolMap,
					UpstreamClaude: upstreamClaude && !claudeNative,
					ResponseBuf:    &streamNoteWriter{w: &metrics.ResponseBuf, note: metrics.NoteOutput},
					StartTime:      start,
					TTFT:           &metrics.TTFT,
				}
				if provider == "agy-acp" {
					acpData := map[string]any{}
					if connData != nil {
						if raw, jerr := json.Marshal(connData); jerr == nil {
							_ = json.Unmarshal(raw, &acpData)
						}
					}
					retryReq.ACPCredentials = func(ctx context.Context, _ map[string]any, cid string) (string, string, time.Time, error) {
						return h.resolveACPCredentials(ctx, acpData, cid)
					}
				}
				// Same client_id cloaking as the first attempt: a refreshed
				// retry without ConnData would fall back to a random id and
				// re-brand the request mid-conversation.
				if connData != nil && len(connData.ProviderSpecificData) > 0 {
					retryReq.ConnData = connData.ProviderSpecificData
				}
				if h.Repo != nil {
					retryReq.Leases = h.Repo
				}
				fwdErr = exec(w, retryReq)
				retryReq.Release()
			} else if providerCfg.IsGeminiNative() {
				fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics, httpClient, start)
			} else {
				fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics, httpClient, provider, upstreamClaude, claudeNative)
			}
		}
	}

	// Codex answers an exhausted 5h/7d window with a 429 carrying
	// usage_limit_reached and the exact reset. Caching it lets the next
	// picker pass skip this account instead of spending a request to
	// rediscover the same 429 (mirrors the Antigravity quota cache).
	//
	// When the body carries no reset, read live quota instead — throttled to
	// one wham call per 30s per connection. That only blocks the account if
	// the reading is actually exhausted, so a per-request or burst 429 on an
	// otherwise healthy account caches a healthy reading and blocks nothing.
	if errors.As(fwdErr, &ue) && provider == "codex" && connectionID != "" && ue.StatusCode == http.StatusTooManyRequests {
		if resetAt := NoteCodexQuotaError(connectionID, ue.StatusCode, ue.Body); resetAt != nil {
			log.Info("fallback", "codex quota exhausted, cached until reset", "conn", shortConnID(connectionID), "resetAt", resetAt.Format(time.RFC3339))
		} else if _, qErr := RefreshCodexQuota(ctx, httpClient, connectionID, apiKey); qErr != nil {
			log.Debug("fallback", "codex quota refresh failed", "conn", shortConnID(connectionID), "error", qErr)
		}
	}

	latencyMs := time.Since(start).Milliseconds()

	// Lightweight request trace for /debug/traces (provider/model latency).
	completed := fwdErr == nil
	if !completed && isClientCanceled(ctx, fwdErr) && metrics != nil && metrics.ResponseBuf.Len() > 0 {
		completed = true
	}
	if !completed && (metrics == nil || metrics.ResponseBuf.Len() == 0) {
		clearRouteHeaders(w)
	}

	status := "error"
	var zc *internalproxy.ZeroContentError
	if completed {
		status = strconv.Itoa(http.StatusOK)
	} else if isClientCanceled(ctx, fwdErr) {
		status = strconv.Itoa(StatusClientClosedRequest)
	} else if errors.As(fwdErr, &zc) {
		status = strconv.Itoa(http.StatusOK)
	} else if ue, ok := fwdErr.(*upstreamError); ok && ue.StatusCode > 0 {
		status = strconv.Itoa(ue.StatusCode)
	}
	tracing.Record(tracing.Span{
		Provider:   provider,
		Model:      model,
		Status:     status,
		DurationMs: latencyMs,
		TTFTMs:     metrics.TTFT,
	})

	usage := translator.GetAndClearUsage(ctx)
	if completed {
		// A dashboard "Test" probe (POST /api/models/test) is dispatched into
		// this very handler, and it reaches the provider for real — but it is
		// not operator traffic. Recording it wrote a usageHistory row and a
		// byProvider bucket, so the probed provider appeared on the Usage
		// Overview topology map having "served" a request nobody sent, and
		// the daily totals counted a token bill that was never metered
		// (#280). Every other probe side effect here is already gated on the
		// context; this is the one that was not.
		if handlerutil.IsProbeContext(ctx) {
			fwdErr = nil
			return nil
		}
		// Clear any existing model lock on success (matching Next.js clearAccountError).
		lockKey := canonicalLockModel(provider, model)
		if unlockErr := h.Repo.UnlockConnectionModel(ctx, connectionID, lockKey); unlockErr != nil {
			log.Warn("fallback", "unlock failed", "provider", provider, "model", lockKey, "error", unlockErr)
		}
		if lockKey != model {
			_ = h.Repo.UnlockConnectionModel(ctx, connectionID, model)
		}
		// A served request also clears the account-scoped cooldown, so an
		// account that recovered is not kept out of rotation until the
		// cooldown expires on its own.
		if clearErr := h.Repo.ClearConnectionRateLimit(ctx, connectionID); clearErr != nil {
			log.Warn("fallback", "rate limit clear failed", "conn", connectionID, "error", clearErr)
		}
		// A served request proves the account is usable again, so drop the
		// account-ban failure streak: the next ban starts from the base
		// cooldown rather than climbing the ladder.
		if clearErr := h.Repo.ClearConnectionAccountBanLock(connectionID); clearErr != nil {
			log.Warn("fallback", "account ban clear failed", "conn", connectionID, "error", clearErr)
		}
		// A served request proves the account is usable again, so drop any
		// cached quota block rather than leaving it to expire on its own.
		if provider == "codex" {
			ClearCodexQuotaBlock(connectionID)
		}
		// A served request proves the model is alive, so a badge recorded by
		// an earlier 410 must not outlive it (#179).
		h.clearModelDeprecation(provider, model)
		// The same verdict for the per-model breaker: the model answered, so
		// its failure count and any cooldown escalation start over. Without
		// this a model that recovered on its own would still pay for the
		// outage it already served through.
		modelBreakerSuccess(provider, model)
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		reqModel := f.RequestedModel
		if reqModel == "" {
			reqModel = translator.RequestedModelFromContext(ctx)
		}
		if reqModel == "" {
			reqModel = model
		}
		protocol := f.Protocol
		if protocol == "" {
			protocol = resolveProtocol(endpoint)
		}
		startedAt := f.StartedAt
		if startedAt.IsZero() {
			startedAt = time.Now().Add(-time.Duration(latencyMs) * time.Millisecond)
		}

		logInfo := &UsageLogInfo{
			Provider:              provider,
			Model:                 model,
			RequestedModel:        reqModel,
			ComboName:             f.ComboName,
			Protocol:              protocol,
			CacheSource:           resolveCacheSource(usage),
			StartedAt:             startedAt,
			ConnectionID:          connectionID,
			APIKey:                apiKey,
			Endpoint:              endpoint,
			Egress:                resolveEgress(connData, providerCfg).LogValue(),
			OriginalInputTokens:   origTokens,
			SavedTokens:           savedTokens,
			SavedPercent:          savedPct,
			CompressionDurationMs: compressDurMs,
		}
		logInfo.ConnName, logInfo.ConnEmail = identityNames(h.connIdentityKVOr(f, connectionID))
		h.logUsage(ctx, logInfo, usage, latencyMs, body, metrics)
		metrics.ResponseBuf.Reset()
		fwdErr = nil
		return nil
	}

	// Consume any terminal usage even when streaming aborts, then persist one
	// failure row. A later fallback attempt gets its own usage holder because
	// the outer handler installs capture once and tryForward never replaces it.
	statusCode := 0
	if errors.As(fwdErr, &ue) {
		statusCode = ue.StatusCode
	} else if errors.As(fwdErr, &zc) {
		statusCode = http.StatusOK
	}
	identity := h.connIdentityKVOr(f, connectionID)
	// Every failure line below carries the egress: a 401/429 from a provider
	// that rate-limits by IP is only diagnosable if the log says whether the
	// request actually left through the pool.
	identity = append(identity, "egress", resolveEgress(connData, providerCfg).LogValue())
	connName, connEmail := identityNames(identity)
	reqModel := f.RequestedModel
	if reqModel == "" {
		reqModel = translator.RequestedModelFromContext(ctx)
	}
	if reqModel == "" {
		reqModel = model
	}
	protocol := f.Protocol
	if protocol == "" {
		protocol = resolveProtocol(endpoint)
	}
	startedAt := f.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().Add(-time.Duration(latencyMs) * time.Millisecond)
	}

	h.LogFailure(
		ctx,
		&UsageLogInfo{
			Provider:              provider,
			Model:                 model,
			RequestedModel:        reqModel,
			ComboName:             f.ComboName,
			Protocol:              protocol,
			StartedAt:             startedAt,
			ConnectionID:          connectionID,
			ConnName:              connName,
			ConnEmail:             connEmail,
			APIKey:                apiKey,
			Endpoint:              endpoint,
			Egress:                resolveEgress(connData, providerCfg).LogValue(),
			OriginalInputTokens:   origTokens,
			SavedTokens:           savedTokens,
			SavedPercent:          savedPct,
			CompressionDurationMs: compressDurMs,
		},
		usage,
		fwdErr,
		latencyMs,
		body,
		metrics,
	)
	metrics.ResponseBuf.Reset()
	if isClientCanceled(ctx, fwdErr) {
		log.Info("fallback", "client canceled request", append([]any{
			"provider", provider, "model", model,
		}, identity...)...)
	} else if projectProbeCached(connectionID) {
		log.Debug("fallback", "upstream skipped (cached no-project)", append([]any{
			"provider", provider, "model", model, "error", fwdErr,
		}, identity...)...)
	} else if errors.As(fwdErr, &zc) {
		log.Warn("fallback", "upstream returned zero usable content", append([]any{
			"provider", provider, "model", model,
			"status", statusCode,
		}, identity...)...)
	} else {
		log.Warn("fallback", "upstream failed", append([]any{
			"provider", provider, "model", model,
			"status", statusCode, "error", fwdErr,
		}, identity...)...)
	}

	if !errors.As(fwdErr, &zc) {
		observ.IncUpstreamError(provider, model, statusCode)
	}
	// The per-model verdict on the same failure. Only a model-scoped class is
	// charged (modelBreakerFailure drops the rest), so a provider-scoped 5xx or
	// a transport error stays with the provider breaker above and a 429 stays
	// with the account-rotation layer — charging any of them here would park
	// every model of a briefly-down provider, which is the failure this layer
	// exists to prevent.
	//
	// A transport failure carries no status; it is charged as 503, which is the
	// same verdict the provider breaker in forward.go gives it, and its scope
	// is provider, so the classification drops it before anything is counted.
	if errors.As(fwdErr, &ue) && !ue.Local && !isClientCanceled(ctx, fwdErr) {
		modelBreakerFailure(provider, model, ue.StatusCode, ue.Body)
	}
	// A client that hung up is not an upstream failure. Returning the raw error
	// here made every abort surface as a bare 502 at the write sites in chat.go:
	// the caller saw "upstream error: context canceled" and the request log
	// recorded a 5xx for a disconnect the gateway did not cause. The combo path
	// already reports this as 499, so the two agree.
	if isClientCanceled(ctx, fwdErr) {
		return &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(clientClosedBody)}
	}
	// The gateway's own end-to-end bound expiring is a gateway timeout, not a
	// lost upstream socket: the client needs to know the request was cut by
	// policy rather than by a broken connection, because only one of those is
	// worth retrying unchanged.
	//
	// Local is set for the same reason a full local queue sets it: the account
	// was never exercised, the gateway simply stopped waiting. Without it the
	// 504 would fall into the gateway-fault branch of recordModelCooldown and
	// park a healthy connection for a delay the operator configured.
	if isRequestDeadline(ctx) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &upstreamError{StatusCode: http.StatusGatewayTimeout, Body: requestDeadlineBody(), Local: true}
	}
	return fwdErr
}

// requestDeadlineBody is the JSON body that travels with a 504 caused by the
// gateway's end-to-end request bound.
func requestDeadlineBody() []byte {
	return []byte(`{"error":{"message":"request exceeded the gateway's total request timeout","type":"server_error","code":"gateway_timeout"}}`)
}

// isClientCanceled reports whether the client that asked for this response
// went away. A deadline the gateway set for itself is not that: the dashboard
// model probe wraps its own call in a 45s timeout, and treating that expiry as
// a hangup reported "HTTP 499: client closed request" for a model that was
// merely still queued upstream. Only an explicit cancel counts, plus the 499
// this package already produced.
func isClientCanceled(ctx context.Context, err error) bool {
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return true
	}
	var ue *upstreamError
	if errors.As(err, &ue) && ue.StatusCode == StatusClientClosedRequest {
		return true
	}
	return false
}

// applyTokenSavers runs RTK compression and prompt injection on the request body.
// claudeNative indicates the body is in Claude Messages format (endpoint
// /v1/messages): system prompts must go to the top-level "system" field —
// a role:"system" message is rejected by the Anthropic API.
// false from compress/inject means nothing changed (or unparseable) — keep original, not a failure.
// recordPersonaInjection books a persona prompt injection into the running
// token totals.
//
// A style prompt does not compress anything: it adds a system prompt the
// provider is about to be billed for. The totals therefore track the body the
// provider receives — OriginalInputTokens rises by the prompt's own size, and
// SavedTokens falls by the same amount, because nothing was saved.
//
// Without this, a request that only had Caveman/Ponytail/ADHD switched on
// left both totals at 0, and the usage page skipped the analytics row
// entirely: the operator saw no compression activity on an instance where a
// saver demonstrably ran on every request.
func recordPersonaInjection(origTokens, savedTokens *int, beforeLen, afterLen int) {
	if afterLen <= beforeLen {
		return
	}
	added := (afterLen - beforeLen) / 4
	if added <= 0 {
		return
	}
	*origTokens += added
	*savedTokens -= added
	if *savedTokens < 0 {
		*savedTokens = 0
	}
}
