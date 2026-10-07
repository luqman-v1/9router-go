package middleware

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/observ"
)

// RateLimiter implements per-key rate limiting with sliding window RPM,
// token bucket TPM, and concurrency control.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*slidingWindow
	tokens  map[string]*tokenBucket
	active  map[string]*int32
	window  time.Duration
	maxKeys int
}

type slidingWindow struct {
	stamps []time.Time
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewRateLimiter creates a new rate limiter with the given window size.
func NewRateLimiter(window time.Duration) *RateLimiter {
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		windows: make(map[string]*slidingWindow),
		tokens:  make(map[string]*tokenBucket),
		active:  make(map[string]*int32),
		window:  window,
		maxKeys: 10000,
	}
}

// Check verifies if a request is allowed under the given limits.
// Returns (allowed, retryAfterSec).
func (rl *RateLimiter) Check(keyID string, rpm, tpm, concurrency int, promptTokens int) (bool, int) {
	now := time.Now()

	// Concurrency check
	if concurrency > 0 {
		if !rl.checkConcurrency(keyID, concurrency) {
			return false, 1
		}
	}

	// RPM check (sliding window)
	if rpm > 0 {
		if !rl.checkRPM(keyID, rpm, now) {
			retryAfter := rl.rpmRetryAfter(keyID, now)
			return false, retryAfter
		}
	}

	// TPM check (token bucket)
	if tpm > 0 {
		if !rl.checkTPM(keyID, tpm, promptTokens, now) {
			retryAfter := rl.tpmRetryAfter(keyID, tpm, promptTokens)
			return false, retryAfter
		}
	}

	return true, 0
}

// ReleaseConcurrency decrements the concurrency counter for a key.
func (rl *RateLimiter) ReleaseConcurrency(keyID string) {
	rl.mu.Lock()
	counter, exists := rl.active[keyID]
	rl.mu.Unlock()
	if exists && counter != nil {
		atomic.AddInt32(counter, -1)
	}
}

// SettleTokenUsage reconciles a reservation against what the turn actually cost.
//
// The pre-dispatch charge is an estimate read off the request body; the real
// cost is only known once the response has been metered. Without this the bucket
// drifts in one direction forever: an over-estimate permanently starves a key
// that is well inside its budget, and an under-estimate lets it spend past the
// very limit that exists to bound it.
//
// Only an under-estimate is corrected. Refunding the surplus would let a client
// bank credit by over-stating its prompt, which is precisely what a token limit
// has to resist.
func (rl *RateLimiter) SettleTokenUsage(keyID string, tpm, reserved, actual int) {
	if tpm <= 0 || keyID == "" || actual <= reserved {
		return
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	bucket, exists := rl.tokens[keyID]
	if !exists {
		return
	}
	bucket.tokens -= float64(actual - reserved)
	if bucket.tokens < 0 {
		bucket.tokens = 0
	}
}

func (rl *RateLimiter) checkRPM(keyID string, rpm int, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.evictIfNeeded()

	window, exists := rl.windows[keyID]
	if !exists {
		window = &slidingWindow{}
		rl.windows[keyID] = window
	}

	// Remove timestamps outside the window
	cutoff := now.Add(-rl.window)
	valid := window.stamps[:0]
	for _, t := range window.stamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	window.stamps = valid

	if len(window.stamps) >= rpm {
		return false
	}

	window.stamps = append(window.stamps, now)
	return true
}

func (rl *RateLimiter) rpmRetryAfter(keyID string, now time.Time) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	window, exists := rl.windows[keyID]
	if !exists || len(window.stamps) == 0 {
		return 1
	}

	oldest := window.stamps[0]
	retryAfter := rl.window - now.Sub(oldest)
	if retryAfter < 0 {
		retryAfter = 0
	}
	return int(retryAfter.Seconds()) + 1
}

