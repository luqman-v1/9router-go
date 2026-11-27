package chat

import (
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/observ"
	"9router/proxy/internal/providers"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	internalproxy "9router/proxy/internal/proxy"
)

// StatusClientClosedRequest is the canonical HTTP status for client connection aborts (nginx 499).
const StatusClientClosedRequest = 499

// clientClosedBody is the JSON body that travels with StatusClientClosedRequest.
// Single-sourced so the combo and account-fallback paths cannot drift apart.
const clientClosedBody = `{"error":{"message":"client closed request","type":"client_closed_request","code":499}}`

// borrowZenKeyForGo returns the identity and key of an active opencode-zen
// connection for the paid go lane, which shares the same backend account.
// Only used when no go connection exists at all; resolution goes through the
// standard connection path so health, stickiness and token refresh apply.
func (h *ChatHandler) borrowZenKeyForGo(ctx context.Context, model string) (connID, connName, connEmail, apiKey string, err error) {
	zenConns, err := h.Repo.GetProviderConnections("opencode-zen", true)
	if err != nil {
		return "", "", "", "", err
	}
	for _, c := range zenConns {
		connObj, connData, err := h.getBestConnectionWithContext(ctx, "opencode-zen", c.ID, nil, model)
		if err != nil || connObj == nil || connData == nil {
			continue
		}
		if apiKey := extractAPIKey(connData); apiKey != "" {
			return connObj.ID, connObjName(connObj), connObjEmail(connObj), apiKey, nil
		}
	}
	return "", "", "", "", fmt.Errorf("no active opencode-zen connection with an API key")
}

