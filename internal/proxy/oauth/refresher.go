// Package oauth provides per-provider OAuth token refresh.
// Register custom refresh functions for providers that need non-standard OAuth flows.
package oauth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// TokenResult holds the result of a token refresh.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
	Scope        string
	ProjectID    string // provider-specific extra field
	APIKey       string
}

// Params holds all inputs for a refresh call.
type Params struct {
	Client       *http.Client
	Provider     string
	RefreshToken string
	AccessToken  string // current (possibly expired) token
	// ProviderSpecificData carries per-account OAuth material stored at login
	// time (e.g. Kiro clientId/clientSecret/region for AWS SSO OIDC refresh).
	ProviderSpecificData map[string]string
}

// Refresher refreshes an OAuth token for a specific provider.
type Refresher func(ctx context.Context, p *Params) (*TokenResult, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Refresher{}
)

// Register adds a refresher for the given provider.
func Register(provider string, fn Refresher) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = fn
}

// Get returns the refresher for the given provider, or nil.
func Get(provider string) Refresher {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[provider]
}

// Unregister removes the refresher for a provider, restoring the entry to
// "none registered". Tests that register a stub under a real provider id need
// this to put the process-wide registry back the way they found it — a leaked
// stub silently changes what production code under test does.
func Unregister(provider string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, provider)
}

// refreshResultTTL mirrors upstream's REFRESH_RESULT_TTL_MS (10s): the window
// in which a completed refresh answers later callers (open-sse/services/
// tokenRefresh/dedup.js).
const refreshResultTTL = 10 * time.Second

// refreshCacheMaxEntries bounds the result cache. Its key holds a
// client-supplied refresh token, so the map needs a ceiling as well as a TTL
// (AGENTS.md §4.G): a gateway serving many connections sees many distinct
// tokens, and an unbounded map would keep every one of them alive. 256
// entries is far above the number of connections one gateway holds, and
// expired entries are swept on every insert.
const refreshCacheMaxEntries = 256

// refreshCacheEntry is one cached refresh answer. The result is stored as a
// copy: a caller still holding the pointer must never see it change under
// them.
type refreshCacheEntry struct {
	result    *TokenResult
	expiresAt time.Time
}

var (
	refreshFlight singleflight.Group

	refreshCacheMu sync.Mutex
	refreshCache   = map[string]refreshCacheEntry{}
)

// Refresh runs a provider's refresher through upstream's dedup wrapper: the
// flight is keyed on provider + the SPENT refresh token, so two callers that
// hold the same token share one exchange (concurrent) or one exchange's
// result (sequential, for refreshResultTTL).
//
// Why this lives here and not in the callers: every provider refresh goes
// through this one function, so the shared layer covers all of them at once.
// Without it, two refreshes of one account that are not exactly concurrent
// each post their own exchange, and a single-use refresh token sent twice
// answers invalid_grant — which the port classifies as a dead grant and signs
// the account out. The per-caller flights still collapse concurrent work for
// one connection; the result cache is what makes a sequential second call
// safe, because singleflight forgets a flight the moment it completes.
//
// A failure is never cached (upstream deletes its entry in the catch path), so
// a 5xx or a transport error leaves nothing behind and the next attempt
// reaches the endpoint again.
func Refresh(ctx context.Context, p *Params) (*TokenResult, error) {
	if p == nil || p.RefreshToken == "" {
		return refreshUncached(ctx, p)
	}

	key := p.Provider + ":" + p.RefreshToken
	if cached := refreshCacheGet(key); cached != nil {
		return cached, nil
	}

	res, err, _ := refreshFlight.Do(key, func() (any, error) {
		// Another caller may have completed the refresh between the lookup
		// above and the start of this flight, so the answer is re-checked
		// here: the spent token must never be posted twice.
		if cached := refreshCacheGet(key); cached != nil {
			return cached, nil
		}
		result, err := refreshUncached(ctx, p)
		if err != nil {
			return nil, err
		}
		if result != nil {
			refreshCacheStore(key, result)
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	result, ok := res.(*TokenResult)
	if !ok {
		return nil, fmt.Errorf("no OAuth refresher for: %s", p.Provider)
	}
	if result == nil {
		return nil, nil
	}
	// Every caller gets its own copy: the flight's result and the cached
	// result are shared, and a caller must not be able to mutate what the
	// next one reads.
	return copyTokenResult(result), nil
}

// refreshUncached dispatches to the provider's registered refresher, or
// reports that none exists. It is the body Refresh used to run directly.
func refreshUncached(ctx context.Context, p *Params) (*TokenResult, error) {
	if p == nil {
		return nil, fmt.Errorf("oauth: nil params")
	}
	if fn := Get(p.Provider); fn != nil {
		return fn(ctx, p)
	}
	return nil, fmt.Errorf("no OAuth refresher for: %s", p.Provider)
}

// refreshCacheGet returns the cached answer for key, or nil when there is none
// or the entry has expired. The returned value is a copy.
func refreshCacheGet(key string) *TokenResult {
	refreshCacheMu.Lock()
	defer refreshCacheMu.Unlock()

	entry, ok := refreshCache[key]
	if !ok {
		return nil
	}
	if !entry.expiresAt.After(time.Now()) {
		delete(refreshCache, key)
		return nil
	}
	return copyTokenResult(entry.result)
}

// refreshCacheStore caches a successful refresh answer for the TTL. Only
// successes are stored, so a failed refresh leaves nothing behind and the next
// attempt reaches the endpoint again.
func refreshCacheStore(key string, result *TokenResult) {
	if result == nil {
		return
	}
	now := time.Now()

	refreshCacheMu.Lock()
	defer refreshCacheMu.Unlock()

	// Sweep the expired entries first, so the ceiling below only ever
	// rejects live entries.
	for k, entry := range refreshCache {
		if !entry.expiresAt.After(now) {
			delete(refreshCache, k)
		}
	}
	if _, exists := refreshCache[key]; !exists &&
		len(refreshCache) >= refreshCacheMaxEntries {
		// At the ceiling the entry closest to expiry goes: every entry lives
		// the same TTL, so that is the oldest one.
		oldestKey := ""
		var oldestExpiry time.Time
		for k, entry := range refreshCache {
			if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestKey, oldestExpiry = k, entry.expiresAt
			}
		}
		if oldestKey != "" {
			delete(refreshCache, oldestKey)
		}
	}
	refreshCache[key] = refreshCacheEntry{
		result:    copyTokenResult(result),
		expiresAt: now.Add(refreshResultTTL),
	}
}

// ResetRefreshCacheForTesting empties the result cache.
//
// It is the same state resetRefreshCache accesses, exported here so packages
// that register stub refreshers (and therefore swap the implementation that
// produced any cached answer) can keep the cache from handing a test the
// answer a different stub's fixture sent. ResetGlobalDatabaseForTesting is the
// pattern this follows.
func ResetRefreshCacheForTesting() { resetRefreshCache() }

// resetRefreshCache empties the result cache.
//
// It exists for the tests: the cache is process-wide state, so a test that
// leaves an entry behind would hand the next test — or the next repetition of
// the same test under -count — an answer its own fixture never sent.
// Production code has no reason to call it.
func resetRefreshCache() {
	refreshCacheMu.Lock()
	defer refreshCacheMu.Unlock()
	clear(refreshCache)
}

// StringMap flattens a connection providerSpecificData blob to the string
// fields refreshers need (clientId/clientSecret/region). Non-string values
// are dropped.
func StringMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok && s != "" {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
