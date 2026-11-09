package oauth

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"9router/proxy/internal/providers"
)

const (
	refreshResultTTL       = 10 * time.Second
	refreshCacheMaxEntries = 512
)

type refreshCacheEntry struct {
	result    *TokenResult
	expiresAt time.Time
}

var (
	refreshFlight singleflight.Group
	refreshMu     sync.Mutex
	refreshCache  = make(map[string]refreshCacheEntry)
)

func cacheKey(provider, oldToken string) string {
	normalizedProvider := providers.ResolveAlias(provider)
	return normalizedProvider + ":" + oldToken
}

func getCachedRefresh(key string) *TokenResult {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	entry, ok := refreshCache[key]
	if !ok {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		delete(refreshCache, key)
		return nil
	}
	return entry.result
}

func storeRefresh(key string, result *TokenResult) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if len(refreshCache) >= refreshCacheMaxEntries {
		now := time.Now()
		for k, v := range refreshCache {
			if now.After(v.expiresAt) {
				delete(refreshCache, k)
			}
		}
		if len(refreshCache) >= refreshCacheMaxEntries {
			for k := range refreshCache {
				delete(refreshCache, k)
				break
			}
		}
	}
	refreshCache[key] = refreshCacheEntry{
		result:    result,
		expiresAt: time.Now().Add(refreshResultTTL),
	}
}

func CopyTokenResult(result *TokenResult) *TokenResult {
	if result == nil {
		return nil
	}
	return &TokenResult{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
		Scope:        result.Scope,
		ProjectID:    result.ProjectID,
		APIKey:       result.APIKey,
	}
}

func DedupRefresh(ctx context.Context, provider, oldToken string, force bool, fn func(ctx context.Context) (*TokenResult, error)) (*TokenResult, error) {
	if oldToken == "" || provider == "" {
		return fn(ctx)
	}

	key := cacheKey(provider, oldToken)
	if !force {
		if cached := getCachedRefresh(key); cached != nil {
			return CopyTokenResult(cached), nil
		}
	}

	res, err, _ := refreshFlight.Do(key, func() (any, error) {
		if !force {
			if cached := getCachedRefresh(key); cached != nil {
				return cached, nil
			}
		}
		flightCtx := context.WithoutCancel(ctx)
		result, err := fn(flightCtx)
		if err != nil {
			return nil, err
		}
		if result != nil {
			storeRefresh(key, result)
		}
		return result, nil
	})

	if err != nil {
		return nil, err
	}
	result, ok := res.(*TokenResult)
	if !ok {
		return nil, nil
	}
	return CopyTokenResult(result), nil
}

func ResetRefreshCache() {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	refreshCache = make(map[string]refreshCacheEntry)
}
