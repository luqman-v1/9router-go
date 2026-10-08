package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"time"

	"9router/proxy/internal/fetchgate"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// quotaFetchGate paces every live quota read this handler makes (issue #30).
//
// The quota tracker refreshes every visible connection in one tick, so a user
// with ten accounts behind one office IP fired ten quota reads within a few
// milliseconds. Google answered 429, and the chat path reads a 429 as real
// quota exhaustion — locking accounts whose tokens were still live, and taking
// the paid combos down with them. Upstream decolua/9router has no throttle here
// either, so this is a deliberate gap and not a parity regression.
//
// The floor is 250ms with up to 120ms of jitter on top: enough that a burst
// stops looking like a fleet sharing one egress IP, cheap enough that ten
// accounts still refresh inside a couple of seconds. Only the start of a
// request is paced, so a single account's manual refresh is never delayed.
var quotaFetchGate = fetchgate.New(250*time.Millisecond, 120*time.Millisecond)

// acquireQuotaSlot blocks until this request may talk to the provider. It
// returns false when the client gave up while queued — usually a dashboard
// that navigated away mid-refresh — in which case the caller abandons the
// fetch instead of spending an upstream request on a response nobody reads.
func acquireQuotaSlot(w http.ResponseWriter, r *http.Request) bool {
	if err := quotaFetchGate.Acquire(r.Context()); err != nil {
		if r.Context().Err() == nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		}
		return false
	}
	return true
}

// HandleGetConnectionUsage handles GET /api/usage/{connectionId}
//
// Upstream parity (src/app/api/usage/[connectionId]/route.js): an OAuth
// connection inside its expiry window is refreshed before the read, and a
// provider that reports an auth failure as a message rather than a status is
// force-refreshed and read once more. Both halves are what keep a stale token
// from surfacing as "quota API rejected the current token. Chat may still
// work." while chat traffic — refreshing on its own path — keeps working
// (#78 item 4).
func (h *DashboardHandler) HandleGetConnectionUsage(w http.ResponseWriter, r *http.Request) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		connID = getURLParam(r, "id")
	}
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}

	// Live provider quota fetchers (ports of open-sse/services/usage/*.js).
	// Antigravity keeps its existing dedicated path below.
	if !acquireQuotaSlot(w, r) {
		return
	}

	creds := usageCredentials{raw: data, provider: conn.Provider}
	// A pre-read refresh only makes sense when the token is inside its expiry
	// window. When it is not, the single retry below is the one attempt: a
	// second call would repeat a request whose outcome is already known.
	preRead := usageRefreshOutcome{}
	if creds.needsRefresh() {
		preRead = h.runUsageRefresh(r.Context(), conn, creds)
		if preRead.err != nil {
			log.Warn("usage", "credential refresh failed", "provider", conn.Provider, "conn", connID, "error", preRead.err)
		}
	}

	res, ok := fetchProviderUsage(r.Context(), conn.Provider, data)
	// Upstream force-refreshes and retries exactly once here
	// (src/app/api/usage/[connectionId]/route.js): a stored access token can age
	// out while the refresh token is still good. That retry is only worth a call
	// when this request has not already had one and it failed — repeating a
	// rejected exchange just spent the call twice and logged the same failure
	// twice, which is what the double warning per account came from.
	if ok && !preRead.attempted && isAuthExpiredUsageMessage(res) {
		retry := h.runUsageRefresh(r.Context(), conn, creds)
		if retry.err != nil {
			log.Warn("usage", "forced credential refresh failed", "provider", conn.Provider, "conn", connID, "error", retry.err)
		} else if retry.refreshed {
			if retried, retryOK := fetchProviderUsage(r.Context(), conn.Provider, data); retryOK {
				handlerutil.WriteJSON(w, http.StatusOK, retried.toResponse())
				return
			}
		}
	}
	if ok {
		handlerutil.WriteJSON(w, http.StatusOK, res.toResponse())
		return
	}

	// Antigravity quota resolution (dashboard presentation with tier check +
	// weekly overlay — parity with open-sse/services/usage/google.js).
	if conn.Provider == "antigravity" && data != nil {
		accessToken, _ := data["accessToken"].(string)
		projectID, _ := data["projectId"].(string)
		if accessToken != "" {
			// The slot above is already held; taking a second one made every
			// Antigravity account queue for two consecutive 250ms gaps even
			// though it issues one burst of upstream reads.

			// The gate is paced on the request context, not this deadline, so a
			// long queue cannot expire a fetch that has not started yet; the
			// 30s budget covers the upstream call itself.
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()

			res := fetchAntigravityDashboardUsage(ctx, accessToken, projectID)
			handlerutil.WriteJSON(w, http.StatusOK, res.toResponse())
			return
		}
	}

	// Fallback for connections with rate limit or locks in data
	respQuotas := make(map[string]any)
	if data != nil {
		if rateLimitedUntil, ok := data["rateLimitedUntil"].(string); ok && rateLimitedUntil != "" {
			respQuotas["default"] = map[string]any{
				"remainingPercentage": 0,
				"resetAt":             rateLimitedUntil,
				"displayName":         conn.Provider,
			}
		}
		for k, v := range data {
			if len(k) > 10 && k[:10] == "modelLock_" {
				model := k[10:]
				if lockStr, ok := v.(string); ok && lockStr != "" {
					respQuotas[model] = map[string]any{
						"remainingPercentage": 0,
						"resetAt":             lockStr,
						"displayName":         model,
					}
				}
			}
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"plan":   conn.Provider,
		"quotas": respQuotas,
	})
}

// HandleGetUsageProviders handles GET /api/usage/providers
func (h *DashboardHandler) HandleGetUsageProviders(w http.ResponseWriter, r *http.Request) {
	conns, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	seen := make(map[string]bool)
	type ProviderItem struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var list []ProviderItem
	for _, c := range conns {
		if c.Provider != "" && !seen[c.Provider] {
			seen[c.Provider] = true
			list = append(list, ProviderItem{
				ID:   c.Provider,
				Name: c.Provider,
			})
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"providers": list,
	})
}
