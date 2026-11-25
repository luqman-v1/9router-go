package chat

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	internalproxy "9router/proxy/internal/proxy"
)

// proxyEgressProbeTimeout bounds the reachability dial on the failure path. It
// is short because the client is already waiting: a proxy that has not accepted
// a TCP connection in this long is not going to serve the retry either.
const proxyEgressProbeTimeout = 3 * time.Second

// proxyWalkBudget bounds the whole walk. A pool (or rotation across hundreds of
// imported pools) cannot hold one client request indefinitely: after this
// window the last upstream answer is returned.
const proxyWalkBudget = 180 * time.Second

// maxProxyWalkSteps caps total egress retries per request as a safety bound.
const maxProxyWalkSteps = 1024

// walkPoolOnFreeTierRefusal retries a refused OpenCode free-tier attempt (or a
// dead proxy socket) across both remaining URLs in the current pool AND other
// active pools when provider-level pool rotation is configured.
func (h *ChatHandler) walkPoolOnFreeTierRefusal(f forwardRequestParams, connObj *models.ProviderConnection, first error) error {
	return h.walkPoolOnFreeTierRefusalWithExcluded(f, connObj.ID, connObjName(connObj), connObjEmail(connObj), nil, first)
}

// walkPoolOnFreeTierRefusalFor is walkPoolOnFreeTierRefusal without a stored
// *models.ProviderConnection row, for the synthesized keyless no-auth lane
// (ConnectionID "default") and combo candidates.
func (h *ChatHandler) walkPoolOnFreeTierRefusalFor(f forwardRequestParams, connID, connName, connEmail string, first error) error {
	return h.walkPoolOnFreeTierRefusalWithExcluded(f, connID, connName, connEmail, nil, first)
}

func (h *ChatHandler) walkPoolOnFreeTierRefusalWithExcluded(
	f forwardRequestParams,
	connID, connName, connEmail string,
	excludedPools map[string]bool,
	first error,
) error {
	if first == nil || f.ConnData == nil || h.Repo == nil || writerCommitted(f.W) {
		return first
	}
	provider, model := f.Provider, f.Model
	connData := f.ConnData
	if connData.Provider == "" {
		connData.Provider = provider
	}
	if connData.ProxyPoolID == "" {
		return first
	}
	pool, poolErr := h.Repo.GetProxyPool(connData.ProxyPoolID)
	if poolErr != nil || pool == nil || !pool.IsActive || len(pool.URLs) == 0 {
		return first
	}

	if !h.parkAndClassifyWalkableError(f.Ctx, provider, connData, pool, first) {
		return first
	}

	if excludedPools == nil {
		excludedPools = make(map[string]bool)
	}

	// Read the settings row and memoise pool rows for the whole walk. A
	// bulk-imported pool is one row per proxy, so reading either per step is
	// 1+N statements over a pool that can hold hundreds of entries
	// (AGENTS.md §4.A). Both reads feed only selection, so one snapshot per
	// request is also the more consistent view.
	settings, _ := h.Repo.GetSettings()
	poolCache := make(walkPoolCache, 8)

	err := first
	walked := 0
	deadline := time.Now().Add(proxyWalkBudget)

	for walked < maxProxyWalkSteps {
		if time.Now().After(deadline) {
			log.Warn("fallback", "proxy walk budget exhausted, reporting last refusal",
				"conn", connID, "provider", provider, "model", model,
				"pool", connData.ProxyPoolID, "tried", walked)
			break
		}
		if f.Ctx != nil && f.Ctx.Err() != nil {
			break
		}
		if writerCommitted(f.W) {
			break
		}

		prevPoolID := connData.ProxyPoolID
		prevProxy := redactProxyURL(connData.ResolvedProxyURL)

		nextPool, ok := h.advanceWalkTarget(provider, connData, pool, excludedPools, settings, poolCache)
		if !ok {
			break
		}
		pool = nextPool

		walked++
		log.Warn("fallback", "opencode free tier refused this proxy, trying the next",
			"conn", connID, "provider", provider, "model", model,
			"pool", prevPoolID, "proxy", prevProxy,
			"nextPool", connData.ProxyPoolID, "step", walked)

		retry := f
		retry.ConnName = connName
		retry.ConnEmail = connEmail
		retry.Attempts = attemptCount(f.Ctx)
		err = h.tryForwardWithConnection(retry)
		if err == nil {
			return nil
		}
		if !h.parkAndClassifyWalkableError(f.Ctx, provider, connData, pool, err) {
			return err
		}
	}

	if walked > 0 {
		log.Warn("fallback", "opencode free tier refused every proxy it tried in the pool",
			"conn", connID, "provider", provider, "model", model,
			"pool", connData.ProxyPoolID, "tried", walked)
	}
	return err
}

// walkPoolCache memoises the proxy-pool rows one walk reads, so the number of
// statements per request follows the pool count rather than the attempt count.
// A pool binding is stable within a request; an empty entry means "read, and
// there was nothing usable", which the walk treats the same as a miss.
type walkPoolCache map[string]*db.ProxyPool

func (c walkPoolCache) get(h *ChatHandler, poolID string) (*db.ProxyPool, error) {
	if pool, ok := c[poolID]; ok {
		return pool, nil
	}
	pool, err := h.Repo.GetProxyPool(poolID)
	if err != nil {
		return nil, err
	}
	c[poolID] = pool
	return pool, nil
}

