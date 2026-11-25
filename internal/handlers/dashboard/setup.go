package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"

	"golang.org/x/crypto/bcrypt"
)

// setupCompleted reports whether the first-run wizard has nothing left to ask.
// Three ways to be done: a stored hash, a configured INITIAL_PASSWORD, or an
// explicit "no password here" (requireLogin false, written by the wizard's Skip
// and by Settings > Security). The third is a decision, not an absence — a
// gateway whose operator turned login off must not be shown a setup form that
// would turn it back on.
func setupCompleted(raw map[string]any) bool {
	if hasStoredPassword(raw) || config.LoadConfig().InitialPassword != "" {
		return true
	}
	if v, ok := raw["requireLogin"].(bool); ok && !v {
		return true
	}
	return false
}

// personalModeHiddenNav is the sidebar hide list written when the operator
// picks Personal. Plans, the token saver and Persona are the three surfaces
// that only matter when someone else is spending the quota. The list is
// advisory: the routes stay reachable, because the dashboard is single-operator
// behind a password and Plans only arranges the operator's own keys.
var personalModeHiddenNav = []string{"plans", "token-saver", "jailbreak"}

// First-run setup wizard
// A fresh install sets the dashboard password from the
// browser before the first login, instead of making the operator type the
// well-known default first.
//
// The gate is the same one mustChangeDefaultPassword uses: the write is only
// accepted while no password hash is stored and the request arrives from the
// loopback interface. A stored hash, or a remote peer, answers 403 — the
// endpoint must never become a password reset for an already-configured
// gateway.

// HandleGetSetup handles GET /api/setup: whether the first-run wizard applies,
// and which database engine backs this instance (the wizard's state
// shape: status plus database_type).
func (h *DashboardHandler) HandleGetSetup(w http.ResponseWriter, r *http.Request) {
	raw := settingsOrEmpty(h)
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		// The wizard applies only while setup is incomplete: no stored hash,
		// no INITIAL_PASSWORD, and no "skip" recorded. Aligns with the POST
		// gate below, so the form never offers itself when the server would
		// refuse it.
		"status":         !setupCompleted(raw),
		"databaseType":   string(db.CurrentEngine()),
		"requestIsLocal": nodeRequestIsLocal(r),
	})
}

// HandlePostSetup handles POST /api/setup with {"password": "..."}: it stores
// the bcrypt hash and signs the session in, so the operator lands in the
// dashboard they just unlocked. Only a fresh, local install qualifies.
func (h *DashboardHandler) HandlePostSetup(w http.ResponseWriter, r *http.Request) {
	raw := settingsOrEmpty(h)
	if setupCompleted(raw) {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"error":   "Setup already completed or password path already chosen",
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
	// Cross-site form POSTs (CSRF): the loopback peer is not necessarily the
	// operator — a same-host browser can be driven by any page it has open.
	// The sibling POST /api/auth/reset-password applies this same gate.
	if !isSameOriginOrLocal(r) {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"error":   "Cross-site setup is prohibited",
		})
		return
	}

	body, err := readSetupBody(r)
	if err != nil {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   "invalid JSON",
		})
		return
	}
	// Skip: the operator does not want a password on this machine. That is the
	// same switch Settings > Security exposes as "Require login" — the
	// dashboard then answers to anyone who can reach it, which is a deliberate
	// choice for a box behind a firewall, not an accident. A password is never
	// stored in this path, so turning the requirement back on falls through to
	// INITIAL_PASSWORD / the default the same way a fresh install does.
	skipPassword, _ := body["skipPassword"].(bool)
	advanced := true
	if v, ok := body["advancedMode"].(bool); ok {
		advanced = v
	}
	if skipPassword {
		updates := map[string]any{"requireLogin": false}
		if !advanced {
			if nav := normalizeNavVisibility(map[string]any{"hidden": personalModeHiddenNav}); nav != nil {
				updates[navVisibilitySettingKey] = nav
			}
		}
		if err := h.Repo.UpdateSettingsRaw(updates); err != nil {
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
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "passwordSkipped": true})
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
	// The wizard's second question: is this gateway for one person, or for
	// keys someone else spends? Personal fills the sidebar hide list so the
	// first screen after login already matches the mode; Advanced writes no
	// hide list, so the deployment keeps every entry it always had.
	updates := map[string]any{"password": string(hashed)}
	if !advanced {
		if nav := normalizeNavVisibility(map[string]any{"hidden": personalModeHiddenNav}); nav != nil {
			updates[navVisibilitySettingKey] = nav
		}
	}

	if err := h.Repo.UpdateSettingsRaw(updates); err != nil {
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	// The wizard just replaced the stored hash (and possibly bumped nothing —
	// a first install has no epoch). Install the new state before the session
	// below is signed, so the token it carries verifies against it.
	h.refreshSessionEpoch()

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

// readSetupBody reads the setup POST body with a hard bound. The sibling
// reset-password handler bounds its body the same way: a fresh install has no
// reason to accept an unbounded read from an anonymous route, and
// readJSONBody's io.ReadAll would.
func readSetupBody(r *http.Request) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body, nil
}
