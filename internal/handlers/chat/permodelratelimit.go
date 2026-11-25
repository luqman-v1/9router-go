package chat

import (
	"fmt"
	"sync"
	"time"

	"9router/proxy/internal/db"
)

// Per-model rate limiting for CUSTOM providers.
//
// A runaway agent loop, or one heavy API customer, hammering a single model on
// a custom endpoint burns that endpoint's quota for everyone else. This caps
// each model separately, so a hot model fails over to the next combo member
// while every other model on the same provider keeps serving.
//
// Scope: one bucket per (scope, provider, model). Scope is, in order:
//
//  1. the client session id, when the client sent a stable one;
//  2. a short non-reversible hash of the router API key — one API customer,
//     one budget per model;
//  3. a shared bucket when neither is known.
//
// So one customer's hot model never blocks another customer's, and one hot
// model never blocks a different model on the same provider.
//
// In-memory only: no schema change, no new dependency, no timer or polling
// loop — stale entries are pruned lazily on access, the way the login limiter
// beside it does. Windows reset on restart, which is the correct behaviour for
// a rate limiter. Every function fails open on bad input instead of ever
// breaking chat.

const (
	// perModelDefaultLimit and perModelDefaultWindowMs are deliberately
	// generous for a busy agent client: an agent sends roughly one request per
	// turn and its turns are sequential, so normal traffic never notices the
	// limit while a tight retry loop hits it within a minute and fails over.
	perModelDefaultLimit    = 120
	perModelDefaultWindowMs = 60_000

	// perModelMaxBuckets caps the table. Beyond it the oldest touched bucket is
	// dropped, so a provider list that churns cannot grow memory without bound.
	perModelMaxBuckets = 5000

	perModelMaxLimit      = 10000
	perModelMinWindowMs   = 1000
	perModelMaxWindowMs   = 3_600_000
	perModelMaxTimestamps = 10000
)

// perModelBucketKey is (provider, model, scope) in one comparable key.
type perModelBucketKey struct {
	provider string
	model    string
	scope    string
}

// perModelBucket is one key's window: timestamps in ascending order, pruned on
// access.
type perModelBucket struct {
	times     []time.Time
	touchedAt time.Time
}

// PerModelRateLimiter is the process-wide table. Use the package instance.
type PerModelRateLimiter struct {
	mu      sync.Mutex
	buckets map[perModelBucketKey]*perModelBucket
	now     func() time.Time // swapped by tests
}

// NewPerModelRateLimiter returns an empty limiter on the wall clock.
func NewPerModelRateLimiter() *PerModelRateLimiter {
	return &PerModelRateLimiter{buckets: make(map[perModelBucketKey]*perModelBucket), now: time.Now}
}

// perModelLimiter is the shared instance the dispatch path consults.
var perModelLimiter = NewPerModelRateLimiter()

// PerModelLimit is the resolved configuration for one provider+model.
type PerModelLimit struct {
	Enabled  bool
	Limit    int
	WindowMs int
}

// resolvePerModelRateLimit reads settings.perModelRateLimit. Every field is
// optional; precedence is per-model override, then per-provider override, then
// the global value, then the defaults. `enabled: false` or `limit: 0` turns the
// limiter off for that scope, and the values are clamped so a typo cannot wedge
// chat.
func resolvePerModelRateLimit(settings *db.SettingsData, provider, model string) PerModelLimit {
	off := PerModelLimit{WindowMs: perModelDefaultWindowMs}
	if settings == nil || settings.PerModelRateLimit == nil {
		return PerModelLimit{Enabled: true, Limit: perModelDefaultLimit, WindowMs: perModelDefaultWindowMs}
	}
	cfg := settings.PerModelRateLimit
	scoped := cfg.Overrides[provider+"/"+model]
	if scoped == nil {
		scoped = cfg.Overrides[provider]
	}

	enabled := cfg.Enabled
	limit := cfg.Limit
	windowMs := cfg.WindowMs
	if scoped != nil {
		if scoped.Enabled != nil {
			enabled = scoped.Enabled
		}
		if scoped.Limit > 0 {
			limit = scoped.Limit
		}
		if scoped.WindowMs > 0 {
			windowMs = scoped.WindowMs
		}
	}

	if enabled != nil && !*enabled {
		return off
	}
	if limit == 0 {
		return off
	}
	if limit <= 0 {
		limit = perModelDefaultLimit
	}
	if limit > perModelMaxLimit {
		limit = perModelMaxLimit
	}
	if windowMs < perModelMinWindowMs {
		windowMs = perModelDefaultWindowMs
	}
	if windowMs > perModelMaxWindowMs {
		windowMs = perModelMaxWindowMs
	}
	return PerModelLimit{Enabled: true, Limit: limit, WindowMs: windowMs}
}

