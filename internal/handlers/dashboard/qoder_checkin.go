package dashboard

import (
	"net/http"
	"path/filepath"

	"9router/proxy/internal/config"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/qodercheckin"
)

// HandleGetQoderCheckins returns the last daily-credit check-in outcome per
// Qoder account. The loop writes one file under DATA_DIR; reading it here keeps
// the dashboard view in sync with the background worker without a second
// in-memory cache. No network call is made.
func (h *DashboardHandler) HandleGetQoderCheckins(w http.ResponseWriter, r *http.Request) {
	checker, err := qodercheckin.NewChecker(filepath.Join(config.ResolveDataDir(), "qoder-checkin.json"), nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkins": checker.Results(),
	})
}
