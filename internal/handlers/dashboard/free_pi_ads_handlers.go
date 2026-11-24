package dashboard

import (
	"encoding/json"
	"io"
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

// freePiConnectionForAd resolves a free-pi connection and its credential.
//
// The session id comes back too: free-pi's ad endpoints are part of the same
// logical session as the completions, and the official CLI sends x-session-id on
// /ads/next and /ads/impression for exactly that reason (free-pi-cli
// src/vendor/pi-ads/api.ts). Sending an ad request without it leaves the
// impression attributed to no session at all.
func (h *DashboardHandler) freePiConnectionForAd(connID string) (baseURL, token, sessionID string, ok bool) {
	if h.Repo == nil || connID == "" {
		return "", "", "", false
	}
	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil || conn == nil || conn.Provider != providers.FreePiProviderID {
		return "", "", "", false
	}
	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, apiKey, _ := usageCreds(data)
	token = firstNonEmptyStr(accessToken, apiKey)
	if token == "" {
		return "", "", "", false
	}
	base, _ := data["baseUrl"].(string)
	return base, token, freePiAdSessionID(token, data), true
}

// freePiAdSessionID is the session id the ad endpoints must carry.
//
// It is the same derivation the executor uses, via executor.FreePiSessionID, so
// the creative is fetched under the session the completions use. An ad fetched
// under a different id would be attributed to a session that never ran the
// conversation.
//
// Derived unconditionally, never read from connection data: the executor no
// longer honours a stored id, so preferring one here would report an impression
// under a session the completions are not using.
func freePiAdSessionID(token string, _ map[string]any) string {
	return executor.FreePiSessionID(token)
}

// HandleGetFreePiAd serves the current banner creative for one connection.
//
// GET /api/free-pi/ad?connectionId=…
//
// Serving it stores it, which is what lets the impression endpoint verify that
// the ad it is asked to report is one it actually handed out.
func (h *DashboardHandler) HandleGetFreePiAd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	connID := r.URL.Query().Get("connectionId")
	baseURL, token, sessionID, ok := h.freePiConnectionForAd(connID)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no free-pi connection with a credential for that id")
		return
	}

	// A prefetched creative is served when one is warm and fresh: that is what
	// makes the banner appear immediately rather than after a round trip. The
	// fetch below stays as the fallback, so a connection the prefetch has not
	// reached yet still gets an ad.
	if warm, ok := freePiWarmAdFor(connID); ok {
		freePiStorePendingAd(connID, warm)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ad": warm})
		return
	}

	ad, err := freePiFetchAd(r.Context(), token, baseURL, sessionID)
	if err != nil {
		// An ad is decoration: a failure here must not look like a broken
		// provider, so report "no ad" and log the cause.
		log.Warn("free-pi", "ad fetch failed", "conn", connID, "error", err)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ad": nil})
		return
	}
	if ad == nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ad": nil})
		return
	}

	freePiStorePendingAd(connID, *ad)
	freePiStoreWarmAd(connID, *ad)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ad": ad})
}

// HandleFreePiImpression records that a served creative was actually painted.
//
// POST /api/free-pi/ad/impression {connectionId, adId}
//
// This is the only path that reports an impression, it requires the id of an ad
// this server served, and it consumes that serving — so a client cannot report
// an ad it was never shown, nor report the same view twice.
func (h *DashboardHandler) HandleFreePiImpression(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ConnectionID string `json:"connectionId"`
		AdID         string `json:"adId"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	}
	if body.ConnectionID == "" || body.AdID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "connectionId and adId are required")
		return
	}

	ad, ok := freePiTakePendingAd(body.ConnectionID, body.AdID)
	if !ok {
		// Unknown, expired, or already reported. Refusing is the point: an
		// impression must correspond to a creative this server served and has
		// not yet been paid for.
		handlerutil.WriteJSONError(w, http.StatusConflict,
			"no matching unreported ad for this connection; an impression requires a creative served by this gateway")
		return
	}

	baseURL, token, sessionID, ok := h.freePiConnectionForAd(body.ConnectionID)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no free-pi connection with a credential for that id")
		return
	}

	if err := freePiReportImpression(r.Context(), token, baseURL, sessionID, ad); err != nil {
		log.Warn("free-pi", "impression report failed", "conn", body.ConnectionID, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	log.Info("free-pi", "ad impression recorded", "conn", body.ConnectionID, "ad", ad.AdID)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"recorded": true})
}
