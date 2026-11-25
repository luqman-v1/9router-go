package chat

import (
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

// Proxy cooldowns are keyed by provider AND proxy, never by the proxy alone.
//
// A 429 FreeUsageLimitError is one provider's gate passing judgement on one
// egress IP. It says nothing about that IP's health for any other provider, so
// a proxy that OpenCode has closed is still a perfectly good egress for Gemini
// or Claude. Keying on the proxy alone would silently shrink the pool for
// providers that never refused it — the operator would see traffic leave from
// the host's real IP with no error to explain why.
//
// The proxy is identified by its configured URL rather than by the exit IP it
// resolved to. The URL is what the operator assigned and what appears in the
// pool, and it is stable: two proxies in one pool were measured exiting via
// different addresses than their hostnames, so the observed IP would be both
// unstable and surprising as a key.

// proxyCooldownEntries bounds the map. A cooldown is dropped by expiry, but a
// pool that keeps rotating through distinct URLs would grow the map between
// reads, so the size is capped as well (AGENTS.md §4.G: bound everything that
// grows).
const proxyCooldownEntries = 2048

// deadProxyCooldown parks a proxy socket that failed at the transport/dial
// layer (connection refused, SOCKS handshake failure, dial timeout) so
// subsequent requests skip dead endpoints instead of re-dialling them.
const deadProxyCooldown = 5 * time.Minute

// openCodeProxyMaxCooldown is the ceiling for an OpenCode free-tier refusal
// parked against one proxy.
//
// It is deliberately its own ceiling rather than a change to maxCooldownFor,
// which caps ACCOUNT cooldowns and is shared by every provider: raising it
// there would hold rate-limited accounts out of rotation for hours on providers
// whose windows are seconds. The 2h maxResetCooldown clamp was left in place
// for accounts by e27ca52d, which recorded the mismatch — OpenCode's real
// free-tier reset is ~21.5h — as cross-provider policy rather than an OpenCode
// fix. This is that fix, scoped to the egress dimension where it belongs: it
// parks a proxy, not an account, so no other provider's behaviour changes.
//
// Six hours covers the observed refusal window with margin while staying well
// inside the ~21.5h reset, so a proxy parked by a transient burst returns the
// same day instead of being written off until tomorrow.
const openCodeProxyMaxCooldown = 6 * time.Hour

// proxyCooldownCeiling returns how long a provider's refusal may park one proxy.
// Every provider shares the generic cap; a provider whose gate measures its
// refusal in hours rather than minutes needs its own, and that list is the one
// place it belongs.
//
// All three OpenCode spellings are listed: the id an operator types is not
// normalised before it reaches here, and a cooldown ceiling that silently
// missed an alias would clamp that lane back to 2h.
func proxyCooldownCeiling(provider string) time.Duration {
	switch provider {
	case "opencode", "opencode-go", "opencode-zen":
		return openCodeProxyMaxCooldown
	}
	return maxResetCooldown
}

// hasProxyCooldownSupport reports whether refusals for provider are recorded.
//
// Recording is opt-in per provider, while the map itself is keyed by provider so
// a later addition needs no migration. Only the OpenCode free tier is wired:
// its gate is the one measured to answer 429 for a spent egress with a window
// measured in hours, and every other provider's 429 is already handled by the
// account cooldown and the retry/backoff path.
func hasProxyCooldownSupport(provider string) bool {
	switch provider {
	case "opencode", "opencode-go", "opencode-zen":
		return true
	}
	return false
}

type proxyCooldownEntry struct {
	until time.Time
}

var (
	proxyCooldownsMu sync.Mutex
	proxyCooldowns   = make(map[string]proxyCooldownEntry, proxyCooldownEntries)
	poolCooldowns    = make(map[string]proxyCooldownEntry, proxyCooldownEntries)
)

// proxyCooldownKey is the map key. The separator cannot appear in a provider id
// or a URL, so two different pairs cannot collide.
//
// The provider is normalised to its canonical id first: an operator may reach
// the same upstream as "opencode", "opencode-go" or "opencode-zen", and those
// must share one cooldown entry rather than holding three parallel windows that
// each let a refused proxy back into rotation.
func proxyCooldownKey(provider, proxyURL string) string {
	return canonicalProxyProvider(provider) + "\x00" + proxyURL
}

// canonicalProxyProvider collapses a provider's aliases onto one id.
func canonicalProxyProvider(provider string) string {
	switch provider {
	case "opencode", "opencode-go", "opencode-zen":
		return "opencode"
	}
	return provider
}

// proxyCoolingDown reports whether proxyURL is parked for provider.
//
// Expiry is lazy: checked on read, so no background goroutine runs for this —
// the same pattern as the opencode retry cooldown and the stable-session cache.
func proxyCoolingDown(provider, proxyURL string, now time.Time) bool {
	if proxyURL == "" {
		return false
	}
	proxyCooldownsMu.Lock()
	defer proxyCooldownsMu.Unlock()
	e, ok := proxyCooldowns[proxyCooldownKey(provider, proxyURL)]
	if !ok {
		return false
	}
	if !now.Before(e.until) {
		delete(proxyCooldowns, proxyCooldownKey(provider, proxyURL))
		return false
	}
	return true
}

// parkProxyCooling records that provider refused proxyURL until now.Add(d).
//
// It extends an existing window rather than shortening it, so concurrent
// refusals cannot race the deadline backwards. A non-positive d is ignored: an
// upstream that names no window must not park an egress at all.
func parkProxyCooling(provider, proxyURL string, d time.Duration, now time.Time) {
	if proxyURL == "" || d <= 0 {
		return
	}
	key := proxyCooldownKey(provider, proxyURL)
	until := now.Add(d)

	proxyCooldownsMu.Lock()
	defer proxyCooldownsMu.Unlock()

	if e, ok := proxyCooldowns[key]; ok && e.until.After(until) {
		return
	}
	if len(proxyCooldowns) >= proxyCooldownEntries {
		pruneProxyCooldownsLocked(now)
		// Pruning found nothing expired, so the map is full of live windows.
		// Evicting the soonest-expiring entry is the least harmful choice: it
		// costs at most one extra attempt on the egress that was closest to
		// being usable again, and forgetting a cooldown can never send a
		// request to an egress that was never refused.
		if len(proxyCooldowns) >= proxyCooldownEntries {
			var oldestKey string
			var oldest time.Time
			for k, e := range proxyCooldowns {
				if oldestKey == "" || e.until.Before(oldest) {
					oldestKey, oldest = k, e.until
				}
			}
			if oldestKey != "" {
				delete(proxyCooldowns, oldestKey)
			}
		}
	}
	proxyCooldowns[key] = proxyCooldownEntry{until: until}
}

// pruneProxyCooldownsLocked drops expired entries. Callers must hold the mutex.
func pruneProxyCooldownsLocked(now time.Time) {
	for k, e := range proxyCooldowns {
		if !now.Before(e.until) {
			delete(proxyCooldowns, k)
		}
	}
}

// resetProxyCooldownsForTest clears the map. Test-only seam: the state is
// process-global, so without it a test that ends on a refused egress leaks the
// window into the next one, which then fails on the cooldown rather than on the
// behaviour it asserts. Mirrors resetOpenCodeRetryCooldownForTest.
func resetProxyCooldownsForTest() {
	proxyCooldownsMu.Lock()
	defer proxyCooldownsMu.Unlock()
	proxyCooldowns = make(map[string]proxyCooldownEntry, proxyCooldownEntries)
	poolCooldowns = make(map[string]proxyCooldownEntry, proxyCooldownEntries)
}

// ResetProxyCooldownsForTesting clears the provider/proxy cooldowns.
//
// Exported for the integration suite, which boots the real router in another
// package and therefore cannot reach the unexported seam.
func ResetProxyCooldownsForTesting() { resetProxyCooldownsForTest() }

// redactProxyURL removes the userinfo from a proxy URL so it can be logged and
// stored as a label. Proxy credentials are secrets in practice — the test pool
// alone carries twenty distinct passwords — and the egress label lands on every
// usage row and in every fallback log line.
func redactProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 || at < schemeEnd {
		return raw
	}
	return raw[:schemeEnd+3] + "***" + raw[at:]
}

