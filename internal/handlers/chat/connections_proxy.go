package chat

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	internalproxy "9router/proxy/internal/proxy"
)

// maxProxyClients caps rotating-proxy growth: each entry pins a Transport
// plus idle sockets, so unbounded distinct URLs (residential rotation)
// would leak descriptors without eviction.
const maxProxyClients = 128

// strictCachePrefix namespaces the InsecureStrict-free, no-direct-replay
// variants in proxyClients: a strict pool and a lax one may point at the same
// URL, and sharing a client would leak the strict decision onto whichever
// connection asked second.
const strictCachePrefix = "strict\x00"

// relayCachePrefix namespaces relay clients in proxyClients. A relay and an
// HTTP proxy can name the same URL — a pool typed "http" whose host is a
// vercel.app deployment is recognised as a relay — so the two variants must not
// share a cache entry.
const relayCachePrefix = "relay\x00"

var (
	proxyClientsMu sync.RWMutex
	proxyClients   = make(map[string]*http.Client)
	// proxyPoolLogged tracks pool IDs already announced at debug level so the
	// "routing via proxy" line appears once per pool, not on every request.
	proxyPoolLogged sync.Map
)

// logProxyOnce records proxy usage once per pool at debug level, e.g.
// proxy routing requests via proxy pool pool=abc123 url=http://... type=http
// Run with LOG_LEVEL=debug and grep for "proxy" to confirm traffic uses the pool.
func logProxyOnce(poolID, proxyURLStr, proxyType string) {
	if poolID == "" {
		return
	}
	if _, loaded := proxyPoolLogged.LoadOrStore(poolID, true); loaded {
		return
	}
	if proxyType == "" {
		proxyType = "http"
	}
	log.Debug("proxy", "routing requests via proxy pool", "pool", poolID, "url", proxyURLStr, "type", proxyType)
}

// GetClientForConnection returns an http.Client configured with ProxyPool transport if set.
// A connection bound to a pool that cannot serve traffic gets an error instead of a
// client: silently dropping the pool would send the request out from the real IP,
// which is the one thing the assignment exists to prevent.
func (h *ChatHandler) GetClientForConnection(connData *ConnectionData) (*http.Client, error) {
	return h.getClientForConnection(connData)
}

