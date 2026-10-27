package dashboard

import (
	"net/http"

	"9router/proxy/internal/handlerutil"
)

// HandleResetPassword serves POST /api/auth/reset-password: clear the stored
// dashboard password hash so the gateway falls back to the configured initial
// password.
//
// Upstream ships this as the recovery path for an operator who forgot their
// password: it is local-only (the Next dashboard guard enforces that), and it
// never returns the literal. Without it there is no recovery short of deleting
// the database, because `password` is a protected setting and a PUT through
// /api/settings is refused.
//
// Local-only is enforced here too: the route is reachable without a session
// precisely because it exists for a locked-out operator, so the network origin
// is the only thing standing between an exposed tunnel and a password reset.
func (h *DashboardHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !nodeRequestIsLocal(r) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, "Password reset is only available from localhost")
		return
	}
	// An empty string, not a deleted key: UpdateSettingsRaw merges, and the
	// readers treat "" as "no stored password" (`hasStoredPassword`). Writing
	// nil would leave the old hash in place.
	if err := h.Repo.UpdateSettingsRaw(map[string]any{"password": ""}); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