// parkRefusedProxy records a provider's refusal of the egress a request just
// used.
//
// The wait is the one the upstream named, not a router invention: Retry-After
// is now plumbed end to end (e27ca52d) and the classifier prefers it over its
// own estimate. It is clamped by the provider's ceiling so a hostile or
// nonsensical value cannot park an egress indefinitely.
//
// With no usable window the proxy is left alone. A refusal with no stated
// duration says nothing about how long the egress is closed, and inventing one
// would remove a proxy from rotation on a guess.
func parkRefusedProxy(provider string, connData *ConnectionData, ue *upstreamError) {
	if connData == nil || !hasProxyCooldownSupport(provider) {
		return
	}
	proxyURL := connData.ResolvedProxyURL
	if proxyURL == "" {
		return
	}
	wait, ok := retryAfterWait(ue)
	if !ok {
		return
	}
	wait = min(wait, proxyCooldownCeiling(provider))

	now := time.Now()
	parkProxyCooling(provider, proxyURL, wait, now)
	log.Warn("proxy", "proxy parked for provider after refusal",
		"provider", provider, "proxy", redactProxyURL(proxyURL),
		"pool", connData.ProxyPoolID, "cooldown", wait)
}

// poolCoolingDown reports whether every URL in poolID is currently parked for
// provider. Checked in-memory during multi-pool rotation so new requests skip
// exhausted single-URL pools ("Imported ...") with zero SQLite queries.
func poolCoolingDown(provider, poolID string, now time.Time) bool {
	if poolID == "" {
		return false
	}
	key := proxyCooldownKey(provider, poolID)
	proxyCooldownsMu.Lock()
	defer proxyCooldownsMu.Unlock()
	e, ok := poolCooldowns[key]
	if !ok {
		return false
	}
	if !now.Before(e.until) {
		delete(poolCooldowns, key)
		return false
	}
	return true
}