func (h *ChatHandler) getClientForConnection(connData *ConnectionData) (*http.Client, error) {
	if connData == nil {
		return h.Client, nil
	}

	var proxyURLStr string
	var proxyType string
	// noProxy is the pool's (or legacy connection's) bypass list. It used to be
	// read only for edge relays, so a dialable pool's field was stored, shown in
	// the dashboard, and ignored on the path that actually dials. An empty
	// string means "never bypass", which is the behaviour for every caller that
	// does not name a list.
	var noProxy string
	// The row's own flag, which the pool below may override. Seeding it here is
	// what makes a legacy connection's strict setting mean anything: the
	// declaration alone would leave it false for every connection that is not
	// bound to a pool.
	strictProxy := connData.StrictProxy
	// Whether the operator meant this connection to leave through a proxy at
	// all. Upstream gates its strictProxy refusal on exactly this
	// (decolua/9router#4333): executors such as Qoder set strictProxy to mean
	// "do not replay this request directly if the proxy fails" — a replayed
	// COSY signature answers 403 — not "a proxy must exist". With nothing
	// configured those callers must keep working direct, so a strict flag
	// alone may never turn into an error.
	proxyIntended := connData.ProxyPoolID != ""

	// 1. Resolve from ProxyPool. A pool that cannot serve traffic (deleted,
	// inactive, or carrying no URL) makes the request fail: silently going
	// direct would publish the operator's egress IP, which is the one thing a
	// proxy was assigned to prevent, and the dashboard would keep showing the
	// connection as proxied.
	if connData.ProxyPoolID != "" {
		pool, err := h.Repo.GetProxyPool(connData.ProxyPoolID)
		switch {
		case err != nil || pool == nil:
			log.Warn("proxy", "assigned pool not found", "pool", connData.ProxyPoolID)
			return nil, fmt.Errorf("proxy pool %q is assigned but does not exist", connData.ProxyPoolID)
		case !pool.IsActive:
			log.Warn("proxy", "assigned pool is inactive", "pool", connData.ProxyPoolID)
			return nil, fmt.Errorf("proxy pool %q is assigned but inactive", connData.ProxyPoolID)
		}
		proxyURLStr = h.nextProxyURL(connData, pool)
		if proxyURLStr == "" {
			log.Warn("proxy", "assigned pool has no url", "pool", connData.ProxyPoolID)
			return nil, fmt.Errorf("proxy pool %q is assigned but carries no url", connData.ProxyPoolID)
		}
		proxyType = pool.Type
		strictProxy = pool.StrictProxy
		noProxy = pool.NoProxy
		// Name the pool for the log line. The lookup already happened, so this
		// costs nothing, and "Vercel Relay" answers a question a UUID cannot.
		connData.ResolvedProxyPool = pool.Name
		// The specific URL rotation chose. A refusal has to name the egress that
		// earned it, and with a hundred proxies behind one pool the pool name says
		// nothing about which one served the turn.
		connData.ResolvedProxyURL = proxyURLStr

		if pool.IsEdgeRelay() {
			// A relay is an HTTPS endpoint that forwards on x-relay-target /
			// x-relay-path, not a CONNECT proxy: dialing it with http.ProxyURL
			// answers "malformed HTTP status code" (see db.ProxyPool.IsEdgeRelay).
			//
			// The client carries the relay as a RoundTripper, so every provider
			// — including one added later, and every request an executor builds
			// itself — leaves through the relay without provider-specific code.
			// The dial moves; the request keeps the URL it was built for.
			logProxyOnce(connData.ProxyPoolID, proxyURLStr, proxyType)
			return h.relayClient(proxyURLStr), nil
		}
	}

	// 2. Fallback to legacy connection proxy
	if proxyURLStr == "" {
		proxyEnabled := connData.ConnectionProxyEnabled
		proxyURL := connData.ConnectionProxyURL
		if connData.ProviderSpecificData != nil {
			if u, ok := connData.ProviderSpecificData["vercelRelayUrl"].(string); ok && strings.TrimSpace(u) != "" {
				return h.relayClient(strings.TrimSpace(u)), nil
			}
			if en, ok := connData.ProviderSpecificData["connectionProxyEnabled"].(bool); ok {
				proxyEnabled = en
			}
			if u, ok := connData.ProviderSpecificData["connectionProxyUrl"].(string); ok {
				proxyURL = u
			}
			if sp, ok := connData.ProviderSpecificData["strictProxy"].(bool); ok {
				strictProxy = sp
			}
			if np, ok := connData.ProviderSpecificData["connectionNoProxy"].(string); ok {
				noProxy = np
			}
		}
		if proxyEnabled || strings.TrimSpace(proxyURL) != "" {
			proxyIntended = true
		}
		if proxyEnabled && proxyURL != "" {
			proxyURLStr = proxyURL
			// The scheme travels in the URL; the dial gate below reads it off the
			// parsed URL, so this only labels the log line.
			proxyType = "http"
		}
	}

	if proxyURLStr == "" {
		if strictProxy && proxyIntended {
			// Strict mode means "never leave over the direct IP". A connection
			// that names a proxy, has one enabled, or is bound to a pool, but
			// resolves no usable URL, is exactly that case: an empty or
			// discarded URL would otherwise go straight out from the real
			// address while the dashboard shows it proxied (#4333).
			log.Error("proxy", "strict proxy enabled but none resolved", "pool", connData.ProxyPoolID, "proxyUrl", connData.ConnectionProxyURL)
			return nil, fmt.Errorf("%w: the connection enables a proxy but resolves none", internalproxy.ErrStrictProxyRequired)
		}
		return h.Client, nil
	}
	logProxyOnce(connData.ProxyPoolID, proxyURLStr, proxyType)

	parsedURL, err := url.Parse(proxyURLStr)
	if err == nil && (parsedURL.Scheme == "" || parsedURL.Host == "") {
		err = fmt.Errorf("no scheme or host in %q", proxyURLStr)
	}
	if err != nil {
		log.Warn("proxy", "invalid proxy pool url", "pool", connData.ProxyPoolID, "url", proxyURLStr, "error", err)
		// The operator named a proxy and the transport cannot dial it. Under a
		// strict connection that is a refusal, not a reason to leave directly.
		if connData.ProxyPoolID != "" {
			// The connection is bound to this pool, so a URL the transport
			// cannot parse is a broken assignment, not a missing one.
			return nil, fmt.Errorf("proxy pool %q has an unparseable url", connData.ProxyPoolID)
		}
		if strictProxy {
			log.Error("proxy", "strict proxy enabled but proxy url invalid", "url", proxyURLStr)
			return nil, fmt.Errorf("%w: unparseable proxy url %q", internalproxy.ErrStrictProxyRequired, proxyURLStr)
		}
		return h.Client, nil
	}

	// noProxy names targets that must not travel through the proxy. It was read
	// only for edge relays, so a dialable pool's bypass list was stored,
	// displayed in the dashboard, and never applied: a target it named still
	// went out through the proxy.
	if target := connData.BaseURL; target != "" && internalproxy.ShouldBypassNoProxy(target, noProxy) {
		log.Debug("proxy", "target matches noProxy, routing direct", "pool", connData.ProxyPoolID, "target", target)
		return h.Client, nil
	}

	if proxyType == "http" || proxyType == "socks5" || proxyType == "socks" || proxyType == "" ||
		strings.HasPrefix(parsedURL.Scheme, "http") || strings.HasPrefix(parsedURL.Scheme, "socks") {
		cacheKey := proxyURLStr
		if strictProxy {
			cacheKey = strictCachePrefix + proxyURLStr
		}
		proxyClientsMu.RLock()
		client, ok := proxyClients[cacheKey]
		proxyClientsMu.RUnlock()
		if ok {
			return client, nil
		}

		proxyClientsMu.Lock()
		defer proxyClientsMu.Unlock()
		if client, ok = proxyClients[cacheKey]; ok {
			return client, nil
		}
		// Evict idle sockets of a random victim when over cap (amortized O(1);
		// exact LRU is overkill — URLs are hot or dead, never warm).
		if len(proxyClients) >= maxProxyClients {
			for victimURL, victim := range proxyClients {
				victim.CloseIdleConnections()
				delete(proxyClients, victimURL)
				break
			}
		}

		baseTransport := internalproxy.NewSafeTransportForProxy(parsedURL.String())
		client = &http.Client{
			Transport: baseTransport,
			Timeout:   h.Client.Timeout,
		}
		if strictProxy {
			// The marker travels with the client, so every caller that dials a
			// strict pool — the chat forwarder, the media lanes, the executors —
			// refuses the direct replay in proxy.DoRequest without having to
			// thread a flag through each of them.
			client = internalproxy.ForbidDirectReplay(client)
		}
		proxyClients[cacheKey] = client
		return client, nil
	}

	// A pool type the transport cannot dial falls back to the connection's
	// plain client. Relay pools never reach here: they are answered above.
	return h.Client, nil
}

