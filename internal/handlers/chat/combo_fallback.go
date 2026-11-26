package chat

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/tokensaver"
	"9router/proxy/internal/translator"
)

func isRetryableComboError(ue *upstreamError) bool {
	if ue == nil {
		return false
	}
	if ue.StatusCode >= 400 && ue.StatusCode < 500 &&
		ue.StatusCode != http.StatusTooManyRequests &&
		!providers.FreePiIsConcurrentSessionCode(ue.Body) {
		return false
	}
	return true
}

// isRequestScopedComboError reports whether the failure is about the request
// rather than about the account. Advancing to the next member is still worth
// trying — another provider may accept what this one rejected — but when every
// member refuses, the first such refusal is the only failure that tells the
// client what to change, so it is the one to return.
//
// The carve-outs are the statuses whose verdict is about the credential or the
// quota, not the request: 401/402/403 and 429 are in
// providers.RetryableStatusCodes, so the remaining connections of that account
// must still be tried, and a later member's 5xx must stay the answer when none
// of them can serve. Re-sending a true request-scoped 4xx to those connections
// cannot help: the request is what was rejected.
func isRequestScopedComboError(ue *upstreamError) bool {
	if ue == nil || ue.StatusCode < 400 || ue.StatusCode >= 500 {
		return false
	}
	// free-pi's `concurrent_session` is a 409 whose verdict is about the
	// ACCOUNT'S capacity, not about the request: the account already holds its
	// maximum number of live sessions, so another account can serve this exact
	// request unchanged. Treating it as request-scoped ended the pass and
	// returned the conflict to the client, which is why a second free-pi account
	// never picked up a mid-conversation switch. Keyed on the envelope `code`
	// rather than the status: 409 is a generic conflict and only this provider's
	// session code means "try another account".
	if ue.StatusCode == http.StatusConflict && providers.FreePiIsConcurrentSessionCode(ue.Body) {
		return false
	}
	return !providers.RetryableStatusCodes[ue.StatusCode]
}

// noteRequestScoped records the pass's first request-scoped refusal when ue is
// one and none was recorded yet. The first wins: later members are a different
// provider's opinion about the same request, and a 5xx from one of them says
// nothing a client could act on.
func noteRequestScoped(slot **upstreamError, ue *upstreamError) {
	if *slot == nil && isRequestScopedComboError(ue) {
		*slot = ue
	}
}

func (h *ChatHandler) comboFallbackCombos(comboName string) []string {
	if h.Repo == nil || comboName == "" {
		return nil
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		log.Warn("combo", "comboFallbackCombos GetSettings failed", "error", err)
		return nil
	}
	cfg, ok := settings.ComboStrategies[comboName]
	log.Info("combo", "comboFallbackCombos check", "comboName", comboName, "ok", ok, "fallbackCombos", cfg.FallbackCombos)
	if !ok {
		return nil
	}
	return cfg.FallbackCombos
}

// detectNewTurn reports whether the request body starts a new conversation
// turn. A turn boundary is the most recent plain-text user message; a request
// whose last user-type message is a tool result continues the current turn,
// and the combo must not switch providers mid-turn (Gemini thinking models
// require a thought_signature on every current-turn functionCall, which only
// the model that made the call can provide).

func keysString(m map[string]bool) string {
	return strings.Join(slices.Sorted(maps.Keys(m)), ",")
}

// upstreamErrorBody renders the terminal error a combo answers with when no
// member could serve the turn.
//
// The message is encoded by json.Marshal, never interpolated into a template:
// an upstream failure routinely carries characters that are illegal raw inside
// a JSON string — a URL that failed to parse embeds itself in quotes, a Windows
// path carries backslashes — and a format string emits them verbatim. The
// result is a body no client can parse: the first quote in the error text
// closes the string early and the following backslash becomes a bare token,
// which is what a JavaScript client reports as
// `JSON Parse error: Unrecognized token '\'`.
func upstreamErrorBody(message string) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    http.StatusBadGateway,
		},
	})
	if err != nil {
		return []byte(`{"error":{"message":"upstream error","type":"upstream_error","code":502}}`)
	}
	return body
}

// comboLastTried tracks the index of the last entry a pass over models can
// still send a request for. The loop skips entries that do not resolve, a
// provider whose model is locked, and a provider whose every connection is
// excluded, cooling down or parked — none of which is knowable from
// len(models)-1. Keying the per-candidate timeout and the zero-content probe to
// that index cut off the one remaining attempt whenever the stored tail was
// skipped (#776).
//
// The answer also changes while the pass runs: a retryable failure locks a
// connection and adds it to excludeIDs, so an entry that was sendable when the
// pass started may be unreachable by the time the loop reaches it. Erring late
// is the failure — it arms the timer on the entry that really is last — so the
// index is recomputed whenever the exclusion set grows rather than once before
// the loop.
type comboLastTried struct {
	h      *ChatHandler
	models []string
	seen   []string
	idx    int
	valid  bool
}

// newComboLastTried returns a tracker for one pass over models.
func newComboLastTried(h *ChatHandler, models []string) *comboLastTried {
	return &comboLastTried{h: h, models: models, idx: -1}
}

// index reports the last sendable entry for the pass's current exclusions,
// recomputing only when they have changed since the last call.
func (t *comboLastTried) index(excludeIDs []string) int {
	if t.valid && slices.Equal(t.seen, excludeIDs) {
		return t.idx
	}
	t.seen = slices.Clone(excludeIDs)
	t.idx = t.lastSendableComboIndex(t.seen)
	t.valid = true
	return t.idx
}

// lastSendableComboIndex returns the index of the last entry a request could
// still be sent for under excludeIDs, or -1 when none can be.
func (t *comboLastTried) lastSendableComboIndex(excludeIDs []string) int {
	cache := make(map[string][]*models.ProviderConnection, len(t.models))
	for i := len(t.models) - 1; i >= 0; i-- {
		modelInfo := t.h.resolveModelEntry(t.models[i])
		if modelInfo == nil {
			continue
		}
		if t.h.comboEntrySendable(modelInfo, excludeIDs, cache) {
			return i
		}
	}
	return -1
}

