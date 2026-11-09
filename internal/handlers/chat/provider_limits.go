package chat

import (
	"context"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/observ"
)

// Per-custom-provider capacity limits, request side — docs/provider-limits-plan.md.
//
// A custom provider is one providerNodes row: an upstream endpoint every
// connection under it shares. Its RPM, TPM, concurrency and queue timeout bound
// what that endpoint tolerates, which is a different axis from the per-key limits
// the middleware already enforces (a caller's own allowance). The key is checked
// first, so a caller that has spent its budget never consumes provider capacity
// on the way to being refused.
//
// Enforcement lives here rather than in a middleware because the provider is only
// known after model resolution (alias, node prefix, combo expansion), and because
// a refusal must fail over to the next candidate instead of ending the request:
// this runs once per upstream attempt, and the caller treats a refusal exactly
// like a failed attempt — try the next one, and only answer 429 when none is left.
//
// Contract expiry and the lifetime token quota are NOT here: both are node state
// rather than endpoint congestion, so they exclude the node before a candidate is
// chosen (PR 3).

// providerLimitScope labels the refusals these limits produce, in the limiter's
// own metrics namespace.
const providerLimitScope = "provider"

// providerLimitAttempt is one candidate the provider limits refused. It is both
// the log line's payload and the body field a client can read to see which
// providers were tried and why each was skipped.
type providerLimitAttempt struct {
	Provider string `json:"provider"`
	// Axis is the limit that refused: "rpm", "tpm" or "concurrency".
	Axis string `json:"reason"`
	// Limit is the configured value that refused.
	Limit int `json:"limit"`
	// RetryAfter is how many seconds to wait before retrying that provider.
	RetryAfter int `json:"retryAfter"`
}

// providerLimitState is what one request learns about provider limits.
//
// It is per request, not per process, because a refusal is a fact about this
// moment: the next request must be free to try the same provider rather than
// inherit a block. Two things live here:
//
//   - limits, memoised per provider, so a combo that tries three models on one
//     node reads the policy once instead of once per attempt;
//   - refused, the providers that already turned this request away. Without it a
//     request with several accounts under the same saturated node would wait out
//     the full queue timeout once per account to learn the same thing.
type providerLimitState struct {
	mu       sync.Mutex
	limits   map[string]db.ProviderLimits
	refused  map[string]providerLimitAttempt
	attempts []providerLimitAttempt
	// verdicts caches each node's state (contract still valid, lifetime quota
	// unspent) for the life of the request. A nil entry means usable; the map is
	// separate from refused because a verdict is a fact about the node, not about
	// a refusal this request has already suffered.
	verdicts map[string]*providerLimitAttempt
}

type providerLimitStateKey struct{}

// withProviderLimitState installs the per-request state if none is present.
//
// It is idempotent so a nested entry point (a combo member, a retry) inherits the
// state its request already carries instead of starting a fresh one — which is
// what lets one request remember that provider A was full when it moves on to
// provider B, and back.
func withProviderLimitState(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	if _, ok := ctx.Value(providerLimitStateKey{}).(*providerLimitState); ok {
		return ctx
	}
	return context.WithValue(ctx, providerLimitStateKey{}, &providerLimitState{
		limits:   map[string]db.ProviderLimits{},
		refused:  map[string]providerLimitAttempt{},
		verdicts: map[string]*providerLimitAttempt{},
	})
}

// providerLimitStateFrom returns the request's state, or nil when the request
// entered through a path that did not install one.
func providerLimitStateFrom(ctx context.Context) *providerLimitState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(providerLimitStateKey{}).(*providerLimitState)
	return state
}

