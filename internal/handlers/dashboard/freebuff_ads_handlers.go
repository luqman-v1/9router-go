package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// freebuffConnectionForAd resolves a freebuff connection and its credential.
func (h *DashboardHandler) freebuffConnectionForAd(connID string) (baseURL, token string, ok bool) {
	if h.Repo == nil || connID == "" {
		return "", "", false
	}
	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil || conn == nil || conn.Provider != "freebuff" {
		return "", "", false
	}
	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, apiKey, _ := usageCreds(data)
	token = firstNonEmptyStr(accessToken, apiKey)
	if token == "" {
		if at, ok := data["authToken"].(string); ok && at != "" {
			token = at
		}
	}
	if token == "" {
		return "", "", false
	}
	base, _ := data["baseUrl"].(string)
	return base, token, true
}

// HandleGetFreebuffAd serves the current banner creative for one Freebuff connection.
//
// GET /api/freebuff/ad?connectionId=…
func (h *DashboardHandler) HandleGetFreebuffAd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	connID := r.URL.Query().Get("connectionId")
	baseURL, token, ok := h.freebuffConnectionForAd(connID)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no freebuff connection with a credential for that id")
		return
	}

	ad, err := freebuffFetchAd(r.Context(), token, baseURL)
	if err != nil {
		log.Warn("freebuff", "ad fetch failed", "conn", connID, "error", err)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ad": nil})
		return
	}
	if ad == nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ad": nil})
		return
	}

	freebuffStorePendingAd(connID, *ad)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ad": ad})
}

// HandleFreebuffImpression records that a served creative was actually painted.
//
// POST /api/freebuff/ad/impression {connectionId, adId}
func (h *DashboardHandler) HandleFreebuffImpression(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ConnectionID string `json:"connectionId"`
		AdID         string `json:"adId"`
	}
	if r.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		_ = json.Unmarshal(raw, &body)
	}
	if body.ConnectionID == "" || body.AdID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "connectionId and adId are required")
		return
	}
	ad, ok := freebuffTakePendingAd(body.ConnectionID, body.AdID)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusConflict,
			"no matching unreported ad for this connection; an impression requires a creative served by this gateway")
		return
	}

	baseURL, token, ok := h.freebuffConnectionForAd(body.ConnectionID)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no freebuff connection with a credential for that id")
		return
	}

	if err := freebuffReportImpression(r.Context(), token, baseURL, ad); err != nil {
		log.Warn("freebuff", "impression report failed", "conn", body.ConnectionID, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	log.Info("freebuff", "ad impression recorded", "conn", body.ConnectionID, "ad", ad.AdID)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"recorded": true})
}