// handleAccountFallback attempts to forward a request with automatic account fallback.
func (h *ChatHandler) handleAccountFallback(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	model string,
	pinnedConnectionID string,
	body []byte,
	isStream bool,
	translateResponse bool,
	endpoint string,
) error {
	if FallbackAttemptRecorderFrom(ctx) == nil {
		ctx = WithFallbackAttemptRecorder(ctx, NewFallbackAttemptRecorder())
	}
	// Per-model admission moved into tryForwardWithConnection, which every
	// dispatch path (this one, the combo loops, fusion) calls: one choke
	// point instead of one per path.
	body = repairToolCallIDsInJSON(body)
	if pinnedConnectionID != "" {
		connObj, connData, err := h.getBestConnectionWithContext(ctx, provider, pinnedConnectionID, nil, model)
		if err != nil {
			return fmt.Errorf("pinned connection %s: %w", pinnedConnectionID, err)
		}
		log.Debug("fallback", "pinned", "pinnedConn", pinnedConnectionID, "connObj", connObj.ID)
		pinnedParams := forwardRequestParams{
			Ctx: ctx, W: w, Provider: provider, Model: model,
			ConnectionID: connObj.ID, ConnName: connObjName(connObj), ConnEmail: connObjEmail(connObj),
			ConnData: connData, Body: body, Attempts: 1,
			IsStream: isStream, TranslateResponse: translateResponse, Endpoint: endpoint,
		}
		err = h.tryForwardWithConnection(pinnedParams)
		return opencodeClosureError(provider, model, h.walkPoolOnFreeTierRefusal(pinnedParams, connObj, err))
	}

	if !handlerutil.IsProbeContext(ctx) && !h.Repo.IsProviderAvailable(ctx, provider, model) {
		log.Warn("fallback", "skip unhealthy", "provider", provider, "model", model)
		return fmt.Errorf("provider %s/%s is unhealthy", provider, model)
	}

	observ.IncFallback(provider, model, observ.FallbackReasonUnhealthy)

	allConns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(allConns) == 0 {
		// opencode-go shares its backend account with opencode-zen: with no
		// go connection configured, borrow the key from an active zen
		// connection (key only — provider, BaseURL and lanes stay go).
		// Operator-requested; the borrow is logged so it stays visible.
		if provider == "opencode-go" {
			if connID, connName, connEmail, apiKey, bErr := h.borrowZenKeyForGo(ctx, model); bErr == nil {
				log.Warn("fallback", "opencode-go has no connection, borrowing opencode-zen key", "conn", connID)
				borrowParams := forwardRequestParams{
					Ctx: ctx, W: w, Provider: provider, Model: model,
					ConnectionID: connID, ConnName: connName, ConnEmail: connEmail,
					ConnData: &ConnectionData{APIKey: apiKey, ProxyPoolID: h.ResolveProviderProxyPoolID(provider)}, Body: body, Attempts: 1,
					IsStream: isStream, TranslateResponse: translateResponse, Endpoint: endpoint,
				}
				bFwdErr := h.tryForwardWithConnection(borrowParams)
				return opencodeClosureError(provider, model, h.walkPoolOnFreeTierRefusalFor(borrowParams, connID, connName, connEmail, bFwdErr))
			} else {
				log.Debug("fallback", "no zen key to borrow for opencode-go", "error", bErr)
			}
		}
		if cfg, ok := providers.KnownProviders[provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			apiKey := cfg.DefaultAPIKey
			if apiKey == "" {
				apiKey = "public"
			}
			proxyPoolID := h.ResolveProviderProxyPoolID(provider)
			noAuthParams := forwardRequestParams{
				Ctx: ctx, W: w, Provider: provider, Model: model,
				ConnectionID: "default", ConnData: &ConnectionData{APIKey: apiKey, ProxyPoolID: proxyPoolID}, Body: body, Attempts: 1,
				IsStream: isStream, TranslateResponse: translateResponse, Endpoint: endpoint,
			}
			err := h.tryForwardWithConnection(noAuthParams)
			// The keyless free lane takes this branch, so the pool walk has to
			// run here too. Without it the operator's pool was walked on the
			// stored-connection path and silently abandoned on the one the
			// OpenCode free tier actually uses: the log showed conn=default, a
			// handful of proxies, then "free tier refused after retries".
			err = h.walkPoolOnFreeTierRefusalFor(noAuthParams, "default", "", "", err)
			if err != nil {
				// Same contract as the multi-connection loop's return: a
				// free-tier refusal leaves this path as the classified
				// answer, not the upstream body verbatim.
				return opencodeClosureError(provider, model, err)
			}
			return nil
		}
		observ.IncFallback(provider, model, observ.FallbackReasonNoConnection)
		return fmt.Errorf("no active connections for provider: %s", provider)
	}

	var strat db.ProviderStrategy
	affinityEligible := false
	if h.Repo != nil {
		if settings, sErr := h.Repo.GetSettings(); sErr == nil && settings != nil {
			strat = connRotationStrategy(settings, provider)
			affinityEligible = isAffinityEligibleStrategy(strat.RotateStrategy)
		}
	}
	// The rotation below stamps whichever row it reaches (selectByRecency) and
	// persists that stamp, so an account parked until its Codex quota resets
	// took the turn's rotation slot for a request it can never serve — and the
	// resolver then burned a second pass falling through its pin. Filter the
	// live set first; an all-parked list is kept as-is so the existing
	// "all excluded / all in cooldown" answer still applies.
	allConns = filterCodexQuotaExhausted(provider, allConns)

	// Apply provider connection routing strategy (round-robin, sticky, random) or session affinity
	if len(allConns) > 1 && strat.RotateStrategy != "" && strat.RotateStrategy != "none" {
		sessionID := handlerutil.GetSessionID(ctx)
		hasAffinity := false
		if sessionID != "" && affinityEligible {
			if boundID, ok := h.getSessionAffinity().Get(provider, model, sessionID); ok && boundID != "" {
				for idx, c := range allConns {
					if c != nil && c.ID == boundID {
						if idx > 0 {
							bound := allConns[idx]
							copy(allConns[1:idx+1], allConns[0:idx])
							allConns[0] = bound
						}
						hasAffinity = true
						break
					}
				}
				if !hasAffinity {
					h.getSessionAffinity().Evict(provider, model, sessionID)
				}
			}
		}
		if !hasAffinity {
			effectiveStrat := strat
			if sessionID != "" && affinityEligible {
				effectiveStrat.StickyLimit = 1
			}
			allConns = h.applyConnectionStrategy(allConns, effectiveStrat)
		}
	}

	var seatBudgetMs int
	if h.Repo != nil {
		if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
			seatBudgetMs = settings.SeatAttemptBudgetMs
			if settings.ProviderOverrides != nil {
				if ov, ok := settings.ProviderOverrides[provider]; ok && ov.SeatBudgetMs > 0 {
					seatBudgetMs = ov.SeatBudgetMs
				}
			}
		}
	}
	if seatBudgetMs > 0 {
		ctx = internalproxy.WithSeatBudget(ctx, seatBudgetMs)
	}

	var excludeIDs []string
	// Pools already refused by an OpenCode free-tier gate in this turn. The
	// resolver filters them out, so the retry walks to the next egress instead
	// of burning a healthy account.
	excludedPools := map[string]bool{}
	var lastErr error
	for _, c := range allConns {
		if slices.Contains(excludeIDs, c.ID) {
			continue
		}
		connObj, connData, err := h.getBestConnectionWithContext(ctx, provider, c.ID, nil, model, excludedPools)
		if err != nil || connObj == nil {
			if lastErr == nil && err != nil {
				lastErr = err
			}
			continue
		}
		apiKey := extractAPIKey(connData)
		// agy-acp is credential-resolved by the executor from either a
		// referenced antigravity connection or copied tokens, so a blank
		// apiKey on the connection is valid.
		acpOptional := provider == "agy-acp" && connData != nil
		if apiKey == "" && !customJSONKeyOptional(connData) && !acpOptional {
			providerCfg, pErr := h.getProviderConfig(provider, connData)
			if pErr == nil && providerCfg.DefaultAPIKey != "" {
				apiKey = providerCfg.DefaultAPIKey
			} else {
				if lastErr == nil {
					lastErr = fmt.Errorf("connection %s has no API key", connObj.ID)
				}
				continue
			}
		}
		// connObj, not the loop candidate: a candidate in cooldown can resolve
		// to a different account (force fallback), and the usage row, the logs
		// and the exclusion below must all name the account that was dialled.
		log.Debug("fallback", "connection", "conn", connObj.ID, "candidate", c.ID)
		attemptStart := time.Now()
		params := forwardRequestParams{
			Ctx: ctx, W: w, Provider: provider, Model: model,
			ConnectionID: connObj.ID, ConnName: connObjName(connObj), ConnEmail: connObjEmail(connObj),
			ConnData: connData, Body: body, Attempts: attemptCount(ctx),
			IsStream: isStream, TranslateResponse: translateResponse, Endpoint: endpoint,
		}
		// Count the attempt as load while it runs, including a long streaming
		// reply: least-busy reads these counts. The release below reports the
		// outcome, so a key that refused is deprioritized for a while.
		leastBusy.notePick(connObj.ID)
		err = h.tryForwardWithConnection(params)
		leastBusy.noteRelease(connObj.ID, err != nil)
		// A pool is many egresses behind ONE connection, but this loop advances
		// over connections. Without this walk a provider with one connection and
		// a hundred proxies got exactly one attempt per request, and the turn
		// failed while ninety-six egresses had never been tried.
		//
		// Each pass re-enters the forward, so the resolver's rotation hands back
		// a different proxy and the refused one is skipped. The bound is the pool
		// size: a hundred proxies is a hundred attempts, which is what the
		// operator asked for by assigning a hundred.
		err = h.walkPoolOnFreeTierRefusalWithExcluded(params, connObj.ID, connObjName(connObj), connObjEmail(connObj), excludedPools, err)
		if err == nil {
			RecordAttemptFromContext(ctx, connObj.ID, connObjName(connObj), provider, model, 200, nil, time.Since(attemptStart).Milliseconds())
			if sessionID := handlerutil.GetSessionID(ctx); sessionID != "" && affinityEligible {
				h.getSessionAffinity().Bind(provider, model, sessionID, connObj.ID)
			}
			return nil
		} else {
			lastErr = err
			observ.IncFallback(provider, model, observ.FallbackReasonUpstreamError)
			RecordAttemptFromContext(ctx, connObj.ID, connObjName(connObj), provider, model, 0, err, time.Since(attemptStart).Milliseconds())
			var ue *upstreamError
			if errors.As(lastErr, &ue) {
				if ue.StatusCode == http.StatusTooManyRequests || ue.StatusCode == http.StatusUnauthorized || ue.StatusCode == http.StatusForbidden {
					if sessionID := handlerutil.GetSessionID(ctx); sessionID != "" {
						h.getSessionAffinity().Evict(provider, model, sessionID)
					}
				}
			}
		}
		// A node-state refusal (contract ended, lifetime quota spent) answers 403, which
		// is a retryable status, so without this it would fall into the generic branch
		// below and park the connection for two minutes on the model *and* on the
		// account — for a fact about the node, not about the credential. The combo loops
		// already intercept it before their writers; the single-provider path arrives
		// here, through a pinned connection that skipped the selection-time check.
		if _, refused := providerLimitRefusal(lastErr); refused {
			log.Info("fallback", "provider node state, not locking the account",
				"provider", provider, "model", model, "conn", connObj.ID)
			excludeIDs = append(excludeIDs, connObj.ID)
			continue
		}
		// A custom JSON business endpoint may already have performed its
		// operation when it failed, so the answer is final: no other account
		// is tried and this one is not cooled down over a business error.
		if isCustomJSONConn(connData) {
			return lastErr
		}

		var ue *upstreamError
		if errors.As(lastErr, &ue) && providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
			// A retired model fails on every account of this provider, so the
			// remaining connections are skipped and the badge the operator
			// needs is recorded before the client sees the 410 (#179). The
			// lastErr still returns when every account is out, so a direct
			// request keeps reporting the upstream's own body.
			h.recordModelDeprecation(ctx, provider, model, connObj.ID, ue)
			excludeIDs = append(excludeIDs, connObj.ID)
			lastErr = ue
			continue
		}
		// A model-level fault that is not a permanent deprecation (404,
		// model-scoped 429, or gateway fault 500-504) earns a transient
		// cooldown badge: the model is temporarily unavailable, not gone.
		// One cooldown per business request per model — deduped by the TTL
		// cache inside recordModelCooldown.
		if errors.As(lastErr, &ue) {
			h.recordModelCooldown(ctx, provider, model, connObj.ID, ue)
		}
		modelScoped := errors.As(lastErr, &ue) && providers.RetryModelScopedError(ue.StatusCode, extractErrorText(ue.Body), provider)
		if modelScoped || (errors.As(lastErr, &ue) && providers.RetryableStatusCodes[ue.StatusCode]) {
			if handlerutil.IsProbeContext(ctx) {
				excludeIDs = append(excludeIDs, connObj.ID)
				continue
			}
			// A free-tier refusal says nothing about the account or the model:
			// the gate is not serving this EGRESS at the moment. Exclude the
			// pool that served the attempt and retry the same account through
			// the next one; with every pool excluded the resolver stops
			// binding a pool (errNoUsablePool) and the retry goes direct.
			// Cooling the account down would mark it unhealthy over an egress
			// verdict, so the cooldown stays untouched.
			if isOpencodeFreeTierRefusal(provider, ue) {
				if poolID := connData.ProxyPoolID; poolID != "" {
					log.Warn("fallback", "opencode free-tier refusal, rotating proxy pool",
						"conn", connObj.ID, "provider", provider, "model", model, "pool", poolID)
					excludedPools[poolID] = true
					continue
				}
				log.Warn("fallback", "opencode free-tier refusal, not locking the account",
					"conn", connObj.ID, "provider", provider, "model", model)
				excludeIDs = append(excludeIDs, connObj.ID)
				continue
			}
			// A local queue refusal is not an upstream verdict: the account never
			// saw the request, so cooling it would park a healthy connection
			// because our own admission queue was busy.
			if isLocalQueueRefusal(ue) {
				log.Warn("fallback", "local queue full, not locking the account",
					"conn", connObj.ID, "provider", provider, "model", model)
				excludeIDs = append(excludeIDs, connObj.ID)
				continue
			}
			// Extract error text from upstream body for classification
			errorText := extractErrorText(ue.Body)
			// Get current backoff level from this connection
			currentBackoffLevel := h.Repo.GetConnectionBackoffLevel(ctx, connObj.ID)
			// Classify error to get dynamic cooldown; provider-scoped rules
			// key off the canonical provider the request routed to.
			classification := providers.ClassifyErrorForProvider(ue.StatusCode, errorText, currentBackoffLevel, provider, ue.Body)
			cooldownSec := retryableCooldownSec(ue.StatusCode, time.Duration(classification.CooldownMs)*time.Millisecond, ue, provider)
			errMsg := errorText
			if errMsg == "" {
				errMsg = fmt.Sprintf("%d upstream error", ue.StatusCode)
			}
			lockKey := canonicalLockModel(provider, model)
			_ = h.Repo.LockConnectionModel(ctx, connObj.ID, lockKey, cooldownSec, classification.NewBackoffLevel)
			if lockKey != model {
				_ = h.Repo.LockConnectionModel(ctx, connObj.ID, model, cooldownSec, classification.NewBackoffLevel)
			}
			// Account-scoped cooldown alongside the per-model locks, so the
			// selector can skip this account before spending a request
			// (upstream applyErrorState). Only lock if error is account-scoped,
			// not model-scoped (e.g. 401 auth issues, or a rule that said so
			// itself such as an NVIDIA NIM degraded cluster), so unrelated
			// models stay available.
			if classification.ModelScoped || isModelScopedError(ue.StatusCode, errorText, model, ue.Body) {
				if recErr := h.Repo.RecordConnectionScopedError(ctx, connObj.ID, model, "chat", ue.StatusCode, errorText, classification.NewBackoffLevel); recErr != nil {
					log.Warn("fallback", "record connection error failed", "conn", connObj.ID, "error", recErr)
				}
			} else {
				// Account-level bans (permanent/severe account failures) use the
				// escalating lockout ladder keyed on the connection's consecutive
				// failure streak instead of a flat 24h cooldown.
				if providers.LooksLikeAccountDeactivated(errorText) {
					if until, _, lockErr := h.Repo.RecordConnectionAccountBanFailure(connObj.ID); lockErr != nil {
						log.Warn("fallback", "account ban lock failed", "conn", connObj.ID, "error", lockErr)
					} else {
						cooldownSec = int(until.Sub(time.Now().UTC()).Seconds())
					}
				}
				until := time.Now().UTC().Add(time.Duration(cooldownSec) * time.Second)
				if lockErr := h.Repo.LockConnectionRateLimit(ctx, connObj.ID, until, classification.NewBackoffLevel, ue.StatusCode, errorText); lockErr != nil {
					log.Warn("fallback", "rate limit lock failed", "conn", connObj.ID, "error", lockErr)
				}
				h.getSessionAffinity().EvictConnection(connObj.ID)
			}
			log.Warn("fallback", "connection locked", append([]any{
				"conn", connObj.ID, "provider", provider, "model", model,
				"lockKey", lockKey, "status", ue.StatusCode, "cooldown_s", cooldownSec,
			}, connIdentityKV(connObj)...)...)
			excludeIDs = append(excludeIDs, connObj.ID)
			continue
		}
		return lastErr
	}
	if lastErr != nil {
		return opencodeClosureError(provider, model, lastErr)
	}
	return fmt.Errorf("no available connections for provider: %s", provider)
}