// comboEntrySendable reports whether a pass could still send a request for one
// combo entry under excludeIDs. It mirrors the three skips the loop performs
// before forwarding: a provider with no active connection, a provider whose
// model is locked, and a provider whose every connection is excluded or
// cooling down. A provider that needs no credentials is always sendable, the
// same way the loop synthesizes its connection instead of selecting one.
//
// The connection query is memoised per pass: the predicate runs for every entry
// on every exclusion change, and re-querying a provider's rows each time would
// put the database back in the hot loop this tracker exists to keep it out of.
func (h *ChatHandler) comboEntrySendable(modelInfo *ModelInfo, excludeIDs []string, cache map[string][]*models.ProviderConnection) bool {
	if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
		return true
	}
	if h.Repo == nil {
		return true
	}
	conns, ok := cache[modelInfo.Provider]
	if !ok {
		var err error
		conns, err = h.Repo.GetProviderConnections(modelInfo.Provider, true)
		if err != nil {
			// An unreadable table is not evidence that the entry cannot run:
			// let the loop find out, which keeps this predicate from turning a
			// transient database error into a shorter combo.
			conns = nil
		}
		if len(conns) == 0 {
			if fallbackProvider, has := CredentialFallbacks[modelInfo.Provider]; has {
				conns, _ = h.Repo.GetProviderConnections(fallbackProvider, true)
			}
		}
		cache[modelInfo.Provider] = conns
	}
	if len(conns) == 0 {
		// No connection at all: the loop's getBestConnectionWithContext fails
		// and it moves on, so this entry cannot be the last one tried.
		return false
	}
	excluded := make(map[string]bool, len(excludeIDs))
	for _, id := range excludeIDs {
		excluded[id] = true
	}
	now := time.Now()
	for _, c := range conns {
		if c == nil || excluded[c.ID] {
			continue
		}
		if h.connectionModelBlocked(c, modelInfo.Provider, modelInfo.Model) {
			continue
		}
		if until, ok := db.ConnectionBlockedUntil(c.Data); ok && until.After(now) {
			continue
		}
		return true
	}
	return false
}

// handleComboFallback iterates through combo model entries, trying each one.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
// adapterModels holds the capacity-adapter entries injected for this request;
// their history is trimmed to their context window before forwarding.
func (h *ChatHandler) handleComboFallback(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, adapterModels ...string) {
	if FallbackAttemptRecorderFrom(ctx) == nil {
		ctx = WithFallbackAttemptRecorder(ctx, NewFallbackAttemptRecorder())
	}
	// Inject the combo's own custom system prompt before any model attempt, so
	// every member of the combo carries the same operator instructions. When
	// set, the global system prompt prefix/suffix is skipped for this request
	// (the combo prompt replaces it, it does not stack on top).
	// handleComboFallback serves /v1/chat/completions, so the body is OpenAI
	// format: inject into messages[] with claudeNative=false.
	if h.Repo != nil && comboName != "" {
		if combo, err := h.Repo.GetComboByName(comboName); err == nil && combo != nil && combo.CustomPrompt != "" {
			if next, did := tokensaver.InjectPrefixSystemPrompt(body, combo.CustomPrompt, false); did {
				body = next
				ctx = WithComboCustomPrompt(ctx)
			}
		}
	}
	cw := newCommittedResponseWriter(w)
	cw.stream = isStream
	cw.responsesClient = translator.IsResponsesClient(ctx)
	h.handleComboFallbackWithVisited(ctx, cw, body, comboModels, strategy, isStream, translateResponse, comboName, stickyLimit, nil, true, adapterModels...)
}

