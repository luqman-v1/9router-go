package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

func init() {
	Register("minimax-code", refreshMiniMaxCode)
	Register("minimax-code-global", refreshMiniMaxCode)
}

// MiniMax Code refresh tokens are SINGLE-USE: sending the same one twice
// answers invalid_grant (HTTP 400) and signs the account out. Two paths reach
// refresh — the pre-dispatch expiry check and the executor's on-401 retry —
// and they may hold different snapshots of one connection, so the flight is
// keyed on the spent token value rather than on the connection id: a token
// already in the air resolves to the shared answer instead of a second send.
// Port of upstream open-sse/services/tokenRefresh/dedup.js (dedupRefresh),
// which upstream calls with the same (provider, oldToken) key.
//
// Upstream keeps two layers and this port now has both. singleflight
// collapses calls that are CONCURRENTLY in flight; the result cache below
// keeps the answer for miniMaxCodeRefreshResultTTL, so a refresh that lands
// a few milliseconds after the previous one finished still reuses it instead
// of posting the same single-use token again. singleflight alone is not
// enough: once the first call completes the flight is gone, and the next
// caller would spend the token a second time and get invalid_grant.
var minimaxCodeFlight singleflight.Group

// miniMaxCodeRefreshResultTTL mirrors upstream's REFRESH_RESULT_TTL_MS (10s):
// the window in which a completed refresh answers later callers.
const miniMaxCodeRefreshResultTTL = 10 * time.Second

// miniMaxCodeRefreshCacheMaxEntries bounds the result cache. Its key holds a
// client-supplied refresh token, so the map needs a ceiling as well as a TTL
// (AGENTS.md §4.G): a gateway serving many connections sees many distinct
// tokens, and an unbounded map would keep every one of them alive. 256
// entries is far above the number of mcode connections one gateway holds,
// and expired entries are swept on every insert.
const miniMaxCodeRefreshCacheMaxEntries = 256

// miniMaxCodeRefreshEntry is one cached refresh answer. The result is stored
// as a copy: a caller still holding the pointer must never see it change
// under them.
type miniMaxCodeRefreshEntry struct {
	result    *TokenResult
	expiresAt time.Time
}

var (
	miniMaxCodeRefreshMu    sync.Mutex
	miniMaxCodeRefreshCache = map[string]miniMaxCodeRefreshEntry{}
)

// miniMaxCodeSites maps each mcode provider id to its site's token endpoint.
// Sign-ins are per site — an account on account.minimax.cn says nothing about
// account.minimax.io — so the two are never cross-falled (AGENTS.md §3.A).
var miniMaxCodeSites = map[string]string{
	"minimax-code":        "https://account.minimax.cn/oauth2/token",
	"minimax-code-global": "https://account.minimax.io/oauth2/token",
}

const (
	miniMaxCodeClientID = "mcode-public"
	miniMaxCodeScope    = "agent.default"
	miniMaxCodeAudience = "agent-backend"
	// miniMaxCodeDefaultExpiresIn covers an answer that omits expires_in. mcode
	// access tokens live about an hour; storing zero instead would mark the
	// connection expired and refresh it again on every single request.
	miniMaxCodeDefaultExpiresIn = 3600
)

