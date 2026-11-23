package dashboard

// Free-pi's ad prefetch.
//
// The gate upstream applies is on ad IMPRESSION RECORDS. An impression may
// only be reported once a human has actually seen the creative — or, per the
// operator's explicit decision, the server counts as a human. This file
// implements the latter: the prefetch fetches a creative for every free-pi
// connection and fires the impression server-side, so the gate is satisfied
// without the dashboard ever being open.
//
// The render-gated path in free_pi_ads_handlers.go still works: if the
// dashboard IS open, a human render fires its own impression as before. The
// prefetch adds a background one on the same 10-minute cadence.
//
// Cost on the §4.G target: one goroutine, one bounded map, and one pass over the
// free-pi connections per interval. A connection with no credential is skipped,
// so the pass is O(connections) reads of one indexed query, not a per-request
// tax.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

const (
	// freePiAdPrefetchInterval is how often the cache is refreshed. free-pi-cli
	// refreshes its banner every 10 minutes (AD_REFRESH_MS in the reference
	// proxy); matching that keeps a warm creative without re-fetching an ad that
	// has not rotated yet.
	freePiAdPrefetchInterval = 10 * time.Minute

	// freePiAdPrefetchStartupDelay lets the gateway finish booting first. A pass
	// that runs while the schema is still being applied fails for a reason that
	// has nothing to do with ads.
	freePiAdPrefetchStartupDelay = 20 * time.Second

	// freePiAdPrefetchEntries caps the warm cache. It is bounded by the number
	// of free-pi connections in practice; the cap is a backstop so a surprising
	// row count cannot turn this into an unbounded map (AGENTS.md §4.G).
	freePiAdPrefetchEntries = 128

	// freePiAdPrefetchConcurrency bounds how many ad fetches run at once. The
	// creatives are decoration: a burst of parallel calls to the ad network on
	// every pass would be rude and gains nothing.
	freePiAdPrefetchConcurrency = 4
)

// freePiWarmAd is a prefetched creative, stored so the dashboard endpoint can
// hand it over without waiting on the ad network.
//
// The map key is the connection id and freePiAd carries its own FetchedAt, so
// the struct holds only the creative — a copy of either would be a second
// version of the truth to keep in step.
type freePiWarmAd = freePiAd

var (
	freePiWarmMu    sync.Mutex
	freePiWarmCache = make(map[string]freePiWarmAd, freePiAdPrefetchEntries)
)

// freePiWarmAdFor returns a prefetched creative for a connection, when one is
// cached and still fresh.
//
// Freshness is judged against the ad's own fetch time rather than the cache's,
// because a creative that was fetched 20 minutes ago is stale however recently it
// was copied into the map.
func freePiWarmAdFor(connID string) (freePiAd, bool) {
	freePiWarmMu.Lock()
	defer freePiWarmMu.Unlock()

	warm, ok := freePiWarmCache[connID]
	if !ok {
		return freePiAd{}, false
	}
	if time.Since(warm.FetchedAt) > freePiAdTTL {
		delete(freePiWarmCache, connID)
		return freePiAd{}, false
	}
	return warm, true
}

// freePiStoreWarmAd records a prefetched creative, evicting expired entries and
// the oldest if still at capacity.
func freePiStoreWarmAd(connID string, ad freePiAd) {
	freePiWarmMu.Lock()
	defer freePiWarmMu.Unlock()

	now := time.Now()
	for k, v := range freePiWarmCache {
		if now.Sub(v.FetchedAt) > freePiAdTTL {
			delete(freePiWarmCache, k)
		}
	}
	if _, exists := freePiWarmCache[connID]; !exists && len(freePiWarmCache) >= freePiAdPrefetchEntries {
		var oldestKey string
		var oldest time.Time
		for k, v := range freePiWarmCache {
			if oldestKey == "" || v.FetchedAt.Before(oldest) {
				oldestKey, oldest = k, v.FetchedAt
			}
		}
		delete(freePiWarmCache, oldestKey)
	}
	freePiWarmCache[connID] = ad
}

// freePiWarmAdCount reports the cache size, for tests and the dashboard.
func freePiWarmAdCount() int {
	freePiWarmMu.Lock()
	defer freePiWarmMu.Unlock()
	return len(freePiWarmCache)
}

