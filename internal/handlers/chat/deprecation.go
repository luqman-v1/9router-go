package chat

import (
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// deprecationRecordTTL bounds how long a deprecation is cached in memory after
// a write, so a burst of requests at one dead model does not hammer the kv
// table with identical upserts.
//
// The dashboard reads the database, never this cache, so the TTL only governs
// write volume: a value dropped early is re-derived from the next 410.
const deprecationRecordTTL = time.Minute

// recordModelDeprecation files what a 410 told us about a model, so the
// dashboard can badge it and an operator can see which combo entries point at
// a dead target.
//
// The connection is locked first: a dead model fails the same way on every
// account of that provider, so cooling the account down is what stops the next
// request from spending a real upstream call to rediscover the same 410. That
// lock is deliberately NOT the account cooldown LockConnectionRateLimit sets —
// the credential is healthy, and locking it would shrink the pool for requests
// to models that still work.
func (h *ChatHandler) recordModelDeprecation(provider, model, connID string, ue *upstreamError) {
	if h.Repo == nil || provider == "" || model == "" {
		return
	}
	if !providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
		return
	}
	detail := providers.ParseModelDeprecation(ue.Body)
	dep := providers.ModelDeprecation{
		Provider:   provider,
		Model:      model,
		Status:     providers.DeprecationGone,
		Message:    detail.Message,
		Successor:  detail.Successor,
		DetectedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if connID != "" {
		lockKey := canonicalLockModel(provider, model)
		if err := h.Repo.LockConnectionModel(connID, lockKey, modelDeprecationCooldownSec, 0); err != nil {
			log.Warn("deprecation", "model lock failed", "conn", connID, "provider", provider, "model", model, "error", err)
		}
		if lockKey != model {
			_ = h.Repo.LockConnectionModel(connID, model, modelDeprecationCooldownSec, 0)
		}
	}

	if !h.deprecationDue(provider, model) {
		return
	}
	if err := h.Repo.RecordModelDeprecation(dep); err != nil {
		log.Warn("deprecation", "record failed", "provider", provider, "model", model, "error", err)
		return
	}
	h.markDeprecationWritten(provider, model)
	log.Info("deprecation", "model retired upstream", "provider", provider, "model", model, "successor", dep.Successor)
}

// modelDeprecationCooldownSec is how long a connection stays out of rotation
// for one provider/model pair after upstream declared the model gone. It is
// long enough that a client retrying the same dead model does not keep paying
// for the upstream call, and short enough that the pair is re-probed daily in
// case the provider restores it.
const modelDeprecationCooldownSec = 24 * 60 * 60

// deprecationDue reports whether enough time has passed since this
// provider/model pair was last written.
func (h *ChatHandler) deprecationDue(provider, model string) bool {
	h.deprecationMu.Lock()
	defer h.deprecationMu.Unlock()
	key := providers.DeprecationKey(provider, model)
	writtenAt, seen := h.deprecationCache[key]
	return !seen || time.Since(writtenAt) >= deprecationRecordTTL
}

// markDeprecationWritten stamps the write so the next 410 inside the TTL skips
// the database.
func (h *ChatHandler) markDeprecationWritten(provider, model string) {
	h.deprecationMu.Lock()
	defer h.deprecationMu.Unlock()
	if h.deprecationCache == nil {
		h.deprecationCache = make(map[string]time.Time)
	}
	h.deprecationCache[providers.DeprecationKey(provider, model)] = time.Now()
}

// clearModelDeprecation forgets a recorded deprecation after the model served a
// request: a provider that brings a model back must stop showing a stale
// badge on the next dashboard load.
func (h *ChatHandler) clearModelDeprecation(provider, model string) {
	if h.Repo == nil || provider == "" || model == "" {
		return
	}
	dep, err := h.Repo.GetModelDeprecation(provider, model)
	if err != nil {
		// Nothing recorded (or unreadable): there is no badge to retract.
		return
	}
	if err := h.Repo.ClearModelDeprecation(provider, model); err != nil {
		log.Warn("deprecation", "clear failed", "provider", provider, "model", model, "error", err)
		return
	}
	log.Info("deprecation", "model serving again, badge cleared", "provider", provider, "model", model, "was", string(dep.Status))
}