// allow reports whether one more request may enter the bucket, recording it
// when it may. An empty or disabled limit always allows.
func (l *PerModelRateLimiter) allow(provider, model, scope string, limit PerModelLimit) (bool, time.Duration) {
	if !limit.Enabled || limit.Limit <= 0 {
		return true, 0
	}
	key := perModelBucketKey{provider: provider, model: model, scope: scope}
	window := time.Duration(limit.WindowMs) * time.Millisecond

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bucket := l.bucketFor(key, now)
	l.prune(bucket, now, window)

	if len(bucket.times) >= limit.Limit {
		// The client may retry once the oldest request in the window falls out.
		return false, bucket.times[0].Add(window).Sub(now)
	}
	if len(bucket.times) >= perModelMaxTimestamps {
		bucket.times = bucket.times[1:]
	}
	bucket.times = append(bucket.times, now)
	return true, 0
}

// bucketFor returns the bucket for key, evicting when the table is at its cap.
// Caller holds the lock.
func (l *PerModelRateLimiter) bucketFor(key perModelBucketKey, now time.Time) *perModelBucket {
	if bucket, ok := l.buckets[key]; ok {
		bucket.touchedAt = now
		return bucket
	}
	if len(l.buckets) >= perModelMaxBuckets {
		l.evictOldest(key)
	}
	bucket := &perModelBucket{touchedAt: now}
	l.buckets[key] = bucket
	return bucket
}

// evictOldest drops the least recently touched bucket. Caller holds the lock.
func (l *PerModelRateLimiter) evictOldest(keep perModelBucketKey) {
	var victim perModelBucketKey
	var victimAt time.Time
	first := true
	for key, bucket := range l.buckets {
		if key == keep {
			continue
		}
		if first || bucket.touchedAt.Before(victimAt) {
			victim, victimAt, first = key, bucket.touchedAt, false
		}
	}
	if !first {
		delete(l.buckets, victim)
	}
}

// prune drops timestamps that fell out of the window. It deletes the bucket
// only when pruning actually emptied it — a bucket that was created for this
// request is legitimately empty and must stay in the map, or every request
// would start a fresh count and the cap would never trip.
// Caller holds the lock.
func (l *PerModelRateLimiter) prune(bucket *perModelBucket, now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	i := 0
	for i < len(bucket.times) && !bucket.times[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return
	}
	// Keep the (now empty) bucket in the map: the caller is about to record
	// this request into it, and a delete here would detach the bucket it then
	// appends to, so the count would start over on every request. Empty buckets
	// cost nothing and are evicted at the cap.
	bucket.times = bucket.times[i:]
}

// reset empties the table (tests).
func (l *PerModelRateLimiter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buckets = make(map[perModelBucketKey]*perModelBucket)
}

// perModelScope derives the bucket scope for one request: the client session
// id when there is one, else a hashed router API key, else the shared bucket.
func perModelScope(sessionID, apiKey string) string {
	if sessionID != "" {
		return "session:" + sessionID
	}
	if apiKey != "" {
		return "key:" + shortKeyHash(apiKey)
	}
	return "shared"
}

// shortKeyHash is a bucketing-only hash of a router API key, so the limiter can
// scope per customer without ever storing or logging the key. djb2, not a
// security hash: a collision only shares a budget. The raw key never appears in
// a bucket key or a return value.
func shortKeyHash(apiKey string) string {
	var h uint32 = 5381
	for i := 0; i < len(apiKey); i++ {
		h = h*33 + uint32(apiKey[i])
	}
	return fmt.Sprintf("%08x", h)
}
