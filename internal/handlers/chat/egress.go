package chat

import (
	"strings"

	"9router/proxy/internal/providers"
)

// Egress describes the path a request actually left the host by, in a form a log
// line can carry.
//
// "Did this request use the proxy?" had no answer in the logs. The only proxy
// line was logProxyOnce, which uses sync.Map.LoadOrStore — once per pool per
// process — and only at debug level, while LOG_LEVEL defaults to info. A second
// request through the same pool printed nothing, and the [usage] rows carried no
// proxy field at all.
type Egress struct {
	// Kind is "direct", "http", or "relay".
	Kind string
	// PoolID is the assigned pool, empty for a direct request or the legacy
	// per-connection proxy.
	PoolID string
	// PoolName is the operator's name for that pool. Preferred in a log line:
	// "Vercel Relay" answers the question a bare UUID leaves open, and the
	// dashboard lists pools by name.
	PoolName string
	// Target is the relay deployment or proxy URL the request was sent through.
	Target string
}

// LogValue returns the short form for a log line: the pool's name when it has
// one, else its id, else the target, else "direct". It lands on every usage row,
// so it stays short.
func (e Egress) LogValue() string {
	switch {
	case e.PoolName != "":
		return logLabel(e.PoolName)
	case e.PoolID != "":
		return e.PoolID
	case e.Target != "":
		return logLabel(e.Target)
	default:
		return "direct"
	}
}

// logLabel collapses a pool name or proxy URL to a single line. The value is
// operator-supplied and lands in every usage log, so a newline inside a pool
// name would otherwise forge extra log entries — the log is the audit trail for
// "which egress served this request".
func logLabel(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
}

// resolveEgress reports how a request will leave the host, from data already
// resolved for it — no extra pool lookup on the hot path.
//
// The relay shape is read off the provider config because that is where an edge
// pool leaves its mark: the upstream in x-relay-target, the relay host in
// BaseURL. A plain HTTP proxy is invisible there, so it is resolved from the
// connection's own proxy fields instead.
//
// The pool name comes from ConnectionData.ResolvedProxyPool, filled in by the
// client resolver while it was already reading the pool for the transport.
func resolveEgress(connData *ConnectionData, cfg *providers.ProviderConfig) Egress {
	poolID, poolName := assignedPool(connData)

	if cfg != nil {
		if target := cfg.StaticHeaders["x-relay-target"]; target != "" {
			return Egress{Kind: "relay", PoolID: poolID, PoolName: poolName, Target: cfg.BaseURL}
		}
	}
	if connData == nil {
		return Egress{Kind: "direct"}
	}
	if enabled, proxyURL := legacyProxyFields(connData); enabled {
		return Egress{Kind: "http", PoolID: poolID, PoolName: poolName, Target: proxyURL}
	}
	return Egress{Kind: "direct"}
}

// assignedPool is the pool a request is attributed to, with the name the
// resolver picked up. Both come from the connection rather than from Egress so a
// relay that failed to resolve is still attributable to the assigned pool.
func assignedPool(connData *ConnectionData) (poolID, poolName string) {
	if connData == nil {
		return "", ""
	}
	return connData.ProxyPoolID, connData.ResolvedProxyPool
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
