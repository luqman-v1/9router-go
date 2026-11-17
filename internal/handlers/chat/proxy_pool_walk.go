package chat

import (
	"context"
	"errors"
	"net"
	"net/url"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

// proxyEgressProbeTimeout bounds the reachability dial on the failure path. It
// is short because the client is already waiting: a proxy that has not accepted
// a TCP connection in this long is not going to serve the retry either.
const proxyEgressProbeTimeout = 3 * time.Second

// walkPoolOnFreeTierRefusal retries a refused OpenCode free-tier attempt against
// the next proxy in the same pool, and returns the answer the caller should act
// on.
//
// Why this exists: a proxy pool is many egresses behind ONE connection, but the
// account loop advances over connections. A provider with one connection and a
// hundred proxies therefore got exactly one attempt per request — the log showed
// a dead socket or two and a 429, then "free tier refused after retries", while
// ninety-six egresses had never been tried. The operator's expectation is the
// obvious one: walk the pool until one answers.
//
// Only a free-tier refusal triggers the walk. That refusal is the gate saying
// "not this egress", which is a fact about the proxy for this request. Every
// other failure is left to the existing machinery: a dead socket may be the
// upstream's fault, and a 5xx says nothing about the egress at all.
//
// The walk is bounded by the pool size. Each pass re-enters the forward, so the
// resolver's rotation hands back a different proxy and the refused ones are
// skipped; when every URL has been spent the walk stops and the last refusal is
// returned, so the caller's own handling — pool exclusion, then direct — is
// unchanged.
func (h *ChatHandler) walkPoolOnFreeTierRefusal(f forwardRequestParams, connObj *models.ProviderConnection, first error) error {
	return h.walkPoolOnFreeTierRefusalFor(f, connObj.ID, connObjName(connObj), connObjEmail(connObj), first)
}

// walkPoolOnFreeTierRefusalFor is the walk without a *models.ProviderConnection,
// for the synthesized no-auth lane (ConnectionID "default"), which has no stored
// row to pass. That lane is what the keyless OpenCode free tier uses, so leaving
// it out of the walk meant the operator's pool was walked on the stored-
// connection path and not on the one they were actually calling.
func (h *ChatHandler) walkPoolOnFreeTierRefusalFor(f forwardRequestParams, connID, connName, connEmail string, first error) error {
	if first == nil || f.ConnData == nil || h.Repo == nil {
		return first
	}
	provider, model := f.Provider, f.Model

	connData := f.ConnData
	if connData.ProxyPoolID == "" {
		return first
	}
	pool, poolErr := h.Repo.GetProxyPool(connData.ProxyPoolID)
	if poolErr != nil || pool == nil || len(pool.URLs) == 0 {
		return first
	}

	// Two failures belong to the egress and are worth walking past.
	//
	// The first is the gate saying "not this egress" with a classified body.
	// The second has no body at all: the proxy socket itself refused the
	// connection, so the request never reached opencode and the error is a bare
	// transport failure. That one used to fall straight through to a 502 —
	// three dead sockets in the operator's pool produced three instant 502s
	// with the rest of the pool untried.
	//
	// A transport failure is only treated as egress-owned after the failing
	// proxy itself is proven dead, which is what keeps a genuine upstream outage
	// from walking the whole pool.
	var ue *upstreamError
	classified := errors.As(first, &ue) && isOpencodeFreeTierRefusal(provider, ue)
	if !classified && !h.proxyEgressIsDead(connData) {
		return first
	}

	err := first
	walked := 0
	for walked < len(pool.URLs) {
		// Record the proxy that just refused, then ask the pool for one it has
		// not spent. "" means every URL is now spent.
		refuseProxy(connData)
		if nextProxyURL(connData, pool) == "" {
			break
		}
		if f.Ctx.Err() != nil {
			// The client is gone. Reporting the upstream refusal is more useful
			// than a cancellation that only says we stopped trying.
			break
		}

		walked++
		log.Warn("fallback", "opencode free tier refused this proxy, trying the next",
			"conn", connID, "provider", provider, "model", model,
			"pool", connData.ProxyPoolID,
			"proxy", redactProxyURL(connData.ResolvedProxyURL),
			"refused", len(connData.RefusedProxies[connData.ProxyPoolID]), "of", len(pool.URLs))

		retry := f
		retry.Attempts = attemptCount(f.Ctx)
		err = h.tryForwardWithConnection(retry)
		if err == nil {
			return nil
		}
		var next *upstreamError
		nextClassified := errors.As(err, &next) && isOpencodeFreeTierRefusal(provider, next)
		// Same two cases as the entry test. A dead socket on the NEXT proxy ends
		// the walk only if that proxy is itself proven dead — otherwise the
		// failure is the upstream's and walking on would just burn the pool.
		if !nextClassified && !h.proxyEgressIsDead(connData) {
			return err
		}
	}

	if walked > 0 {
		log.Warn("fallback", "opencode free tier refused every proxy it tried in the pool",
			"conn", connID, "provider", provider, "model", model,
			"pool", connData.ProxyPoolID, "tried", walked, "poolSize", len(pool.URLs))
	}
	return err
}

// proxyEgressIsDead reports whether the proxy this request just used cannot be
// dialled at all.
//
// It exists to tell two identical-looking transport failures apart. The log
// line is the same for both — "Post ...: <error>" with status 0 — but one means
// the egress is gone and the pool has better options, while the other means the
// upstream is having a bad afternoon and no amount of rotation will help.
//
// The test is whether a TCP connection can be opened to the proxy's own
// listen address. That is a fact about the proxy, not about opencode: a socket
// that refuses the proxy dial would refuse any target. It costs one dial with a
// short deadline, and only on the failure path, so the happy path never pays
// for it (AGENTS.md §4.G: nothing new per successful request).
//
// A proxy that accepts the connection but fails later (TLS, CONNECT rejection,
// the gate's own 5xx) is deliberately NOT reported dead: those are the
// upstream's answers travelling through a working egress.
func (h *ChatHandler) proxyEgressIsDead(connData *ConnectionData) bool {
	if connData == nil || connData.ResolvedProxyURL == "" {
		return false
	}
	parsed, err := url.Parse(connData.ResolvedProxyURL)
	if err != nil || parsed.Host == "" {
		return false
	}

	host := parsed.Host
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		// Default the SOCKS port so a URL written without one still probes the
		// address the transport would dial.
		host = net.JoinHostPort(host, "1080")
	}

	dialer := &net.Dialer{Timeout: proxyEgressProbeTimeout}
	conn, err := dialer.DialContext(context.Background(), "tcp", host)
	if err != nil {
		log.Warn("fallback", "proxy egress is unreachable, walking the pool",
			"proxy", redactProxyURL(connData.ResolvedProxyURL),
			"pool", connData.ProxyPoolID, "error", err)
		return true
	}
	_ = conn.Close()
	return false
}
