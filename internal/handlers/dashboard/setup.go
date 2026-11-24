package dashboard

import (
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"

	"golang.org/x/crypto/bcrypt"
)

// First-run setup, inspired by QuantumNous/new-api's GET/POST /api/setup
// (controller/setup.go): a fresh install sets the dashboard password from the
// browser before the first login, instead of making the operator type the
// well-known default first.
//
// The gate is the same one mustChangeDefaultPassword uses: the write is only
// accepted while no password hash is stored and the request arrives from the
// loopback interface. A stored hash, or a remote peer, answers 403 — the
// endpoint must never become a password reset for an already-configured
// gateway.

// HandleGetSetup handles GET /api/setup: whether the first-run wizard applies,
// and which database engine backs this instance (mirrors new-api's Setup
// shape: status plus database_type).
func (h *DashboardHandler) HandleGetSetup(w http.ResponseWriter, r *http.Request) {
	raw := settingsOrEmpty(h)
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":         !hasStoredPassword(raw),
		"databaseType":   string(db.CurrentEngine()),
		"requestIsLocal": nodeRequestIsLocal(r),
	})
}

// HandlePostSetup handles POST /api/setup with {"password": "..."}: it stores
// the bcrypt hash and signs the session in, so the operator lands in the
// dashboard they just unlocked. Only a fresh, local install qualifies.
func (h *DashboardHandler) HandlePostSetup(w http.ResponseWriter, r *http.Request) {
	raw := settingsOrEmpty(h)
	if hasStoredPassword(raw) {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"error":   "Setup already completed",
		})
		return
	}
	if !nodeRequestIsLocal(r) {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"error":   "Setup is only available from the local machine",
		})
		return
	}

	body, err := readJSONBody(r)
	if err != nil {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   "invalid JSON",
		})
		return
	}
	password, _ := body["password"].(string)
	password = strings.TrimSpace(password)
	if len(password) < 8 {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   "Password must be at least 8 characters",
		})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"error":   "Failed to hash password",
		})
		return
	}
	if err := h.Repo.UpdateSettingsRaw(map[string]any{"password": string(hashed)}); err != nil {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	token, err := auth.Sign(auth.Secret(), time.Now())
	if err != nil {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"error":   "Failed to create session",
		})
		return
	}
	auth.SetCookie(w, r, token)
	h.RecordLogin(r, http.StatusOK, token)
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