// syncPoolCooldown parks pool.ID in poolCooldowns when every URL in pool.URLs
// is currently cooling for provider, expiring as soon as the earliest URL
// becomes eligible again.
func syncPoolCooldown(provider string, pool *db.ProxyPool, now time.Time) {
	if pool == nil || pool.ID == "" || len(pool.URLs) == 0 || !hasProxyCooldownSupport(provider) {
		return
	}
	proxyCooldownsMu.Lock()
	defer proxyCooldownsMu.Unlock()

	var earliest time.Time
	for _, u := range pool.URLs {
		e, ok := proxyCooldowns[proxyCooldownKey(provider, u)]
		if !ok || !now.Before(e.until) {
			return
		}
		if earliest.IsZero() || e.until.Before(earliest) {
			earliest = e.until
		}
	}
	if earliest.IsZero() {
		return
	}

	key := proxyCooldownKey(provider, pool.ID)
	if len(poolCooldowns) >= proxyCooldownEntries {
		for k, e := range poolCooldowns {
			if !now.Before(e.until) {
				delete(poolCooldowns, k)
			}
		}
		if len(poolCooldowns) >= proxyCooldownEntries {
			var oldestKey string
			var oldest time.Time
			for k, e := range poolCooldowns {
				if oldestKey == "" || e.until.Before(oldest) {
					oldestKey, oldest = k, e.until
				}
			}
			if oldestKey != "" {
				delete(poolCooldowns, oldestKey)
			}
		}
	}
	poolCooldowns[key] = proxyCooldownEntry{until: earliest}
}

// parkRefusedProxyWithPool parks the refused proxy URL and synchronises its
// pool-level cooldown entry so multi-pool rotation skips exhausted pools.
func parkRefusedProxyWithPool(provider string, connData *ConnectionData, pool *db.ProxyPool, ue *upstreamError) {
	if connData == nil || !hasProxyCooldownSupport(provider) || connData.ResolvedProxyURL == "" {
		return
	}
	if _, ok := retryAfterWait(ue); ok {
		parkRefusedProxy(provider, connData, ue)
	} else {
		// Free-tier refusal with no Retry-After header: apply a conservative
		// short cooldown so immediate subsequent requests prefer healthy pools.
		now := time.Now()
		parkProxyCooling(provider, connData.ResolvedProxyURL, deadProxyCooldown, now)
		log.Warn("proxy", "proxy parked for provider after refusal (default window)",
			"provider", provider, "proxy", redactProxyURL(connData.ResolvedProxyURL),
			"pool", connData.ProxyPoolID, "cooldown", deadProxyCooldown)
	}
	syncPoolCooldown(provider, pool, time.Now())
}

// parkDeadProxyWithPool parks an unreachable/broken proxy socket for a short
// transport cooldown so subsequent requests do not keep stalling on it.
func parkDeadProxyWithPool(provider string, connData *ConnectionData, pool *db.ProxyPool) {
	if connData == nil || !hasProxyCooldownSupport(provider) || connData.ResolvedProxyURL == "" {
		return
	}
	now := time.Now()
	parkProxyCooling(provider, connData.ResolvedProxyURL, deadProxyCooldown, now)
	syncPoolCooldown(provider, pool, now)
	log.Warn("proxy", "proxy parked after transport failure",
		"provider", provider, "proxy", redactProxyURL(connData.ResolvedProxyURL),
		"pool", connData.ProxyPoolID, "cooldown", deadProxyCooldown)
}