func (s *providerLimitState) cachedLimits(provider string) (db.ProviderLimits, bool) {
	if s == nil {
		return db.ProviderLimits{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limits, ok := s.limits[provider]
	return limits, ok
}

func (s *providerLimitState) cacheLimits(provider string, limits db.ProviderLimits) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits[provider] = limits
}

func (s *providerLimitState) refusedAttempt(provider string) (providerLimitAttempt, bool) {
	if s == nil {
		return providerLimitAttempt{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.refused[provider]
	return attempt, ok
}

// recordRefusal remembers a refusal and returns every attempt this request has
// seen, so the response body can name all of them.
func (s *providerLimitState) recordRefusal(attempt providerLimitAttempt) []providerLimitAttempt {
	if s == nil {
		return []providerLimitAttempt{attempt}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.refused[attempt.Provider]; !seen {
		s.attempts = append(s.attempts, attempt)
	}
	s.refused[attempt.Provider] = attempt
	out := make([]providerLimitAttempt, len(s.attempts))
	copy(out, s.attempts)
	return out
}

func (s *providerLimitState) cachedVerdict(provider string) (*providerLimitAttempt, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	verdict, ok := s.verdicts[provider]
	return verdict, ok
}

func (s *providerLimitState) cacheVerdict(provider string, verdict *providerLimitAttempt) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdicts[provider] = verdict
}

// providerLimitsFor reads a provider's policy, memoised for the request.
//
// A failed read answers "unlimited" rather than refusing: a storage blip must not
// take an endpoint offline, which is the same fail-open rule the key-side token
// quota reader follows. The alternative — treating an unreadable policy as
// exhausted — turns a transient database error into a gateway-wide outage for
// every custom provider at once.
func (h *ChatHandler) providerLimitsFor(ctx context.Context, state *providerLimitState, provider string) db.ProviderLimits {
	if limits, ok := state.cachedLimits(provider); ok {
		return limits
	}
	if h.Repo == nil {
		return db.ProviderLimits{}
	}
	limits, err := h.Repo.GetProviderLimits(ctx, provider)
	if err != nil {
		log.Warn("providerlimit", "provider limits read failed, treating as unlimited",
			"provider", provider, "error", err)
		return db.ProviderLimits{}
	}
	state.cacheLimits(provider, limits)
	return limits
}

// providerNodeBlocked decides whether a node is unusable for this whole request
// from its own state rather than from its congestion: an expired contract, or a
// lifetime token quota that is spent.
//
// It is the ONE place that decides this, and both the candidate selection
// (connections.go) and the dispatch path (acquireProviderAttempt) ask it, so a node
// skipped while choosing a candidate cannot be admitted at dispatch, or the other
// way round.
//
// A failed usage read answers "usable", the same fail-open rule the policy read
// follows: a storage blip must not take a node out of rotation.
func (h *ChatHandler) providerNodeBlocked(ctx context.Context, state *providerLimitState, provider string, limits db.ProviderLimits) (providerLimitAttempt, bool) {
	if cached, ok := state.cachedVerdict(provider); ok {
		if cached == nil {
			return providerLimitAttempt{}, false
		}
		return *cached, true
	}
	if verdict := h.providerNodeVerdict(ctx, provider, limits); verdict != nil {
		state.cacheVerdict(provider, verdict)
		return *verdict, true
	}
	state.cacheVerdict(provider, nil)
	return providerLimitAttempt{}, false
}

// providerNodeVerdict reads the two node-state facts, or nil when the node is
// usable.
func (h *ChatHandler) providerNodeVerdict(ctx context.Context, provider string, limits db.ProviderLimits) *providerLimitAttempt {
	if limits.Expired(time.Now()) {
		return &providerLimitAttempt{Provider: provider, Axis: axisContract}
	}
	if limits.TokenQuota <= 0 || h.Repo == nil {
		return nil
	}
	used, err := h.Repo.ProviderTokenUsage(ctx, provider)
	if err != nil {
		log.Warn("providerlimit", "provider usage read failed, treating the quota as unspent",
			"provider", provider, "error", err)
		return nil
	}
	if used < limits.TokenQuota {
		return nil
	}
	return &providerLimitAttempt{Provider: provider, Axis: axisTokenQuota, Limit: limits.TokenQuota}
}

// middlewareLimits maps the stored policy onto the limiter's own type.
func middlewareLimits(limits db.ProviderLimits) middleware.Limits {
	return middleware.Limits{
		RPM:          limits.RPM,
		TPM:          limits.TPM,
		Concurrency:  limits.Concurrency,
		QueueTimeout: time.Duration(limits.QueueTimeoutMs) * time.Millisecond,
	}
}

// acquireProviderAttempt enforces one provider's limits for one upstream attempt.
//
// It returns a slot to release when the attempt ends, or an error the caller
// treats as a failed attempt. A nil slot with a nil error means the provider is
// usable and declares no congestion limits — every registry provider, and any
// custom node an operator has not configured.
func (h *ChatHandler) acquireProviderAttempt(ctx context.Context, provider, model, connectionID string, body []byte) (*middleware.ProviderSlot, error) {
	if h == nil || provider == "" {
		return nil, nil
	}
	state := providerLimitStateFrom(ctx)

	// Already refused once in this request: waiting again on the same provider
	// would spend the whole queue timeout to be told the same thing, and the pass
	// still has other candidates.
	if attempt, refused := state.refusedAttempt(provider); refused {
		log.Debug("providerlimit", "provider already refused this request, skipping",
			"provider", provider, "axis", attempt.Axis)
		return nil, h.providerLimitError(ctx, provider, attempt)
	}

	limits := h.providerLimitsFor(ctx, state, provider)

	// Node state before congestion, and before the limiter is consulted at all: an
	// expired contract or a spent lifetime quota excludes the node whatever its RPM
	// says, including when it declares no congestion limits.
	if attempt, blocked := h.providerNodeBlocked(ctx, state, provider, limits); blocked {
		h.recordProviderLimit(ctx, state, provider, model, connectionID, attempt)
		return nil, h.providerLimitError(ctx, provider, attempt)
	}

	if h.RateLimiter == nil {
		return nil, nil
	}
	effective := middlewareLimits(limits)
	if effective.RPM <= 0 && effective.TPM <= 0 && effective.Concurrency <= 0 {
		return nil, nil
	}

	// TPM is charged from the payload about to be sent, which is why this runs
	// after the body has been translated and trimmed: the key-side limiter
	// estimates from the client's body because that is all it has, and the two
	// numbers are allowed to differ.
	promptTokens := 0
	if effective.TPM > 0 {
		promptTokens = middleware.EstimatePromptTokens(body)
	}

	slot, refusal := h.RateLimiter.AcquireProvider(ctx, provider, effective, promptTokens)
	if refusal == nil {
		return slot, nil
	}

	attempt := providerLimitAttempt{
		Provider:   provider,
		Axis:       refusal.Axis,
		Limit:      refusal.Limit,
		RetryAfter: int(refusal.RetryAfter.Seconds()),
	}
	h.recordProviderLimit(ctx, state, provider, model, connectionID, attempt)
	return nil, h.providerLimitError(ctx, provider, attempt)
}

// recordProviderLimit writes the four layers of debug trail a refusal owes an
// operator (docs/provider-limits-plan.md D7.1): a structured log line that names
// the provider, the axis and the numbers; the existing rejection counter under
// the provider scope; a lastError row the dashboard's node-health table shows;
// and, through the error body, the same facts for a client that cannot read the
// server's logs.
//
// Deliberately NOT written: a cooldown. LockConnectionRateLimit would park a
// healthy account because the endpoint behind it was busy for one second, which
// is the opposite of what a capacity limit is for.
func (h *ChatHandler) recordProviderLimit(ctx context.Context, state *providerLimitState, provider, model, connectionID string, attempt providerLimitAttempt) {
	windowCount, active := h.RateLimiter.Snapshot(middleware.ProviderScopeKey(provider))
	keyID := ""
	if key := requestKeyFromContext(ctx); key != nil {
		keyID = key.ID
	}
	log.Warn("providerlimit", "provider limit breached",
		"provider", provider, "model", model, "axis", attempt.Axis,
		"limit", attempt.Limit, "windowCount", windowCount, "active", active,
		"retryAfter_s", attempt.RetryAfter, "conn", shortConnID(connectionID), "apiKeyId", keyID)
	observ.IncRateLimitReject(providerLimitScope, provider)
	state.recordRefusal(attempt)
	if connectionID == "" || h.Repo == nil {
		return
	}
	// The row must carry the status the client received, not a fixed one: an
	// expired contract reaches this function with axis "contract", and writing
	// 429 there puts "too many requests" on a node-health row whose real
	// condition is "the operator let the contract lapse".
	status := providerRefusalStatus(attempt.Axis)
	message := providerRefusalMessage(provider, attempt)
	if err := h.Repo.RecordConnectionScopedError(
		connectionID, model, providerLimitScope, status, message, 0,
	); err != nil {
		log.Debug("providerlimit", "record refusal on connection failed", "conn", connectionID, "error", err)
	}
}

// snapshotAttempts returns every refusal this request has recorded, in the order
// they happened. It exists for the debug assertion: a test can prove the trail
// accumulated rather than the response body being the only witness.
func (s *providerLimitState) snapshotAttempts() []providerLimitAttempt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]providerLimitAttempt, len(s.attempts))
	copy(out, s.attempts)
	return out
}
