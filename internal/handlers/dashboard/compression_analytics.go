package dashboard

import (
	"net/http"

	"9router/proxy/internal/handlerutil"
)

// HandleGetCompressionAnalytics handles GET /api/analytics/compression.
// Supports ?since=24h|7d|30d|all (default: 24h).
func (h *DashboardHandler) HandleGetCompressionAnalytics(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		since = "24h"
	}
	switch since {
	case "24h", "7d", "30d", "all":
	default:
		since = "24h"
	}

	summary, err := h.Repo.GetCompressionAnalyticsSummary(r.Context(), since)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, summary)
}