// advanceWalkTarget records the just-failed proxy URL and selects the next
// egress: first any remaining unrefused URL in the current pool, and once the
// current pool is exhausted (immediately true for single-URL "Imported ..."
// pools), the next active pool from provider-level pool rotation.
func (h *ChatHandler) advanceWalkTarget(
	provider string,
	connData *ConnectionData,
	pool *db.ProxyPool,
	excludedPools map[string]bool,
	settings *db.SettingsData,
	poolCache walkPoolCache,
) (*db.ProxyPool, bool) {
	refuseProxy(connData)

	if pool != nil && poolHasUnrefusedURL(connData, pool) {
		connData.ResolvedProxyURL = ""
		return pool, true
	}

	if connData.ProxyPoolID != "" {
		excludedPools[connData.ProxyPoolID] = true
	}

	for {
		nextPoolID, rotErr := h.resolveProviderProxyPoolIDFrom(provider, excludedPools, settings)
		if rotErr != nil || nextPoolID == "" || excludedPools[nextPoolID] {
			return nil, false
		}
		nextPool, pErr := poolCache.get(h, nextPoolID)
		if pErr != nil || nextPool == nil || !nextPool.IsActive || len(nextPool.URLs) == 0 {
			excludedPools[nextPoolID] = true
			continue
		}
		probe := &ConnectionData{
			ProxyPoolID:    nextPoolID,
			RefusedProxies: connData.RefusedProxies,
		}
		if !poolHasUnrefusedURL(probe, nextPool) {
			excludedPools[nextPoolID] = true
			continue
		}
		connData.ProxyPoolID = nextPoolID
		connData.ResolvedProxyPool = ""
		connData.ResolvedProxyURL = ""
		return nextPool, true
	}
}

// parkAndClassifyWalkableError parks the failed proxy in per-provider cooldown
// when appropriate and reports whether the failure belongs to the egress (so
// walking to the next proxy/pool can recover it).
func (h *ChatHandler) parkAndClassifyWalkableError(
	ctx context.Context,
	provider string,
	connData *ConnectionData,
	pool *db.ProxyPool,
	err error,
) bool {
	if err == nil {
		return false
	}
	var ue *upstreamError
	if errors.As(err, &ue) {
		if isOpencodeFreeTierRefusal(provider, ue) {
			parkRefusedProxyWithPool(provider, connData, pool, ue)
			return true
		}
		return false
	}

	if isClientCanceled(ctx, err) || connData == nil || connData.ResolvedProxyURL == "" {
		return false
	}
	if isProxyTransportError(err) || h.proxyEgressIsDead(ctx, connData) {
		parkDeadProxyWithPool(provider, connData, pool)
		return true
	}
	return false
}

// isProxyTransportError recognises dial/tunnel failures on a proxied attempt
// immediately without spending an extra TCP probe dial.
func isProxyTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, internalproxy.ErrStrictProxyRequired) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}

	// Typed verdicts first: a transport failure is a property the syscall
	// reports, and matching it by text breaks whenever Go or a library rewords
	// a message (AGENTS.md §4.E). These cover refused, reset, unreachable,
	// timed out and closed-socket, which is the whole list the string table
	// used to guess at.
	if isTypedTransportFailure(err) {
		return true
	}

	// A narrow text fallback survives for one reason: the SOCKS5 dialer and
	// net/http's proxy CONNECT path report their failures as formatted strings
	// with no exported sentinel or type to match, so these three have no typed
	// equivalent to ask for. Anything added here must meet that same bar.
	msg := strings.ToLower(err.Error())
	for _, token := range []string{
		"socks ",
		"socks5",
		"proxyconnect",
		"connect tunnel failed",
		"proxy authentication required",
	} {
		if strings.Contains(msg, token) {
			return true
		}
	}
	return false
}

// isTypedTransportFailure reports whether err is a connection-level failure the
// standard library names with a sentinel or a syscall error.
func isTypedTransportFailure(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func writerCommitted(w http.ResponseWriter) bool {
	type committer interface{ IsCommitted() bool }
	if cw, ok := w.(committer); ok {
		return cw.IsCommitted()
	}
	return false
}

// proxyEgressIsDead reports whether the proxy this request just used cannot be
// dialled at all.
func (h *ChatHandler) proxyEgressIsDead(ctx context.Context, connData *ConnectionData) bool {
	if connData == nil || connData.ResolvedProxyURL == "" {
		return false
	}
	parsed, err := url.Parse(connData.ResolvedProxyURL)
	if err != nil || parsed.Host == "" {
		return false
	}

	host := parsed.Host
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		host = net.JoinHostPort(host, "1080")
	}

	// Dial on the caller's context, not context.Background(): a client that has
	// already gone away must not have this request spend its probe timeout
	// dialling a socket nobody is waiting for, and the walk's own budget is
	// carried on that context.
	if ctx == nil {
		ctx = context.Background()
	}
	dialer := &net.Dialer{Timeout: proxyEgressProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		log.Warn("fallback", "proxy egress is unreachable, walking the pool",
			"proxy", redactProxyURL(connData.ResolvedProxyURL),
			"pool", connData.ProxyPoolID, "error", err)
		return true
	}
	_ = conn.Close()
	return false
}