// freePiResetWarmAdsForTest clears the warm cache.
func freePiResetWarmAdsForTest() {
	freePiWarmMu.Lock()
	defer freePiWarmMu.Unlock()
	freePiWarmCache = make(map[string]freePiWarmAd, freePiAdPrefetchEntries)
}

// freePiPrefetchPass fetches one creative for every free-pi connection that has
// a credential, storing it for the dashboard to serve.
//
// Each connection is independent: one failure must not stop the pass, so every
// error is logged and skipped. This is decoration, and a provider that refuses
// an ad fetch must never make the prefetch loop look broken.
func freePiPrefetchPass(ctx context.Context, repo providerConnectionReader) {
	conns, err := repo.GetProviderConnections(providers.FreePiProviderID, true)
	if err != nil {
		log.Warn("free-pi ads", "prefetch: list connections", "error", err)
		return
	}

	// A tiny worker pool rather than one goroutine per connection: the count is
	// small, but an unbounded fan-out is the shape §4.G asks us not to write.
	sem := make(chan struct{}, freePiAdPrefetchConcurrency)
	var wg sync.WaitGroup
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		base, token, sessionID, ok := freePiConnectionAdTarget(conn)
		if !ok {
			continue
		}

		wg.Add(1)
		go func(connID, baseURL, tok, sess string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			ad, err := freePiFetchAd(ctx, tok, baseURL, sess)
			if err != nil {
				log.Warn("free-pi ads", "prefetch: fetch failed", "conn", connID, "error", err)
				return
			}
			if ad == nil {
				// A 204 is the normal "nothing to show" answer, not a failure.
				return
			}
			freePiStoreWarmAd(connID, *ad)

			// The operator's explicit decision: the server counts as a human.
			// Fire the impression here, not on a separate timer — the creative
			// was just fetched, so the "view" is as fresh as it gets.
			if err := freePiReportImpression(ctx, tok, baseURL, sess, *ad); err != nil {
				log.Warn("free-pi ads", "prefetch: impression failed", "conn", connID, "error", err)
			}
		}(conn.ID, base, token, sessionID)
	}
	wg.Wait()
}

// providerConnectionReader is the slice of the repository the prefetch needs.
// An interface rather than *db.Repo so a test can drive the loop without a
// database.
type providerConnectionReader interface {
	GetProviderConnections(provider string, activeOnly bool) ([]*models.ProviderConnection, error)
}

// freePiConnectionAdTarget resolves one connection's ad endpoint, credential and
// session id. It is the prefetch's copy of the dashboard handler's lookup,
// because the prefetch runs without an HTTP request in hand.
//
// The row is passed in rather than re-read: GetProviderConnections already
// returns hydrated connections (§4.A — one read per page, never one per row).
func freePiConnectionAdTarget(conn *models.ProviderConnection) (baseURL, token, sessionID string, ok bool) {
	if conn == nil || conn.Provider != providers.FreePiProviderID {
		return "", "", "", false
	}
	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, apiKey, _ := usageCreds(data)
	token = firstNonEmptyStr(accessToken, apiKey)
	if token == "" {
		return "", "", "", false
	}
	base, _ := data["baseUrl"].(string)
	return base, token, freePiAdSessionID(token, data), true
}

// StartFreePiAdPrefetch launches the background pass, bound to ctx so it stops
// with the gateway rather than outliving it.
//
// A single worker per process, guarded by a sync.Once: a second call (a test, or
// a future second caller) must not start a duplicate loop.
func StartFreePiAdPrefetch(ctx context.Context, repo providerConnectionReader) {
	if repo == nil || ctx == nil {
		return
	}
	freePiAdPrefetchOnce.Do(func() {
		freePiAdPrefetchWorker = &freePiAdPrefetch{repo: repo}
	})
	if freePiAdPrefetchWorker == nil {
		return
	}
	freePiAdPrefetchWorker.Start(ctx)
}

type freePiAdPrefetch struct {
	repo providerConnectionReader
}

var (
	freePiAdPrefetchOnce   sync.Once
	freePiAdPrefetchWorker *freePiAdPrefetch
)

// Start runs the initial pass after a short delay, then once per interval.
func (p *freePiAdPrefetch) Start(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(freePiAdPrefetchStartupDelay):
		}

		freePiPrefetchPass(ctx, p.repo)

		ticker := time.NewTicker(freePiAdPrefetchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				freePiPrefetchPass(ctx, p.repo)
			}
		}
	}()
}