// relayClient is the client a relay pool hands out: the connection's own client
// with an edge relay wrapped around its transport.
//
// Clients are cached per relay URL for the same reason the CONNECT ones are —
// each pins a transport and its idle sockets — and the cache is the same map,
// namespaced so a relay and an HTTP proxy on one URL cannot share an entry.
func (h *ChatHandler) relayClient(relayURL string) *http.Client {
	cacheKey := relayCachePrefix + relayURL
	proxyClientsMu.RLock()
	client, ok := proxyClients[cacheKey]
	proxyClientsMu.RUnlock()
	if ok {
		return client
	}

	proxyClientsMu.Lock()
	defer proxyClientsMu.Unlock()
	if client, ok = proxyClients[cacheKey]; ok {
		return client
	}
	base := h.Client
	if base == nil {
		base = http.DefaultClient
	}
	if len(proxyClients) >= maxProxyClients {
		for victimURL, victim := range proxyClients {
			victim.CloseIdleConnections()
			delete(proxyClients, victimURL)
			break
		}
	}
	clone := *base
	clone.Transport = internalproxy.RelayTransport(relayURL, base.Transport)
	proxyClients[cacheKey] = &clone
	return &clone
}

// nextProxyURL picks the pool URL this request should dial, skipping proxies it
// must not use.
//
// Two things disqualify a proxy, and both are about one egress rather than the
// whole pool:
//
//   - a proxy parked by an earlier refusal (proxyCoolingDown), which outlives
//     the request, and
//   - a proxy this turn has already spent (RefusedProxies), which does not.
//
// The second is what makes a pool walkable. A pool is many egresses behind ONE
// connection, but the account fallback loop advances over connections, so a
// provider with one connection and a hundred proxies got exactly one attempt per
// request: the resolver picked one URL, the gate refused it, and the turn failed
// while ninety-nine egresses had never been tried.
//
// It returns "" when every proxy in the pool is spent for this turn. The caller
// answers that with the last upstream refusal rather than an internal error, so
// the operator sees what the gate actually said.
//
// Falling back to the pool's own rotation when nothing is disqualified keeps
// every existing caller unchanged: no cooldowns and no refusals means the old
// behaviour exactly. The same fallback covers the case where everything is
// cooling — the request still has to leave, and going direct would publish the
// host's real IP, which is the one thing a proxy assignment exists to prevent.
func (h *ChatHandler) nextProxyURL(connData *ConnectionData, pool *db.ProxyPool) string {
	return h.selectProxyURL(connData, pool, false)
}

