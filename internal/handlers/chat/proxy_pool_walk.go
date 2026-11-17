package chat

import (
	"errors"

	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

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
	if first == nil || f.ConnData == nil || h.Repo == nil {
		return first
	}
	provider, model := f.Provider, f.Model
	var ue *upstreamError
	if !errors.As(first, &ue) || !isOpencodeFreeTierRefusal(provider, ue) {
		return first
	}

	connData := f.ConnData
	if connData.ProxyPoolID == "" {
		return first
	}
	pool, poolErr := h.Repo.GetProxyPool(connData.ProxyPoolID)
	if poolErr != nil || pool == nil || len(pool.URLs) == 0 {
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
			"conn", connObj.ID, "provider", provider, "model", model,
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
		if !errors.As(err, &next) || !isOpencodeFreeTierRefusal(provider, next) {
			// A different failure on a different proxy is that proxy's own
			// answer, and the existing machinery already knows what to do with
			// it. Hand it back rather than walking the rest of the pool.
			return err
		}
	}

	if walked > 0 {
		log.Warn("fallback", "opencode free tier refused every proxy it tried in the pool",
			"conn", connObj.ID, "provider", provider, "model", model,
			"pool", connData.ProxyPoolID, "tried", walked, "poolSize", len(pool.URLs))
	}
	return err
}