func (h *ChatHandler) handleComboFallbackWithVisited(ctx context.Context, cw *committedResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, visited map[string]bool, isRoot bool, adapterModels ...string) {
	var lastErr *upstreamError
	// firstRequestScoped is the first member failure that names the request
	// rather than the account. When every member refuses, that refusal is the
	// one the client can act on (#5057).
	var firstRequestScoped *upstreamError
	// noReplay ends the turn after a custom JSON business attempt, whose
	// operation must never be repeated against another connection or model.
	var noReplay bool
	// inbandError records that the current candidate committed a
	// provider-injected in-band error frame. The chain must stop: no failover
	// to subsequent models, no retry pass.
	var inbandError bool
	var retry passRetry
	responseTimeout, timeoutGrace := h.comboTimeoutPolicy(comboName)
	modelTimedOut := false
	// Connections that failed with a retryable status this request; remaining
	// combo models must not re-select them (same account = same 429 quota).
	var excludeIDs []string

	var settings, _ = h.Repo.GetSettings()

	var models []string
	comboModels, adapterModels = h.filterAvailableComboModels(ctx, comboModels, adapterModels)
	if len(comboModels) == 0 {
		lastErr = allComboModelsUnavailableError()
	} else {
		// 1. Apply combo rotation strategy first. The smart strategy orders the
		// models itself (System One evaluation + session stickiness) and carries
		// its routing state on the context for the failure loop below.
		//
		// RespectStoredOrder skips the rotation and walks the stored list as-is,
		// so the order the operator arranged in the editor is the fallback chain.
		// The smart, tokenmaxxing, and cascade strategies are exempt: they compute
		// dynamic ranking (by task suitability, quota allowance, and unit price
		// respectively), so honoring stored order would defeat the evaluation they
		// exist to perform.
		if strategy == "smart" {
			ctx, models = h.applySmartRouting(ctx, body, comboModels, comboName)
		} else if strategy == "tokenmaxxing" {
			models = h.applyTokenmaxxingRouting(ctx, comboModels)
		} else if strategy == "cascade" {
			models = OrderModelsByCascade(comboModels)
		} else if h.comboRespectsStoredOrder(comboName) {
			models = slices.Clone(comboModels)
		} else {
			var comboOwn []string
			if len(adapterModels) > 0 && len(comboModels) >= len(adapterModels) {
				comboOwn = comboModels[len(adapterModels):]
			}
			models = h.applyComboStrategyForConversation(strategy, comboModels, comboName, stickyLimit, detectNewTurn(body), conversationFingerprint(body), comboOwn)
		}

		// 2. Auto-capability-switch: float models that satisfy the request's required capabilities to the front.
		// This ensures that if the rotated model lacks required capabilities (e.g., vision), a capable model overrides it.
		if required := DetectRequiredCapabilities(body); len(required) > 0 && len(models) > 0 {
			reordered := ReorderByCapabilities(models, required)
			if len(reordered) > 0 && reordered[0] != models[0] {
				log.Info("combo", "auto-switch", "caps", keysString(required), "model", reordered[0])
			}
			models = reordered
		}
		// A model that recently timed out sits out its grace period.
		models = h.withoutBenchedModels(comboName, models, timeoutGrace)
	}

	// If every model fails, retry the whole pass once after a bounded
	// Retry-After wait so a transient provider blip doesn't surface as a hard
	// 429 to the client.
	for attempt := 0; attempt < 2 && len(models) > 0; attempt++ {
		if attempt > 0 {
			wait := retry.wait()
			if wait == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			// Fresh pass: re-allow connections locked by the previous attempt
			// (their cooldown has elapsed) and clear the error state.
			lastErr = nil
			firstRequestScoped = nil
			retry.reset()
			excludeIDs = nil
		}

		// The entry tried last is the last one this pass can actually send a
		// request for, not the last one stored: unresolved entries, a
		// model-locked provider and a provider with nothing left to dial are
		// all skipped before anything is forwarded. The tracker recomputes
		// when this pass excludes another connection, so a member that the
		// pass itself takes out of reach stops counting as the last one.
		lastTried := newComboLastTried(h, models)

		for i, entry := range models {
			modelInfo := h.resolveModelEntry(entry)
			if modelInfo == nil {
				continue
			}
			isLastModel := i == lastTried.index(excludeIDs)

			// Skip unavailable (model-locked) providers
			if !h.Repo.IsProviderAvailable(ctx, modelInfo.Provider, modelInfo.Model) {
				log.Warn("combo", "skip unhealthy", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"all connections for this provider are rate-limited","type":"rate_limit_error","code":429}}`)}
				}
				continue
			}

			candidateCtx := ctx
			var seatBudgetMs int
			if settings != nil {
				seatBudgetMs = settings.SeatAttemptBudgetMs
				if settings.ProviderOverrides != nil {
					if ov, ok := settings.ProviderOverrides[modelInfo.Provider]; ok && ov.SeatBudgetMs > 0 {
						seatBudgetMs = ov.SeatBudgetMs
					}
				}
			}
			if seatBudgetMs > 0 {
				candidateCtx = internalproxy.WithSeatBudget(candidateCtx, seatBudgetMs)
			}
			candidateWriter := http.ResponseWriter(cw)
			// The candidate tried last keeps the global deadline: the response
			// timeout only advances a combo, it never makes one fail sooner
			// than it would without the setting (#744).
			var timeoutGate *comboResponseTimeout
			if responseTimeout > 0 && !isLastModel {
				timeoutGate = newComboResponseTimeout(ctx, cw, responseTimeout)
				defer timeoutGate.finish()
				candidateCtx, candidateWriter = timeoutGate.ctx, timeoutGate
			}
			var entrySuccess bool
			// Connections this entry already sent the request to. A failure
			// that does not lock the account (a 400, a 500, a busy endpoint)
			// left it selectable, so without this the next connection attempt
			// picked the same account and resent the same request.
			var tried []string
			// Try up to 10 connections for this model entry
			for connAttempt := 0; connAttempt < 10; connAttempt++ {
				var connID string
				var picked *ProviderConnection
				var connData *ConnectionData
				isKnownNoAuth := false
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					isKnownNoAuth = true
					connData = &ConnectionData{
						APIKey:      cfg.DefaultAPIKey,
						Provider:    modelInfo.Provider,
						ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				} else {
					var cData *ConnectionData
					var err error
					picked, cData, err = h.getBestConnectionWithContext(candidateCtx, modelInfo.Provider, modelInfo.ConnectionID, entryExcludes(excludeIDs, tried), modelInfo.Model)
					if err != nil {
						// A node the gateway refuses on purpose — expired contract,
						// spent lifetime quota — must reach the client as that
						// refusal rather than as an anonymous "all combo models
						// failed": an operator can act on the first and not on the
						// second.
						if refusal, blocked := providerLimitRefusal(err); blocked {
							log.Info("combo", "provider node unusable, next candidate",
								"provider", modelInfo.Provider, "model", modelInfo.Model, "axis", refusal.Axis)
							var refusedErr *upstreamError
							errors.As(err, &refusedErr)
							if refusedErr != nil {
								lastErr = refusedErr
							}
						}
						break
					}
					connID = picked.ID
					connData = cData
				}
				// Skip a connection that is already locked for this model.
				// picked.Data is already in memory — the repo call re-fetched
				// and re-parsed the same row per connection attempt.
				if picked != nil {
					lockKey := canonicalLockModel(modelInfo.Provider, modelInfo.Model)
					locked := db.ConnectionModelLockedData(picked.Data, lockKey)
					if !locked && lockKey != modelInfo.Model {
						locked = db.ConnectionModelLockedData(picked.Data, modelInfo.Model)
					}
					if locked {
						log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "lockKey", lockKey, "conn", picked.ID)
						excludeIDs = append(excludeIDs, picked.ID)
						continue
					}
				}

				var upstreamBody map[string]any
				upstreamBodyJSON := adapterTrimmedBody(body, entry, adapterModels)

				if err := json.Unmarshal(upstreamBodyJSON, &upstreamBody); err != nil {
					handlerutil.WriteJSONError(cw, http.StatusBadRequest, "failed to parse request body")
					return
				}
				upstreamBody["model"] = h.wireModelForNode(modelInfo.Provider, modelInfo.Model)
				repairToolCallIDsInMap(upstreamBody)
				candidateCtx = h.applyJailbreak(candidateCtx, upstreamBody, comboName, jailbreakLegIDs(modelInfo.Provider, modelInfo.Model)...)
				upstreamJSON, err := json.Marshal(upstreamBody, json.Deterministic(true))
				if err != nil {
					break
				}

				var fwdErr error
				fwdCtx := candidateCtx
				targetWriter := candidateWriter
				var streamPeeker *internalproxy.StreamContentPeeker
				customJSON := isCustomJSONConn(connData)
				// The response timeout advances a combo to the next member,
				// which for a business endpoint would replay its operation.
				// Its member runs to completion instead; a gate that already
				// fired leaves the request unsent and falls back as usual.
				if customJSON && timeoutGate != nil {
					timeoutGate.disarm()
				}
				// A custom JSON answer is final even when empty, so it is served
				// like the last member's rather than probed for a fallback that
				// would replay the business request.
				if !isStream {
					fwdCtx = internalproxy.WithZeroContentProbe(candidateCtx, !isLastModel && !customJSON)
				} else if !isLastModel && !customJSON {
					streamPeeker = internalproxy.NewStreamContentPeeker(candidateWriter)
					targetWriter = streamPeeker
				}
				attemptStart := time.Now()
				if modelInfo.Provider == "mimo-free" {
					comboMetrics := &streamMetrics{}
					upstreamJSON = clampMimoFreeReasoning(upstreamJSON, modelInfo.Model)
					fwdErr = h.MimoFreeChat(fwdCtx, targetWriter, upstreamJSON, isStream, comboMetrics)
				} else {
					comboParams := forwardRequestParams{
						Ctx: fwdCtx, W: targetWriter, Provider: modelInfo.Provider, Model: modelInfo.Model,
						RequestedModel: comboName, ComboName: comboName, Attempts: attemptCount(ctx),
						ConnectionID: connID, ConnName: connObjName(picked), ConnEmail: connObjEmail(picked),
						ConnData: connData, Body: upstreamJSON,
						IsStream: isStream, TranslateResponse: translateResponse,
						Endpoint: forwardEndpoint(ctx, "/v1/chat/completions"),
					}
					fwdErr = h.tryForwardWithConnection(comboParams)
					if fwdErr != nil && (timeoutGate == nil || !timeoutGate.timedOut()) {
						fwdErr = h.walkPoolOnFreeTierRefusalFor(comboParams, connID, connObjName(picked), connObjEmail(picked), fwdErr)
					}
				}
				if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
					fwdErr = context.DeadlineExceeded
				}
				if streamPeeker != nil {
					if finishErr := streamPeeker.Finish(); finishErr != nil {
						if fwdErr == nil || errors.Is(finishErr, internalproxy.ErrStreamingInbandError) {
							fwdErr = finishErr
						}
					}
				}
				RecordAttemptFromContext(ctx, connID, connObjName(picked), modelInfo.Provider, modelInfo.Model, 0, fwdErr, time.Since(attemptStart).Milliseconds())
				if fwdErr != nil && connID != "" {
					tried = append(tried, connID)
				}

				if fwdErr != nil {
					if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
						log.Warn("combo", "model response timeout, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = comboTimeoutError()
						break
					}
					if errors.Is(fwdErr, internalproxy.ErrStreamingInbandError) {
						// A provider-injected in-band error is an explicit
						// failure signal. The peeker already committed the
						// error frame to the client, so stop the chain and
						// do not fail over to the next combo member.
						log.Warn("combo", "in-band upstream error, stopping chain, no failover",
							"provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connID)
						var ue *upstreamError
						if !errors.As(fwdErr, &ue) {
							ue = &upstreamError{
								StatusCode: http.StatusBadGateway,
								Body:       upstreamErrorBody(fwdErr.Error()),
							}
						}
						h.recordModelCooldown(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						retry.note(ue)
						lastErr = ue
						inbandError = true
						break
					}
					if errors.Is(fwdErr, internalproxy.ErrStreamingZeroContent) {
						log.Warn("combo", "streaming zero content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: []byte(`{"error":{"message":"streaming zero content"}}`)}
						break
					}
					var zcErr *internalproxy.ZeroContentError
					if errors.As(fwdErr, &zcErr) {
						log.Warn("combo", "zero usable content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: zcErr.Body}
						break
					}
					if ctx.Err() != nil {
						lastErr = &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(clientClosedBody)}
						break
					}
					if isGuardrailBlock(fwdErr) {
						// A policy refusal is final: every other connection and
						// model in the combo would receive the same prompt and
						// answer with the same refused content, so continuing
						// would spread it across every provider the combo names.
						log.Warn("combo", "guardrail block, stopping turn", "provider", modelInfo.Provider, "conn", connID)
						var ue *upstreamError
						errors.As(fwdErr, &ue)
						lastErr = ue
						break
					}
					if _, refused := providerLimitRefusal(fwdErr); customJSON && !refused {
						log.Warn("combo", "custom JSON attempt failed, not replaying", "provider", modelInfo.Provider, "conn", connID)
						lastErr = customJSONTerminalError(fwdErr)
						noReplay = true
						break
					}
					if attempt, refused := providerLimitRefusal(fwdErr); refused {
						// A provider at its own capacity is neither an account
						// fault nor a bad model: cooling the connection down
						// would park a healthy account, and the next candidate
						// may be free. Keep the refusal as the terminal error for
						// when none is left.
						log.Info("combo", "provider limit, next candidate",
							"provider", modelInfo.Provider, "model", modelInfo.Model,
							"axis", attempt.Axis, "conn", connID)
						var refusedErr *upstreamError
						errors.As(fwdErr, &refusedErr)
						if refusedErr != nil {
							lastErr = refusedErr
						}
						if isKnownNoAuth {
							break
						}
						continue
					}
					var ue *upstreamError
					if errors.As(fwdErr, &ue) {
						// A 410 naming a dead model is a routing fact, not an account
						// fault: every connection to this provider answers the same
						// way. Record it, then leave this entry for the next combo
						// model (#179).
						//
						// The connection is deliberately NOT added to excludeIDs:
						// that list spans the whole pass, so excluding it here would
						// starve every later entry on a single-account provider —
						// exactly the case the failover exists to rescue. The model
						// lock recordModelDeprecation writes is per provider/model,
						// which is the scope that actually matters.
						if providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
							h.recordModelDeprecation(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						// A model-level fault that is not a permanent
						// deprecation (404, model-scoped 429, or gateway
						// fault 500-504) earns a transient cooldown badge.
						// One cooldown per business request per model —
						// deduped by the TTL cache inside
						// recordModelCooldown.
						h.recordModelCooldown(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
						// A prompt that does not fit this model fails the same
						// way on every connection to it, so advancing to the
						// next combo member is the only move that can succeed
						// (#338). Breaking here also skips
						// comboLockRetryable: the account is fine, and cooling
						// it down would take a healthy connection out of the
						// rotation.
						if providers.IsModelFitError(ue.StatusCode, ue.Body) {
							log.Info("combo", "model too small for request, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						// A 4xx outside 429 rejects the request itself, so the
						// account's other connections answer the same way:
						// advance to the next combo member instead of re-sending
						// here, and keep the first such refusal as the terminal
						// error — it is the only one that names what to change.
						if isRequestScopedComboError(ue) {
							noteRequestScoped(&firstRequestScoped, ue)
							log.Info("combo", "request-scoped rejection, next member",
								"status", ue.StatusCode, "provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
							// In combo loops, fail over immediately to the next connection/model without blocking the client turn
							log.Info("combo", "transient failover skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "conn", connID)
						}
						retry.note(ue)
						lastErr = ue
						if isKnownNoAuth {
							break
						}
						continue
					}
					lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: upstreamErrorBody(fmt.Sprintf("upstream error: %v", fwdErr))}
					if isKnownNoAuth {
						break
					}
					continue
				}

				entrySuccess = true
				break
			}
			if timeoutGate != nil && timeoutGate.finish() && ctx.Err() == nil {
				lastErr = comboTimeoutError()
				modelTimedOut = true
				entrySuccess = false
				h.benchTimedOutModel(comboName, entry, timeoutGrace)
			}

			// A failed smart-pinned model loses its session pin, so the next
			// turn re-evaluates instead of returning to a dead model.
			if !entrySuccess {
				h.clearFailedSmartSticky(ctx, entry)
			}
			if inbandError {
				break
			}
			if entrySuccess || ctx.Err() != nil {
				return
			}
			if noReplay || isGuardrailBlock(lastErr) {
				// A policy refusal ends the whole turn, not just this entry: the
				// next model would be sent the same prompt and answer with the
				// same refused content.
				break
			}
		}
		if noReplay || isGuardrailBlock(lastErr) {
			break
		}

		// All entries failed. Retry once only if a bounded wait is available;
		// otherwise fall through to the error response below.
		if lastErr == nil || ctx.Err() != nil || attempt == 1 || modelTimedOut || inbandError {
			break
		}
	}

	if cw.IsCommitted() {
		return
	}

	if lastErr != nil && isRetryableComboError(lastErr) {
		if fallbackCombos := h.comboFallbackCombos(comboName); len(fallbackCombos) > 0 {
			if visited == nil {
				visited = make(map[string]bool)
			}
			visited[comboName] = true
			for _, fbComboName := range fallbackCombos {
				if visited[fbComboName] {
					continue
				}
				if h.Repo == nil {
					continue
				}
				combo, err := h.Repo.GetComboByName(fbComboName)
				log.Info("combo", "trying fallback combo repo result", "fbComboName", fbComboName, "err", err, "comboNil", combo == nil)
				if err != nil || combo == nil || combo.Models == "" {
					continue
				}
				fbModels := combo.ModelNames()
				if len(fbModels) == 0 {
					if err := json.Unmarshal([]byte(combo.Models), &fbModels); err != nil || len(fbModels) == 0 {
						continue
					}
				}
				flattened, err := h.flattenComboModels(fbModels)
				if err != nil || len(flattened) == 0 {
					continue
				}
				strat, sticky, _ := h.resolveComboRouting(combo.Name, combo.Strategy)

				nextVisited := make(map[string]bool, len(visited)+1)
				for k, v := range visited {
					nextVisited[k] = v
				}
				nextVisited[fbComboName] = true

				if cw.IsCommitted() {
					return
				}
				clearRouteHeaders(cw)

				h.handleComboFallbackWithVisited(ctx, cw, body, flattened, strat, isStream, translateResponse, fbComboName, sticky, nextVisited, false, adapterModels...)
				if cw.IsCommitted() {
					return
				}
			}
		}
	}

	if !isRoot {
		// As a child fallback combo, do not commit terminal error to cw so the parent
		// combo can continue trying its remaining fallbackCombos.
		return
	}

	if cw.IsCommitted() {
		return
	}
	// The pass is over with nothing committed. The terminal error is the first
	// member that rejected the request itself — a 400/404/422 — and not the
	// last failure: a later member failing with a 5xx says nothing the client
	// can act on, while the 4xx names the parameter or the model to change
	// (#5057).
	terminal := firstRequestScoped
	if terminal == nil {
		terminal = lastErr
	}
	if terminal != nil {
		if cw.IsCommitted() {
			log.Error("combo", "upstream error after headers committed", "error", terminal)
			return
		}
		retry.writeError(cw, terminal)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// handleMessagesComboFallback iterates through combo models for the Claude endpoint.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
// adapterModels holds the capacity-adapter entries injected for this request;
// their history is trimmed to their context window before forwarding.
func (h *ChatHandler) handleMessagesComboFallback(ctx context.Context, w http.ResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool, comboName string, stickyLimit int, adapterModels ...string) {
	if FallbackAttemptRecorderFrom(ctx) == nil {
		ctx = WithFallbackAttemptRecorder(ctx, NewFallbackAttemptRecorder())
	}
	// Inject the combo's own custom system prompt before any model attempt, so
	// every member of the combo carries the same operator instructions. When
	// set, the global system prompt prefix/suffix is skipped for this request
	// (the combo prompt replaces it, it does not stack on top).
	// Messages-endpoint bodies may be Claude-native (top-level system field)
	// or OpenAI-format (messages array), so detect from the body shape.
	claudeNative := translatedReq["system"] != nil
	if h.Repo != nil && comboName != "" {
		if combo, err := h.Repo.GetComboByName(comboName); err == nil && combo != nil && combo.CustomPrompt != "" {
			body, err := json.Marshal(translatedReq)
			if err == nil {
				if next, did := tokensaver.InjectPrefixSystemPrompt(body, combo.CustomPrompt, claudeNative); did {
					_ = json.Unmarshal(next, &translatedReq)
					ctx = WithComboCustomPrompt(ctx)
				}
			}
		}
	}
	cw := newCommittedResponseWriter(w)
	cw.stream = isStream
	cw.responsesClient = translator.IsResponsesClient(ctx)
	h.handleMessagesComboFallbackWithVisited(ctx, cw, translatedReq, comboModels, strategy, isStream, comboName, stickyLimit, nil, true, adapterModels...)
}

func (h *ChatHandler) handleMessagesComboFallbackWithVisited(ctx context.Context, cw *committedResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool, comboName string, stickyLimit int, visited map[string]bool, isRoot bool, adapterModels ...string) {
	var lastErr *upstreamError
	// firstRequestScoped is the first member failure that names the request
	// rather than the account. When every member refuses, that refusal is the
	// one the client can act on (#5057).
	var firstRequestScoped *upstreamError
	// noReplay ends the turn after a custom JSON business attempt, whose
	// operation must never be repeated against another connection or model.
	var noReplay bool
	// inbandError records that the current candidate committed a
	// provider-injected in-band error frame. The chain must stop: no failover
	// to subsequent models, no retry pass.
	var inbandError bool
	var retry passRetry
	responseTimeout, timeoutGrace := h.comboTimeoutPolicy(comboName)
	modelTimedOut := false

	// Auto-capability-switch: convert body to JSON for detection
	bodyJSON, _ := json.Marshal(translatedReq)
	// Connections that failed with a retryable status this request; remaining
	// combo models must not re-select them (same account = same 429 quota).
	var excludeIDs []string
	var models []string
	comboModels, adapterModels = h.filterAvailableComboModels(ctx, comboModels, adapterModels)
	if len(comboModels) == 0 {
		lastErr = allComboModelsUnavailableError()
	} else {
		// smart, tokenmaxxing, and cascade compute dynamic rankings and override stored order.
		if strategy == "smart" {
			ctx, models = h.applySmartRouting(ctx, bodyJSON, comboModels, comboName)
		} else if strategy == "tokenmaxxing" {
			models = h.applyTokenmaxxingRouting(ctx, comboModels)
		} else if strategy == "cascade" {
			models = OrderModelsByCascade(comboModels)
		} else if h.comboRespectsStoredOrder(comboName) {
			models = slices.Clone(comboModels)
		} else {
			var comboOwn []string
			if len(adapterModels) > 0 && len(comboModels) >= len(adapterModels) {
				comboOwn = comboModels[len(adapterModels):]
			}
			models = h.applyComboStrategyForConversation(strategy, comboModels, comboName, stickyLimit, detectNewTurn(bodyJSON), conversationFingerprint(bodyJSON), comboOwn)
		}
		if required := DetectRequiredCapabilities(bodyJSON); len(required) > 0 && len(models) > 0 {
			reordered := ReorderByCapabilities(models, required)
			models = reordered
		}
		// A model that recently timed out sits out its grace period.
		models = h.withoutBenchedModels(comboName, models, timeoutGrace)
	}

	// If every model fails, retry the whole pass once after a bounded
	// Retry-After wait so a transient provider blip doesn't surface as a hard
	// 429 to the client.
	for attempt := 0; attempt < 2 && len(models) > 0; attempt++ {
		if attempt > 0 {
			wait := retry.wait()
			if wait == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			// Fresh pass: re-allow connections locked by the previous attempt
			// (their cooldown has elapsed) and clear the error state.
			lastErr = nil
			firstRequestScoped = nil
			retry.reset()
			excludeIDs = nil
		}

		// See the chat loop: the entry tried last is the last one this pass
		// can actually send a request for, not the last one stored.
		lastTried := newComboLastTried(h, models)

		for i, entry := range models {
			modelInfo := h.resolveModelEntry(entry)
			if modelInfo == nil {
				continue
			}
			isLastModel := i == lastTried.index(excludeIDs)

			// Skip unavailable (model-locked) providers
			if !h.Repo.IsProviderAvailable(ctx, modelInfo.Provider, modelInfo.Model) {
				log.Warn("combo", "skip unhealthy", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"all connections for this provider are rate-limited","type":"rate_limit_error","code":429}}`)}
				}
				continue
			}

			candidateCtx := ctx
			candidateWriter := http.ResponseWriter(cw)
			// See the chat loop: the candidate tried last keeps the global deadline.
			var timeoutGate *comboResponseTimeout
			if responseTimeout > 0 && !isLastModel {
				timeoutGate = newComboResponseTimeout(ctx, cw, responseTimeout)
				defer timeoutGate.finish()
				candidateCtx, candidateWriter = timeoutGate.ctx, timeoutGate
			}

			var entrySuccess bool
			// See the chat loop: never resend this entry to a connection it
			// already tried.
			var tried []string
			// Try up to 10 connections for this model entry
			for connAttempt := 0; connAttempt < 10; connAttempt++ {
				var connID string
				var picked *ProviderConnection
				var connData *ConnectionData
				isKnownNoAuth := false
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					isKnownNoAuth = true
					connData = &ConnectionData{
						APIKey:      cfg.DefaultAPIKey,
						Provider:    modelInfo.Provider,
						ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				} else {
					var cData *ConnectionData
					var err error
					picked, cData, err = h.getBestConnectionWithContext(candidateCtx, modelInfo.Provider, modelInfo.ConnectionID, entryExcludes(excludeIDs, tried), modelInfo.Model)
					if err != nil {
						if refusal, blocked := providerLimitRefusal(err); blocked {
							log.Info("combo", "provider node unusable, next candidate",
								"provider", modelInfo.Provider, "model", modelInfo.Model, "axis", refusal.Axis)
							var refusedErr *upstreamError
							errors.As(err, &refusedErr)
							if refusedErr != nil {
								lastErr = refusedErr
							}
						}
						break
					}
					connID = picked.ID
					connData = cData
				}
				// Skip a connection already locked for this model, reading the
				// blob picked already carries — same rationale as the chat loop.
				if picked != nil {
					lockKey := canonicalLockModel(modelInfo.Provider, modelInfo.Model)
					locked := db.ConnectionModelLockedData(picked.Data, lockKey)
					if !locked && lockKey != modelInfo.Model {
						locked = db.ConnectionModelLockedData(picked.Data, modelInfo.Model)
					}
					if locked {
						log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "lockKey", lockKey, "conn", picked.ID)
						excludeIDs = append(excludeIDs, picked.ID)
						continue
					}
				}

				entryBody, err := adapterTrimmedMap(translatedReq, entry, adapterModels)
				if err != nil {
					handlerutil.WriteJSONError(cw, http.StatusBadRequest, "failed to parse request body")
					return
				}
				entryBody["model"] = h.wireModelForNode(modelInfo.Provider, modelInfo.Model)
				candidateCtx = h.applyJailbreak(candidateCtx, entryBody, comboName, jailbreakLegIDs(modelInfo.Provider, modelInfo.Model)...)

				upstreamJSON, err := json.Marshal(entryBody, json.Deterministic(true))
				if err != nil {
					break
				}

				fwdCtx := candidateCtx
				targetWriter := candidateWriter
				var streamPeeker *internalproxy.StreamContentPeeker
				customJSON := isCustomJSONConn(connData)
				// The response timeout advances a combo to the next member,
				// which for a business endpoint would replay its operation.
				// Its member runs to completion instead; a gate that already
				// fired leaves the request unsent and falls back as usual.
				if customJSON && timeoutGate != nil {
					timeoutGate.disarm()
				}
				// A custom JSON answer is final even when empty, so it is served
				// like the last member's rather than probed for a fallback that
				// would replay the business request.
				if !isStream {
					fwdCtx = internalproxy.WithZeroContentProbe(candidateCtx, !isLastModel && !customJSON)
				} else if !isLastModel && !customJSON {
					streamPeeker = internalproxy.NewStreamContentPeeker(candidateWriter)
					targetWriter = streamPeeker
				}
				var providerCfg *providers.ProviderConfig
				if cfg, err := h.GetProviderConfig(modelInfo.Provider, nil); err == nil {
					providerCfg = cfg
				}
				upstreamClaude := modelInfo.Provider == "claude" || modelInfo.Provider == "anthropic" || servesClaudeMessages(modelInfo.Provider, providerCfg)
				translateResp := !upstreamClaude && !translator.IsResponsesClient(ctx)

				attemptStart := time.Now()
				msgComboParams := forwardRequestParams{
					Ctx: fwdCtx, W: targetWriter, Provider: modelInfo.Provider, Model: modelInfo.Model,
					RequestedModel: comboName, ComboName: comboName, Attempts: attemptCount(ctx),
					ConnectionID: connID, ConnName: connObjName(picked), ConnEmail: connObjEmail(picked),
					ConnData: connData, Body: upstreamJSON,
					IsStream: isStream, TranslateResponse: translateResp,
					Endpoint: forwardEndpoint(ctx, "/v1/messages"),
				}
				fwdErr := h.tryForwardWithConnection(msgComboParams)
				if fwdErr != nil && (timeoutGate == nil || !timeoutGate.timedOut()) {
					fwdErr = h.walkPoolOnFreeTierRefusalFor(msgComboParams, connID, connObjName(picked), connObjEmail(picked), fwdErr)
				}
				if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
					fwdErr = context.DeadlineExceeded
				}
				if streamPeeker != nil {
					if finishErr := streamPeeker.Finish(); finishErr != nil {
						if fwdErr == nil || errors.Is(finishErr, internalproxy.ErrStreamingInbandError) {
							fwdErr = finishErr
						}
					}
				}
				RecordAttemptFromContext(ctx, connID, connObjName(picked), modelInfo.Provider, modelInfo.Model, 0, fwdErr, time.Since(attemptStart).Milliseconds())
				if fwdErr != nil && connID != "" {
					tried = append(tried, connID)
				}

				if fwdErr != nil {
					if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
						log.Warn("combo", "model response timeout, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = comboTimeoutError()
						break
					}
					if errors.Is(fwdErr, internalproxy.ErrStreamingInbandError) {
						// A provider-injected in-band error is an explicit
						// failure signal. The peeker already committed the
						// error frame to the client, so stop the chain and
						// do not fail over to the next combo member.
						log.Warn("combo", "in-band upstream error, stopping chain, no failover",
							"provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connID)
						var ue *upstreamError
						if !errors.As(fwdErr, &ue) {
							ue = &upstreamError{
								StatusCode: http.StatusBadGateway,
								Body:       upstreamErrorBody(fwdErr.Error()),
							}
						}
						h.recordModelCooldown(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						retry.note(ue)
						lastErr = ue
						inbandError = true
						break
					}
					if errors.Is(fwdErr, internalproxy.ErrStreamingZeroContent) {
						log.Warn("combo", "streaming zero content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: []byte(`{"error":{"message":"streaming zero content"}}`)}
						break
					}
					var zcErr *internalproxy.ZeroContentError
					if errors.As(fwdErr, &zcErr) {
						log.Warn("combo", "zero usable content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: zcErr.Body}
						break
					}
					if ctx.Err() != nil {
						lastErr = &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(clientClosedBody)}
						break
					}
					if isGuardrailBlock(fwdErr) {
						// See the chat branch above: a policy refusal stops the
						// turn instead of being replayed against the next model.
						log.Warn("combo", "guardrail block, stopping turn", "provider", modelInfo.Provider, "conn", connID)
						var blocked *upstreamError
						errors.As(fwdErr, &blocked)
						lastErr = blocked
						break
					}
					if _, refused := providerLimitRefusal(fwdErr); customJSON && !refused {
						log.Warn("combo", "custom JSON attempt failed, not replaying", "provider", modelInfo.Provider, "conn", connID)
						lastErr = customJSONTerminalError(fwdErr)
						noReplay = true
						break
					}
					if attempt, refused := providerLimitRefusal(fwdErr); refused {
						// Same contract as the OpenAI loop: the endpoint is busy,
						// so the next candidate is tried without a cooldown on an
						// account that did nothing wrong.
						log.Info("combo", "provider limit, next candidate",
							"provider", modelInfo.Provider, "model", modelInfo.Model,
							"axis", attempt.Axis, "conn", connID)
						var refusedErr *upstreamError
						errors.As(fwdErr, &refusedErr)
						if refusedErr != nil {
							lastErr = refusedErr
						}
						if isKnownNoAuth {
							break
						}
						continue
					}
					var ue *upstreamError
					if errors.As(fwdErr, &ue) {
						// Same contract as the OpenAI loop: a 410 naming a dead
						// model is a provider-wide fact, so it is recorded and
						// the next combo entry is tried (#179). The connection
						// stays out of excludeIDs — see the OpenAI loop: that
						// list spans the pass, and a single-account provider
						// would have nothing left to fail over to.
						if providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
							h.recordModelDeprecation(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						// A model-level fault that is not a permanent
						// deprecation (404, model-scoped 429, or gateway
						// fault 500-504) earns a transient cooldown badge.
						// One cooldown per business request per model —
						// deduped by the TTL cache inside
						// recordModelCooldown.
						h.recordModelCooldown(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
						// A prompt that does not fit this model fails the same
						// way on every connection to it, so advancing to the
						// next combo member is the only move that can succeed
						// (#338). Breaking here also skips
						// comboLockRetryable: the account is fine, and cooling
						// it down would take a healthy connection out of the
						// rotation.
						if providers.IsModelFitError(ue.StatusCode, ue.Body) {
							log.Info("combo", "model too small for request, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						// Same contract as the chat loop: a 4xx outside 429
						// rejects the request itself, so the account's other
						// connections answer the same way. Advance to the next
						// combo member and keep the first such refusal as the
						// terminal error — it is the only one that names what
						// to change (#5057).
						if isRequestScopedComboError(ue) {
							noteRequestScoped(&firstRequestScoped, ue)
							log.Info("combo", "request-scoped rejection, next member",
								"status", ue.StatusCode, "provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
							// In combo loops, fail over immediately to the next connection/model without blocking the client turn
							log.Info("combo", "transient failover skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "conn", connID)
						}
						retry.note(ue)
						lastErr = ue
						if isKnownNoAuth {
							break
						}
						continue
					}
					lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: upstreamErrorBody(fmt.Sprintf("upstream error: %v", fwdErr))}
					if isKnownNoAuth {
						break
					}
					continue
				}

				entrySuccess = true
				break
			}
			if timeoutGate != nil && timeoutGate.finish() && ctx.Err() == nil {
				lastErr = comboTimeoutError()
				modelTimedOut = true
				entrySuccess = false
				h.benchTimedOutModel(comboName, entry, timeoutGrace)
			}

			// A failed smart-pinned model loses its session pin, so the next
			// turn re-evaluates instead of returning to a dead model.
			if !entrySuccess {
				h.clearFailedSmartSticky(ctx, entry)
			}
			if inbandError {
				break
			}
			if entrySuccess || ctx.Err() != nil {
				return
			}
			if noReplay || isGuardrailBlock(lastErr) {
				// See handleComboFallback: a policy refusal ends the turn.
				break
			}
		}
		if noReplay || isGuardrailBlock(lastErr) {
			break
		}

		// All entries failed. Retry once only if a bounded wait is available;
		// otherwise fall through to the error response below.
		if lastErr == nil || ctx.Err() != nil || attempt == 1 || modelTimedOut || inbandError {
			break
		}
	}

	if cw.IsCommitted() {
		return
	}

	if lastErr != nil && isRetryableComboError(lastErr) {
		if fallbackCombos := h.comboFallbackCombos(comboName); len(fallbackCombos) > 0 {
			if visited == nil {
				visited = make(map[string]bool)
			}
			visited[comboName] = true
			for _, fbComboName := range fallbackCombos {
				if visited[fbComboName] {
					continue
				}
				if h.Repo == nil {
					continue
				}
				combo, err := h.Repo.GetComboByName(fbComboName)
				log.Info("combo", "trying fallback combo repo result", "fbComboName", fbComboName, "err", err, "comboNil", combo == nil)
				if err != nil || combo == nil || combo.Models == "" {
					continue
				}
				fbModels := combo.ModelNames()
				if len(fbModels) == 0 {
					if err := json.Unmarshal([]byte(combo.Models), &fbModels); err != nil || len(fbModels) == 0 {
						continue
					}
				}
				flattened, err := h.flattenComboModels(fbModels)
				if err != nil || len(flattened) == 0 {
					continue
				}
				strat, sticky, _ := h.resolveComboRouting(combo.Name, combo.Strategy)

				nextVisited := make(map[string]bool, len(visited)+1)
				for k, v := range visited {
					nextVisited[k] = v
				}
				nextVisited[fbComboName] = true

				if cw.IsCommitted() {
					return
				}
				clearRouteHeaders(cw)

				h.handleMessagesComboFallbackWithVisited(ctx, cw, translatedReq, flattened, strat, isStream, fbComboName, sticky, nextVisited, false, adapterModels...)
				if cw.IsCommitted() {
					return
				}
			}
		}
	}

	if !isRoot {
		// As a child fallback combo, do not commit terminal error to cw so the parent
		// combo can continue trying its remaining fallbackCombos.
		return
	}

	if cw.IsCommitted() {
		return
	}
	// Same terminal rule as the chat loop: the first member that rejected the
	// request itself outranks a later member's 5xx, which names nothing the
	// client can fix (#5057).
	terminal := firstRequestScoped
	if terminal == nil {
		terminal = lastErr
	}
	if terminal != nil {
		if cw.IsCommitted() {
			log.Error("combo", "upstream error after headers committed", "error", terminal)
			return
		}
		retry.writeError(cw, terminal)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// entryExcludes returns the connections one combo entry must not select: the
// pass-wide exclusions plus the connections this entry already tried. The
// tried set stays per entry on purpose — a connection that rejected this
// model may serve the next one, and on a single-account provider excluding it
// for the whole pass would starve every later entry (see the 410 note in the
// chat loop). Clip forces a copy so excludeIDs is never written through.
func entryExcludes(excludeIDs, tried []string) []string {
	if len(tried) == 0 {
		return excludeIDs
	}
	return append(slices.Clip(excludeIDs), tried...)
}

// comboLockRetryable classifies a retryable upstream error in a combo loop and
// locks the failed connection+model so the exponential backoff persists across
// requests. The combo path iterates tryForwardWithConnection directly and used
// to skip this entirely — so a 429 never set a lock, every request re-tried all
// combo models on the same account, and Google rate-limited it forever. The
// connection is also appended to excludeIDs so the remaining combo models in
// this request skip it instead of re-hitting the same quota bucket.
// isLocalQueueRefusal reports a refusal our own admission control produced. It
// says nothing about the account or the model, so cooling either on it parks a
// healthy connection because our queue was busy.
//
// The verdict comes from UpstreamError.Local, which only admitAntigravity sets.
// Reading it out of the body would let an upstream that echoes a queue_full
// payload dodge its own cooldown.
func isLocalQueueRefusal(ue *upstreamError) bool {
	return ue != nil && ue.Local
}

func (h *ChatHandler) comboLockRetryable(ctx context.Context, excludeIDs *[]string, connID, provider, model string, ue *upstreamError) {
	if handlerutil.IsProbeContext(ctx) {
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	if connID == "" {
		return
	}
	// Same contract as the single-provider failover loop: an OpenCode free-tier
	// refusal is an egress verdict, not an account or model fault. Cooling
	// either would park a healthy account (and, for the 429 usage-limit shape,
	// hide it behind a quota it does not own), so the connection is only
	// excluded for the rest of this pass and the next combo entry is tried.
	if isOpencodeFreeTierRefusal(provider, ue) {
		*excludeIDs = append(*excludeIDs, connID)
		log.Warn("combo", "opencode free-tier refusal, not locking the account",
			"provider", provider, "model", model, "conn", connID, "status", ue.StatusCode)
		return
	}
	// Our own queue was full: exclude the connection for this pass and try the
	// next entry, but leave its cooldown and backoff alone.
	if isLocalQueueRefusal(ue) {
		*excludeIDs = append(*excludeIDs, connID)
		log.Warn("combo", "local queue full, not locking the account",
			"provider", provider, "model", model, "conn", connID)
		return
	}
	currentLevel := h.Repo.GetConnectionBackoffLevel(connID)
	cls := providers.ClassifyErrorForProvider(ue.StatusCode, extractErrorText(ue.Body), currentLevel, provider, ue.Body)
	// Upstream parity (checkFallbackError): request-scoped 4xx (400/404/405/
	// 409/422/...) must not lock the account — the bug is in the request,
	// not the credential. Only lock when ShouldFallback is set.
	if !cls.ShouldFallback || cls.CooldownMs <= 0 {
		return
	}
	cooldownSec := retryableCooldownSec(ue.StatusCode, time.Duration(cls.CooldownMs)*time.Millisecond, ue, provider)
	lockKey := canonicalLockModel(provider, model)
	if err := h.Repo.LockConnectionModel(connID, lockKey, cooldownSec, cls.NewBackoffLevel); err != nil {
		log.Warn("combo", "lock failed", "conn", connID, "provider", provider, "model", lockKey, "error", err)
	}
	if lockKey != model {
		_ = h.Repo.LockConnectionModel(connID, model, cooldownSec, cls.NewBackoffLevel)
	}
	// Account-scoped cooldown alongside the per-model locks, so the selector
	// can skip this account before spending a request (upstream applyErrorState).
	// Only lock if error is account-scoped, not model-scoped (e.g. 401 auth
	// issues, or a rule that declared itself model-scoped such as an NVIDIA
	// NIM degraded cluster), so unrelated models stay available.
	if cls.ModelScoped || isModelScopedError(ue.StatusCode, extractErrorText(ue.Body), model, ue.Body) {
		if recErr := h.Repo.RecordConnectionScopedError(connID, model, "chat", ue.StatusCode, extractErrorText(ue.Body), cls.NewBackoffLevel); recErr != nil {
			log.Warn("combo", "record connection error failed", "conn", connID, "error", recErr)
		}
	} else {
		until := time.Now().UTC().Add(time.Duration(cooldownSec) * time.Second)
		if err := h.Repo.LockConnectionRateLimit(connID, until, cls.NewBackoffLevel, ue.StatusCode, extractErrorText(ue.Body)); err != nil {
			log.Warn("combo", "rate limit lock failed", "conn", connID, "error", err)
		}
	}
	*excludeIDs = append(*excludeIDs, connID)
	log.Warn("combo", "locked on retryable error", "provider", provider, "model", model, "lockKey", lockKey, "conn", connID, "status", ue.StatusCode, "cooldown_s", cooldownSec)
}

// ---- Fusion (parallel fan-out + judge synthesis) ----

// fusionResult holds a single panel model's response.
