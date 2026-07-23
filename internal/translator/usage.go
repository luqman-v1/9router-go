package translator

import (
	"context"
	"sync"
	"time"
)

var (
	statesMu sync.Mutex
	states   = make(map[string]*StreamState)
)

func pruneStaleStatesLocked() {
	if len(states) < 50 {
		return
	}
	now := time.Now()
	for k, v := range states {
		if v == nil || v.CreatedAt.IsZero() || now.Sub(v.CreatedAt) > 10*time.Minute {
			delete(states, k)
		}
	}
}

// GetStreamUsage returns the accumulated usage for a stream session and removes it.
func GetStreamUsage(sessionKey string) *OpenAIUsage {
	statesMu.Lock()
	defer statesMu.Unlock()
	pruneStaleStatesLocked()
	if state, ok := states[sessionKey]; ok {
		delete(states, sessionKey)
		if state.Usage != nil {
			usage := *state.Usage
			return &usage
		}
	}
	return nil
}

// ClearStreamState removes a stream session state.
func ClearStreamState(sessionKey string) {
	statesMu.Lock()
	defer statesMu.Unlock()
	pruneStaleStatesLocked()
	delete(states, sessionKey)
}

// Global last-usage capture for the non-streaming path.
// Deprecated: use SetUsage and GetAndClearUsage with context instead.
var lastUsage *OpenAIUsage
var lastUsageMu sync.Mutex

// SetLastUsage stores usage from a completed stream translation.
// Deprecated: use SetUsage instead.
func SetLastUsage(u *OpenAIUsage) {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	lastUsage = u
}

// GetAndClearLastUsage returns and clears the stored last usage.
// Deprecated: use GetAndClearUsage instead.
func GetAndClearLastUsage() *OpenAIUsage {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	u := lastUsage
	lastUsage = nil
	return u
}

// usageCtxKey is the context key for per-request usage storage.
type usageCtxKey struct{}

// usageHolder stores usage per-request in context.
type usageHolder struct {
	mu    sync.Mutex
	usage *OpenAIUsage
}

// WithUsageCapture returns a new context that captures usage per-request.
func WithUsageCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, usageCtxKey{}, &usageHolder{})
}

// SetUsage stores usage in the request context. Falls back to global if context has no holder.
func SetUsage(ctx context.Context, u *OpenAIUsage) {
	if ctx != nil {
		if h, ok := ctx.Value(usageCtxKey{}).(*usageHolder); ok {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.usage = u
			return
		}
	}
	// Fallback to global for backward compatibility
	SetLastUsage(u)
}

// GetAndClearUsage retrieves and clears usage from the request context (or global fallback if no holder).
func GetAndClearUsage(ctx context.Context) *OpenAIUsage {
	if ctx != nil {
		if h, ok := ctx.Value(usageCtxKey{}).(*usageHolder); ok {
			h.mu.Lock()
			defer h.mu.Unlock()
			u := h.usage
			h.usage = nil
			return u
		}
	}
	return GetAndClearLastUsage()
}
