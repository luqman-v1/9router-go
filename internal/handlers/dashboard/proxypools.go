package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// countProxyPoolBindings counts, per pool, what still points at it. A pool is
// in use by a connection that pins it, by a provider strategy that pins it, or
// by any provider rotating across the active pools — the last case covers every
// pool rotation would pick, so none of them can be deleted underneath it.
func (h *DashboardHandler) countProxyPoolBindings() map[string]int {
	boundCounts := make(map[string]int)
	conns, _ := h.Repo.GetProviderConnections("", false)
	for _, c := range conns {
		if c == nil {
			continue
		}
		var dataMap map[string]any
		if c.Data != "" {
			_ = json.Unmarshal([]byte(c.Data), &dataMap)
		}
		poolID := handlerutil.GetString(dataMap, "proxyPoolId")
		if poolID == "" {
			if psd, ok := dataMap["providerSpecificData"].(map[string]any); ok {
				poolID = handlerutil.GetString(psd, "proxyPoolId")
			}
		}
		if poolID != "" {
			boundCounts[poolID]++
		}
	}
	if h.Repo == nil {
		return boundCounts
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return boundCounts
	}
	rotating := false
	for provider, strat := range settings.ProviderStrategies {
		// Same gate the chat path applies: only a NoAuth provider's
		// `rotateStrategy` means pool rotation, so counting rotation usage for
		// a keyed provider would refuse deletion of pools nobody rotates over.
		if db.IsProxyPoolRotation(strat.ProxyRotateStrategy) && providers.IsNoAuthProvider(provider) {
			rotating = true
		}
		if strat.ProxyPoolID != "" && strat.ProxyPoolID != "__none__" {
			boundCounts[strat.ProxyPoolID]++
		}
	}
	if rotating {
		// Rotation draws from every active pool, so each one counts as in use —
		// but only when nothing else already counts it. A pinned pool must not
		// read as two bindings, and a provider rotating over the pool it also
		// pins is still one binding.
		for _, id := range h.Repo.ActivePoolIDs() {
			if boundCounts[id] == 0 {
				boundCounts[id] = 1
			}
		}
	}
	return boundCounts
}

// isPoolRotationStrategy mirrors the accepted pool-rotation values in the chat
// package. Kept as a literal list rather than shared so the dashboard does not
// import the handler it serves.
func isPoolRotationStrategy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "round-robin", "roundrobin", "random":
		return true
	}
	return false
}

// HandleGetProxyPools handles GET /api/proxy-pools.
func (h *DashboardHandler) HandleGetProxyPools(w http.ResponseWriter, r *http.Request) {
	includeUsage := r.URL.Query().Get("includeUsage") == "true"
	pools, err := h.Repo.ListProxyPools()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if includeUsage {
		boundCounts := h.countProxyPoolBindings()

		for _, p := range pools {
			if id, ok := p["id"].(string); ok {
				p["boundConnectionCount"] = boundCounts[id]
			}
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"proxyPools": pools,
	})
}