// selectProxyURL is nextProxyURL with the walk override.
func (h *ChatHandler) selectProxyURL(connData *ConnectionData, pool *db.ProxyPool, refuseOnly bool) string {
	provider := connData.Provider
	refused := connData.RefusedProxies[connData.ProxyPoolID]

	// Nothing to skip: the plain rotation is exactly right and costs one call.
	if len(refused) == 0 && (refuseOnly || provider == "" || !hasProxyCooldownSupport(provider) || !poolHasCoolingProxy(provider, pool)) {
		return pool.NextURL()
	}

	now := time.Now()
	urls := pool.URLs

	// Pass 1: prefer a URL that is neither spent this turn nor cooling down.
	if !refuseOnly && provider != "" && hasProxyCooldownSupport(provider) {
		for i := 0; i <= len(urls); i++ {
			candidate := pool.NextURL()
			if refused[candidate] || proxyCoolingDown(provider, candidate, now) {
				continue
			}
			return candidate
		}
	}

	// Pass 2: if every remaining URL is cooling (or refuseOnly is true), still
	// return any unrefused URL rather than aborting with "".
	for i := 0; i <= len(urls); i++ {
		candidate := pool.NextURL()
		if !refused[candidate] {
			return candidate
		}
	}

	// Every URL in this pool was refused during this turn.
	if len(refused) > 0 {
		return ""
	}
	return pool.NextURL()
}

// poolHasUnrefusedURL reports whether pool holds at least one URL not yet
// refused during the current turn, without advancing pool's round-robin cursor.
func poolHasUnrefusedURL(connData *ConnectionData, pool *db.ProxyPool) bool {
	if pool == nil || !pool.IsActive || len(pool.URLs) == 0 {
		return false
	}
	if connData == nil {
		return true
	}
	refused := connData.RefusedProxies[connData.ProxyPoolID]
	if len(refused) == 0 {
		return true
	}
	for _, u := range pool.URLs {
		if !refused[u] {
			return true
		}
	}
	return false
}

// poolHasCoolingProxy reports whether any proxy in the pool is currently parked
// for provider. It lets the common case — no cooldowns at all — skip the
// scanning loop entirely, so a healthy pool costs exactly what it did before.
func poolHasCoolingProxy(provider string, pool *db.ProxyPool) bool {
	now := time.Now()
	for _, u := range pool.URLs {
		if proxyCoolingDown(provider, u, now) {
			return true
		}
	}
	return false
}

// refuseProxy records that the turn's current proxy answered with a free-tier
// refusal, so the next selection moves to a different egress.
//
// Only a free-tier refusal belongs here. The gate has said "not this egress",
// which is a fact about the proxy for this request. A dead socket or a 5xx is
// deliberately not recorded: it may be the upstream's fault, and skipping a
// healthy proxy over it would walk a good pool on someone else's bad afternoon.
//
// The map is the caller's, shared across the retry loop's iterations: connData
// is rebuilt per attempt, so a set allocated here would be thrown away by the
// retry that needs it.
func refuseProxy(connData *ConnectionData) {
	if connData == nil || connData.ResolvedProxyURL == "" || connData.ProxyPoolID == "" {
		return
	}
	if connData.RefusedProxies == nil {
		connData.RefusedProxies = make(map[string]map[string]bool)
	}
	set, ok := connData.RefusedProxies[connData.ProxyPoolID]
	if !ok {
		set = make(map[string]bool)
		connData.RefusedProxies[connData.ProxyPoolID] = set
	}
	set[connData.ResolvedProxyURL] = true
}
