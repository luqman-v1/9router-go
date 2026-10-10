package dashboard

import (
	"net/http"
	"time"

	"9router/proxy/internal/analyticsrange"
	"9router/proxy/internal/handlerutil"
)

// HandleGetCompressionAnalytics handles GET /api/analytics/compression.
//
// The window is the `period` parameter the rest of the Usage page already uses,
// resolved through analyticsrange so this section and the Overview cannot
// disagree about what "7d" means. `since` is still accepted: it was the only
// name this endpoint ever had, and a bookmark or an older SPA that sends it must
// keep working. `period` wins when both are present.
func (h *DashboardHandler) HandleGetCompressionAnalytics(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("period")
	if raw == "" {
		raw = r.URL.Query().Get("since")
	}
	win := analyticsrange.Resolve(raw, time.Now())

	summary, err := h.Repo.GetCompressionAnalyticsSummary(r.Context(), win)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The window the numbers describe, echoed so a client can tell an empty
	// window from one the server quietly answered for a different period.
	summary.Period = win.Label(raw)

	handlerutil.WriteJSON(w, http.StatusOK, summary)
}
