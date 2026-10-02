package chat

import (
	"strings"

	"9router/proxy/internal/providers"
)

// Egress describes the path a request actually left the host by, in a form a log
// line can carry.
//
// "Did this request use the proxy?" was unanswerable from the logs. The only
// proxy line was emitted once per pool at debug level, so a second request
// through the same pool said nothing at all, and the success path carried no
// proxy field either. Two gateways sharing one database then look identical even
// when one silently goes direct — the exact failure the egress fix closed.
type Egress struct {
	// Kind is "direct", "http", or "relay".
	Kind string
	// PoolID is the assigned pool, empty for a direct request or the legacy
	// per-connection proxy.
	PoolID string
	// Target is the relay deployment or proxy URL the request was sent through.
	Target string
}

// LogValue returns the short form for a log line: the pool when there is one,
// else the target, else "direct". It lands on every usage row, so it stays short.
func (e Egress) LogValue() string {
	switch {
	case e.PoolID != "":
		return e.PoolID
	case e.Target != "":
		return e.Target
	default:
		return "direct"
	}
}

// resolveEgress reports how a request will leave the host, from data already
// resolved for it — no extra pool lookup on the hot path.
//
// The relay shape is read off the provider config because that is where an edge
// pool leaves its mark: the upstream in x-relay-target, the relay host in
// BaseURL. A plain HTTP proxy is invisible there, so it is resolved from the
// connection's own proxy fields instead.
func resolveEgress(connData *ConnectionData, cfg *providers.ProviderConfig) Egress {
	poolID := assignedPoolID(connData)

	if cfg != nil {
		if target := cfg.StaticHeaders["x-relay-target"]; target != "" {
			return Egress{Kind: "relay", PoolID: poolID, Target: cfg.BaseURL}
		}
	}
	if connData == nil {
		return Egress{Kind: "direct"}
	}
	if enabled, proxyURL := legacyProxyFields(connData); enabled {
		return Egress{Kind: "http", PoolID: poolID, Target: proxyURL}
	}
	return Egress{Kind: "direct"}
}

// assignedPoolID is the pool a request is attributed to. It comes from the
// connection rather than from Egress so a relay that failed to resolve is still
// attributable to the pool the operator assigned.
func assignedPoolID(connData *ConnectionData) string {
	if connData == nil {
		return ""
	}
	return connData.ProxyPoolID
}

// legacyProxyFields reads the pre-pool per-connection proxy, which predates
// proxyPools and is still written by the dashboard. A pool assignment outranks
// it, and the caller only reaches this when no pool is set.
func legacyProxyFields(connData *ConnectionData) (enabled bool, proxyURL string) {
	enabled = connData.ConnectionProxyEnabled
	proxyURL = connData.ConnectionProxyURL
	if !enabled && connData.ProviderSpecificData != nil {
		if en, ok := connData.ProviderSpecificData["connectionProxyEnabled"].(bool); ok {
			enabled = en
		}
		if u, ok := connData.ProviderSpecificData["connectionProxyUrl"].(string); ok {
			proxyURL = u
		}
	}
	proxyURL = strings.TrimSpace(proxyURL)
	return enabled && proxyURL != "", proxyURL
}