func (rl *RateLimiter) checkTPM(keyID string, tpm int, tokens int, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists := rl.tokens[keyID]
	if !exists {
		bucket = &tokenBucket{
			tokens:     float64(tpm),
			lastRefill: now,
		}
		rl.tokens[keyID] = bucket
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	if elapsed > 0 {
		refillRate := float64(tpm) / rl.window.Seconds()
		bucket.tokens += elapsed * refillRate
		if bucket.tokens > float64(tpm) {
			bucket.tokens = float64(tpm)
		}
		bucket.lastRefill = now
	}

	if bucket.tokens < float64(tokens) {
		return false
	}

	bucket.tokens -= float64(tokens)
	return true
}

func (rl *RateLimiter) tpmRetryAfter(keyID string, tpm int, tokens int) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists := rl.tokens[keyID]
	if !exists {
		return 1
	}

	deficit := float64(tokens) - bucket.tokens
	if deficit <= 0 {
		return 1
	}

	refillRate := float64(tpm) / rl.window.Seconds()
	retryAfter := deficit / refillRate
	return int(retryAfter) + 1
}

func (rl *RateLimiter) checkConcurrency(keyID string, maxConcurrent int) bool {
	rl.mu.Lock()
	counter, exists := rl.active[keyID]
	if !exists {
		c := new(int32)
		rl.active[keyID] = c
		counter = c
	}
	rl.mu.Unlock()

	current := atomic.AddInt32(counter, 1)
	if current > int32(maxConcurrent) {
		atomic.AddInt32(counter, -1)
		return false
	}
	return true
}

func (rl *RateLimiter) evictIfNeeded() {
	if len(rl.windows) < rl.maxKeys {
		return
	}
	// Simple eviction: clear oldest 10% of keys
	for k := range rl.windows {
		delete(rl.windows, k)
		if len(rl.windows) < rl.maxKeys*9/10 {
			break
		}
	}
}

// RequireRateLimit creates a middleware that enforces per-key rate limits.
//
// TPM is charged against the request's own prompt tokens, so the body is read
// once here to size it and then handed to the next handler intact. Reading it
// unconditionally would buffer large uploads for every request, so it is only
// read when a TPM limit is actually configured.
func RequireRateLimit(rl *RateLimiter, defaults Defaults) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := GetAuthenticatedApiKey(r)
			if apiKey == nil {
				next.ServeHTTP(w, r)
				return
			}

			limits := limitsFor(apiKey, defaults)
			promptTokens := 0
			if limits.TPM > 0 {
				promptTokens = promptTokensForRequest(r)
			}

			allowed, retryAfter := rl.Check(apiKey.ID, limits.RPM, limits.TPM, limits.Concurrency, promptTokens)
			if !allowed {
				rejectRateLimited(w, apiKey.ID, limits, retryAfter)
				return
			}

			// Release concurrency after request completes
			if limits.Concurrency > 0 {
				defer rl.ReleaseConcurrency(apiKey.ID)
			}

			// A TPM charge is an estimate; the meter reconciles it once the real
			// usage is known. The reservation travels on the context so the
			// settle hook is only installed for requests that were actually
			// charged.
			if limits.TPM > 0 {
				r = r.WithContext(withReservedTokens(r.Context(), promptTokens))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// EffectiveTPM returns the TPM limit that applies to a key, resolving the
// key's column against the operator's global default exactly as the limiter
// does.
//
// The metering path needs the same number the limiter charged: reconciling the
// real usage against a different limit would settle the charge into the wrong
// bucket.
func EffectiveTPM(d Defaults, apiKey *models.APIKey) int {
	return limitsFor(apiKey, d).TPM
}

// reservedCtxKey carries the TPM charge the limiter took for this request, and
// tokenSettlerKey the hook that reconciles it once the real usage is known.
type reservedCtxKey struct{}

type tokenSettlerKey struct{}

// withReservedTokens records the TPM reservation on the context so the metering
// path can settle it.
func withReservedTokens(ctx context.Context, reserved int) context.Context {
	return context.WithValue(ctx, reservedCtxKey{}, reserved)
}

// ReservedTokensFromContext returns the TPM reservation, or false when no TPM
// limit applied and therefore nothing was charged.
func ReservedTokensFromContext(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	v, ok := ctx.Value(reservedCtxKey{}).(int)
	return v, ok
}

// WithTokenSettler installs the hook the metering path calls with the turn's
// real token count.
func WithTokenSettler(ctx context.Context, settle func(actual int)) context.Context {
	if ctx == nil || settle == nil {
		return ctx
	}
	return context.WithValue(ctx, tokenSettlerKey{}, settle)
}

// SettleTokenUsageFromContext reconciles the reservation against actual usage,
// if this request had one. A request that was never charged, or whose handler
// never reached the meter, simply has nothing to settle.
func SettleTokenUsageFromContext(ctx context.Context, actual int) {
	if ctx == nil {
		return
	}
	if settle, ok := ctx.Value(tokenSettlerKey{}).(func(int)); ok {
		settle(actual)
	}
}

// Defaults supplies the operator's global fallback, read fresh per request so
// a settings change takes effect without a restart.
type Defaults func() Limits

// Limits is the effective limit set for one key.
type Limits struct {
	RPM         int
	TPM         int
	Concurrency int
}

// limitsFor resolves the effective limits for a key.
//
// The key's own column always wins where it is set; the global default only
// fills a column the operator left at 0. That ordering is what keeps a global
// default from quietly capping a key that was deliberately given a higher
// budget, and from lifting one that was deliberately capped. The plan tier the
// port plan describes sits between the two and is not implemented yet.
func limitsFor(apiKey *models.APIKey, d Defaults) Limits {
	// A nil Defaults means the operator configured no global fallback, which
	// is the same as a fallback of zero. Every install without the settings
	// keys reaches here, so it must not be a nil call.
	var out Limits
	if d != nil {
		out = d()
	}
	if v := derefInt(apiKey.RateLimitRPM); v > 0 {
		out.RPM = v
	}
	if v := derefInt(apiKey.RateLimitTPM); v > 0 {
		out.TPM = v
	}
	if v := derefInt(apiKey.RateLimitConcurrency); v > 0 {
		out.Concurrency = v
	}
	return out
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// rejectRateLimited answers a refused request.
//
// The headers name which limit was hit rather than a single X-RateLimit-Limit,
// because a key can be bounded on three independent axes at once and a client
// that has to guess cannot back off correctly. X-RateLimit-Limit still carries
// RPM for the clients that only read the standard name.
func rejectRateLimited(w http.ResponseWriter, keyID string, limits Limits, retryAfter int) {
	log.Warn("rate limit", "key", keyID, "retryAfter", retryAfter)
	observ.IncRateLimitReject(rateLimitScope(limits), keyID)

	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	if limits.RPM > 0 {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limits.RPM))
		w.Header().Set("X-RateLimit-Limit-RPM", strconv.Itoa(limits.RPM))
	}
	if limits.TPM > 0 {
		w.Header().Set("X-RateLimit-Limit-TPM", strconv.Itoa(limits.TPM))
	}
	if limits.Concurrency > 0 {
		w.Header().Set("X-RateLimit-Limit-Concurrency", strconv.Itoa(limits.Concurrency))
	}
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Duration(retryAfter)*time.Second).Unix(), 10))

	handlerutil.WriteJSONError(w, http.StatusTooManyRequests,
		fmt.Sprintf("Rate limit exceeded. Retry after %ds.", retryAfter))
}

