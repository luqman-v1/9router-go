package chat

import (
	"fmt"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
	"net/http"
	"net/url"
	"sync"
)

// maxProxyClients caps rotating-proxy growth: each entry pins a Transport
// plus idle sockets, so unbounded distinct URLs (residential rotation)
// would leak descriptors without eviction.
const maxProxyClients = 128

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
	var strictProxy bool

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
		proxyURLStr = pool.NextURL()
		if proxyURLStr == "" {
			log.Warn("proxy", "assigned pool has no url", "pool", connData.ProxyPoolID)
			return nil, fmt.Errorf("proxy pool %q is assigned but carries no url", connData.ProxyPoolID)
		}
		proxyType = pool.Type
		strictProxy = pool.StrictProxy
		// Name the pool for the log line. The lookup already happened, so this
		// costs nothing, and "Vercel Relay" answers a question a UUID cannot.
		connData.ResolvedProxyPool = pool.Name
	}

	// 2. Fallback to legacy connection proxy
	if proxyURLStr == "" {
		proxyEnabled := connData.ConnectionProxyEnabled
		proxyURL := connData.ConnectionProxyURL
		if !proxyEnabled && connData.ProviderSpecificData != nil {
			if en, ok := connData.ProviderSpecificData["connectionProxyEnabled"].(bool); ok {
				proxyEnabled = en
			}
			if u, ok := connData.ProviderSpecificData["connectionProxyUrl"].(string); ok {
				proxyURL = u
			}
			if sp, ok := connData.ProviderSpecificData["strictProxy"].(bool); ok {
				strictProxy = sp
			}
		}
		if proxyEnabled && proxyURL != "" {
			proxyURLStr = proxyURL
			proxyType = "http"
		}
	}

	if proxyURLStr == "" {
		return h.Client, nil
	}
	logProxyOnce(connData.ProxyPoolID, proxyURLStr, proxyType)

	parsedURL, err := url.Parse(proxyURLStr)
	if err != nil {
		log.Warn("proxy", "invalid proxy pool url", "pool", connData.ProxyPoolID, "url", proxyURLStr, "error", err)
		if connData.ProxyPoolID != "" {
			// The connection is bound to this pool, so a URL the transport
			// cannot parse is a broken assignment, not a missing one.
			return nil, fmt.Errorf("proxy pool %q has an unparseable url", connData.ProxyPoolID)
		}
		if strictProxy {
			log.Error("proxy", "strict proxy enabled but proxy url invalid", "url", proxyURLStr)
		}
		return h.Client, nil
	}

	if proxyType == "http" || proxyType == "" {
		proxyClientsMu.RLock()
		client, ok := proxyClients[proxyURLStr]
		proxyClientsMu.RUnlock()
		if ok {
			return client, nil
		}

		proxyClientsMu.Lock()
		defer proxyClientsMu.Unlock()
		if client, ok = proxyClients[proxyURLStr]; ok {
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

		var baseTransport *http.Transport
		if origT, ok := http.DefaultTransport.(*http.Transport); ok {
			baseTransport = origT.Clone()
		} else {
			baseTransport = constants.DefaultHTTPTransportConfig.NewTransport()
		}
		constants.DefaultHTTPTransportConfig.Configure(baseTransport)
		baseTransport.Proxy = http.ProxyURL(parsedURL)
		client = &http.Client{
			Transport: baseTransport,
			Timeout:   h.Client.Timeout,
		}
		proxyClients[proxyURLStr] = client
		return client, nil
	}

	// For Edge Relays (vercel, cloudflare, deno), standard client is used because
	// URL rewriting and x-relay headers are handled at request time.
	return h.Client, nil
}