// refreshMiniMaxCode refreshes a MiniMax Code OAuth token.
//
// invalid_grant is the only answer that means the sign-in is gone; anything
// else (5xx, transport error) returns an error the caller's grant-dead check
// does not recognise, so the current access token stays usable and the
// connection is not parked on a blip.
func refreshMiniMaxCode(ctx context.Context, p *Params) (*TokenResult, error) {
	tokenURL, known := miniMaxCodeSites[p.Provider]
	if !known || p.RefreshToken == "" {
		return nil, fmt.Errorf("%s: unsupported provider or missing refresh_token", p.Provider)
	}

	// The key is upstream's: provider + the SPENT token value. Two connections
	// holding the same spent token share the answer; two providers never do.
	key := p.Provider + ":" + p.RefreshToken
	if cached := miniMaxCodeCachedRefresh(key); cached != nil {
		return cached, nil
	}

	vals := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {p.RefreshToken},
		"client_id":     {miniMaxCodeClientID},
		"scope":         {miniMaxCodeScope},
		"audience":      {miniMaxCodeAudience},
	}

	res, err, _ := minimaxCodeFlight.Do(key, func() (any, error) {
		// Another caller may have completed the refresh between the lookup
		// above and the start of this flight, so the answer is re-checked
		// here: the spent token must never be posted twice.
		if cached := miniMaxCodeCachedRefresh(key); cached != nil {
			return cached, nil
		}
		result, err := doFormRefresh(ctx, minimaxCodeClient(p.Client), tokenURL, vals)
		if err != nil {
			// A spent token is reported as 400 invalid_grant, which the shared
			// OAuthRefreshError already carries; anything else (5xx, transport)
			// surfaces as-is and is not grant-dead.
			// A failed refresh is NOT cached (upstream deletes the entry in
			// its catch path), so the next attempt retries the endpoint.
			return nil, fmt.Errorf("%s: %w", p.Provider, err)
		}
		// MiniMax rotates the refresh token on every refresh. The old one is
		// kept only when the answer omits it, so a rotation is never lost to a
		// partial body and the next refresh does not resend a spent token.
		if result.RefreshToken == "" {
			result.RefreshToken = p.RefreshToken
		}
		if result.ExpiresIn <= 0 {
			result.ExpiresIn = miniMaxCodeDefaultExpiresIn
		}
		miniMaxCodeStoreRefresh(key, result)
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	result, ok := res.(*TokenResult)
	if !ok || result == nil {
		return nil, fmt.Errorf("%s: refresh returned no token", p.Provider)
	}
	// Every caller gets its own copy: the flight's result and the cached
	// result are shared, and a caller must not be able to mutate what the
	// next one reads.
	return copyTokenResult(result), nil
}

// miniMaxCodeCachedRefresh returns the cached answer for key, or nil when
// there is none or the entry has expired. The returned value is a copy.
func miniMaxCodeCachedRefresh(key string) *TokenResult {
	miniMaxCodeRefreshMu.Lock()
	defer miniMaxCodeRefreshMu.Unlock()

	entry, ok := miniMaxCodeRefreshCache[key]
	if !ok {
		return nil
	}
	if !entry.expiresAt.After(time.Now()) {
		delete(miniMaxCodeRefreshCache, key)
		return nil
	}
	return copyTokenResult(entry.result)
}

// miniMaxCodeStoreRefresh caches a successful refresh answer for the TTL.
// Only successes are stored, so a failed refresh leaves nothing behind and
// the next attempt reaches the endpoint again.
func miniMaxCodeStoreRefresh(key string, result *TokenResult) {
	if result == nil {
		return
	}
	now := time.Now()

	miniMaxCodeRefreshMu.Lock()
	defer miniMaxCodeRefreshMu.Unlock()

	// Sweep the expired entries first, so the ceiling below only ever
	// rejects live entries.
	for k, entry := range miniMaxCodeRefreshCache {
		if !entry.expiresAt.After(now) {
			delete(miniMaxCodeRefreshCache, k)
		}
	}
	if _, exists := miniMaxCodeRefreshCache[key]; !exists &&
		len(miniMaxCodeRefreshCache) >= miniMaxCodeRefreshCacheMaxEntries {
		// At the ceiling the entry closest to expiry goes: every entry lives
		// the same TTL, so that is the oldest one.
		oldestKey := ""
		var oldestExpiry time.Time
		for k, entry := range miniMaxCodeRefreshCache {
			if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestKey, oldestExpiry = k, entry.expiresAt
			}
		}
		if oldestKey != "" {
			delete(miniMaxCodeRefreshCache, oldestKey)
		}
	}
	miniMaxCodeRefreshCache[key] = miniMaxCodeRefreshEntry{
		result:    copyTokenResult(result),
		expiresAt: now.Add(miniMaxCodeRefreshResultTTL),
	}
}

// copyTokenResult duplicates a token answer so no two holders share one
// pointer.
func copyTokenResult(result *TokenResult) *TokenResult {
	if result == nil {
		return nil
	}
	copied := *result
	return &copied
}

// resetMiniMaxCodeRefreshCache empties the result cache.
//
// It exists for the tests: the cache is process-wide state, so a test that
// leaves an entry behind would hand the next test — or the next repetition
// of the same test under -count - an answer its own fixture never sent.
// Production code has no reason to call it.
func resetMiniMaxCodeRefreshCache() {
	miniMaxCodeRefreshMu.Lock()
	defer miniMaxCodeRefreshMu.Unlock()
	clear(miniMaxCodeRefreshCache)
}

// minimaxCodeClient bounds the exchange. The caller's client is the shared
// proxy-aware one whenever it carries a timeout; otherwise a copy (or a fresh
// client) gets one, so no mcode refresh can hang indefinitely.
func minimaxCodeClient(client *http.Client) *http.Client {
	switch {
	case client == nil:
		return &http.Client{Timeout: 30 * time.Second}
	case client.Timeout > 0:
		return client
	default:
		clone := *client
		clone.Timeout = 30 * time.Second
		return &clone
	}
}
