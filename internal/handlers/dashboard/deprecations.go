package dashboard

import (
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// HandleGetModelDeprecations handles GET /api/models/deprecations.
//
// It returns the recorded deprecations keyed by "<provider>/<model>" — the
// same key a combo entry is written as — so the dashboard can badge a whole
// model list in one pass. The provider query parameter narrows the result to
// a single provider; "" returns everything.
func (h *DashboardHandler) HandleGetModelDeprecations(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	deps, err := h.Repo.ListModelDeprecations(provider)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":     provider,
		"deprecations": deps,
	})
}