// HandleCreateProxyPool handles POST /api/proxy-pools.
func (h *DashboardHandler) HandleCreateProxyPool(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Name        string   `json:"name"`
		ProxyURL    string   `json:"proxyUrl"`
		URLs        []string `json:"urls"`
		Type        string   `json:"type"`
		NoProxy     string   `json:"noProxy"`
		StrictProxy bool     `json:"strictProxy"`
		IsActive    bool     `json:"isActive"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Name == "" {
		req.Name = "Proxy Pool"
	}
	if req.Type == "" {
		if strings.HasPrefix(req.ProxyURL, "socks") {
			req.Type = "socks5"
		} else {
			req.Type = "http"
		}
	}

	poolData := db.ProxyPoolData{
		Name:        req.Name,
		ProxyURL:    req.ProxyURL,
		NoProxy:     req.NoProxy,
		Type:        req.Type,
		StrictProxy: req.StrictProxy,
	}

	created, err := h.Repo.InsertProxyPool(poolData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, created)
}

// HandleUpdateProxyPool handles PUT /api/proxy-pools/{id}.
func (h *DashboardHandler) HandleUpdateProxyPool(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing proxy pool id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var updates map[string]any
	if err := json.Unmarshal(body, &updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.Repo.UpdateProxyPool(id, updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleDeleteProxyPool handles DELETE /api/proxy-pools/{id}.
// Upstream parity: refuse with 409 while connections are still bound to the pool.
func (h *DashboardHandler) HandleDeleteProxyPool(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing proxy pool id")
		return
	}

	if bound := h.countProxyPoolBindings()[id]; bound > 0 {
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":                "Proxy pool is currently in use",
			"boundConnectionCount": bound,
		})
		return
	}

	if err := h.Repo.DeleteProxyPool(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleTestProxyPool handles POST /api/proxy-pools/{id}/test.
func (h *DashboardHandler) HandleTestProxyPool(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing proxy pool id")
		return
	}

	pool, err := h.Repo.GetProxyPool(id)
	if err != nil || pool == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "proxy pool not found")
		return
	}

	targetURL := pool.NextURL()
	if targetURL == "" {
		h.writeProbeResult(w, id, false, 0, "no proxy URLs configured")
		return
	}

	// Upstream parity: relay-type pools (vercel/cloudflare/deno) are not HTTP
	// proxies but plain HTTPS endpoints that forward based on x-relay-*
	// headers. Dialing them via http.ProxyURL breaks with
	// "malformed HTTP status code", so they get the upstream relay test.
	if pool.IsEdgeRelay() {
		h.testRelayPool(w, r, id, targetURL)
		return
	}

	tr, probeErr := poolProbeTransport(targetURL)
	if probeErr != "" {
		h.writeProbeResult(w, id, false, 0, probeErr)
		return
	}

	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
	}

	// Dual probe: some networks block Google, which would report a working
	// proxy as dead. Fall back to a Cloudflare endpoint before giving up. The
	// reported latency always belongs to the attempt that decided the
	// outcome, so a slow primary failure never inflates a fast fallback.
	ok, latencyMs, primaryErr := probeViaProxy(r.Context(), client, h.primaryProbeURL())
	if ok {
		h.writeProbeResult(w, id, true, latencyMs, "")
		return
	}

	ok, latencyMs, secondaryErr := probeViaProxy(r.Context(), client, h.secondaryProbeURL())
	if ok {
		h.writeProbeResult(w, id, true, latencyMs, "")
		return
	}

	h.writeProbeResult(w, id, false, 0, probeFailureMessage(primaryErr, secondaryErr))
}

// probeViaProxy runs one GET through the proxy and reports the round-trip
// latency of that single attempt.
func probeViaProxy(ctx context.Context, client *http.Client, probeURL string) (ok bool, latencyMs int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false, 0, err
	}

	start := time.Now()
	resp, err := client.Do(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return false, 0, err
	}
	if resp.StatusCode >= 400 {
		return false, 0, fmt.Errorf("probe returned HTTP %d", resp.StatusCode)
	}
	return true, time.Since(start).Milliseconds(), nil
}

// probeFailureMessage prefers the fallback error, then the primary one, so a
// single reachable failure still explains itself.
func probeFailureMessage(primary, secondary error) string {
	if secondary != nil {
		return secondary.Error()
	}
	if primary != nil {
		return primary.Error()
	}
	return ""
}

// writeProbeResult persists the terminal test state of a pool and mirrors it
// into the response, so the dashboard can repaint one row without a refetch.
// A failed pool stores latency 0: an unmeasured value must not be mistaken for
// a measured fast one.
func (h *DashboardHandler) writeProbeResult(w http.ResponseWriter, id string, ok bool, latencyMs int64, errStr string) {
	status := "passed"
	if !ok {
		status = "failed"
		latencyMs = 0
		if errStr == "" {
			errStr = "connection timed out or failed"
		}
	}
	_ = h.Repo.SetProxyPoolStatus(id, status, latencyMs)

	body := map[string]any{"success": ok, "status": status, "latency": latencyMs}
	if !ok {
		body["error"] = errStr
	}
	handlerutil.WriteJSON(w, http.StatusOK, body)
}

// primaryProbeURL and secondaryProbeURL fall back to the package defaults so a
// handler built as a bare struct literal still probes real endpoints.
func (h *DashboardHandler) primaryProbeURL() string {
	if h.PrimaryProbeURL == "" {
		return DefaultPrimaryProbeURL
	}
	return h.PrimaryProbeURL
}

func (h *DashboardHandler) secondaryProbeURL() string {
	if h.SecondaryProbeURL == "" {
		return DefaultSecondaryProbeURL
	}
	return h.SecondaryProbeURL
}

// testRelayPool tests a deploy-relay pool (vercel/cloudflare/deno) the way
// upstream does: a plain GET to the relay URL with x-relay-target and
// x-relay-path headers (mirrors testVercelRelay in the Next.js dashboard).
func (h *DashboardHandler) testRelayPool(w http.ResponseWriter, r *http.Request, id, relayURL string) {
	start := time.Now()
	finish := func(ok bool, errStr string) {
		latencyMs := time.Since(start).Milliseconds()
		status := "passed"
		var lastError any
		if !ok {
			status = "failed"
			lastError = errStr
			latencyMs = 0
		}
		_ = h.Repo.UpdateProxyPool(id, map[string]any{
			"testStatus":   status,
			"lastTestedAt": time.Now().UTC().Format(time.RFC3339),
			"lastError":    lastError,
			"latency":      latencyMs,
			"isActive":     ok,
		})
		body := map[string]any{"success": ok, "status": status, "latency": latencyMs}
		if !ok {
			body["error"] = errStr
		}
		handlerutil.WriteJSON(w, http.StatusOK, body)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, relayURL, nil)
	if err != nil {
		finish(false, "invalid relay URL format")
		return
	}
	req.Header.Set("x-relay-target", "https://httpbin.org")
	req.Header.Set("x-relay-path", "/get")
	tr := &http.Transport{
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			msg = "Relay test timed out"
		}
		finish(false, msg)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		finish(false, "relay test failed with status "+resp.Status)
		return
	}
	finish(true, "")
}

// poolProbeTransport returns the transport the pool test endpoint probes
// through, or a message for the operator when the URL cannot be dialled.
//
// It goes through proxy.NewSafeTransportForProxy rather than building its own
// http.Transport: the shared constructor validates the scheme and applies the
// SSRF dialer's proxy exemption, so a pool this reports as healthy is one
// requests can actually dial. A hand-built transport accepted any scheme
// url.Parse accepted and could report "passed" for a pool the request path
// would refuse.
func poolProbeTransport(targetURL string) (*http.Transport, string) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, "invalid proxy URL format"
	}
	if !proxy.ProxyURLDialable(targetURL) {
		return nil, "unsupported proxy protocol in " + parsed.Scheme
	}

	tr := proxy.NewSafeTransportForProxy(targetURL)
	// The probe opens one connection and discards it; keep-alives would leave
	// the socket to the idle timeout instead of closing with the request.
	tr.DisableKeepAlives = true
	return tr, ""
}