// rateLimitScope names which axis refused the request, for the metric label.
// The label is bounded to these five values so a key id can never become a
// metric cardinality explosion.
func rateLimitScope(l Limits) string {
	switch {
	case l.Concurrency > 0 && l.RPM == 0 && l.TPM == 0:
		return "concurrency"
	case l.TPM > 0 && l.RPM == 0:
		return "tpm"
	default:
		return "rpm"
	}
}

// maxTokenScanBody caps how much of a body the limiter buffers to size a TPM
// charge. A prompt past this is charged from its declared length instead of
// being read, so a large multimodal or base64 payload cannot make every
// request hold two copies of itself in memory.
const maxTokenScanBody = 4 << 20 // 4 MiB

// promptTokensForRequest sizes the prompt for the TPM charge, leaving r.Body
// readable for the next handler.
func promptTokensForRequest(r *http.Request) int {
	if r.Body == nil || r.Body == http.NoBody {
		return 0
	}
	// A body too large to buffer is charged from the declared length, which
	// over-counts relative to a real tokenizer. Biasing high is the safe
	// direction for a limit: it can reject a request that would have fit,
	// never admit one that would not.
	if r.ContentLength > maxTokenScanBody {
		return int(r.ContentLength/4) + 1
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTokenScanBody))
	if err != nil {
		// The body is consumed on error and cannot be handed on. Charging the
		// request and letting the handler see a closed body would turn a
		// limiter problem into a request failure, so the limiter declines to
		// judge it.
		return 0
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return estimatePromptTokens(body)
}
