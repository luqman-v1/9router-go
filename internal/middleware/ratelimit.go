package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
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
func RequireRateLimit(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := GetAuthenticatedApiKey(r)
			if apiKey == nil {
				next.ServeHTTP(w, r)
				return
			}

			rpm := 0
			tpm := 0
			concurrency := 0
			if apiKey.RateLimitRPM != nil {
				rpm = *apiKey.RateLimitRPM
			}
			if apiKey.RateLimitTPM != nil {
				tpm = *apiKey.RateLimitTPM
			}
			if apiKey.RateLimitConcurrency != nil {
				concurrency = *apiKey.RateLimitConcurrency
			}

			promptTokens := 0
			if tpm > 0 {
				promptTokens = promptTokensForRequest(r)
			}

			allowed, retryAfter := rl.Check(apiKey.ID, rpm, tpm, concurrency, promptTokens)
			if !allowed {
				log.Warn("rate limit", "key", apiKey.ID, "retryAfter", retryAfter)
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				if rpm > 0 {
					w.Header().Set("X-RateLimit-Limit-RPM", strconv.Itoa(rpm))
				}
				if tpm > 0 {
					w.Header().Set("X-RateLimit-Limit-TPM", strconv.Itoa(tpm))
				}
				w.Header().Set("X-RateLimit-Remaining", "0")
				handlerutil.WriteJSONError(w, http.StatusTooManyRequests, "Rate limit exceeded. Retry after a few seconds.")
				return
			}

			// Release concurrency after request completes
			if concurrency > 0 {
				defer rl.ReleaseConcurrency(apiKey.ID)
			}

			next.ServeHTTP(w, r)
		})
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
