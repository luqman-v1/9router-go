package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/keikey"
	"9router/proxy/internal/log"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
)

// HandleGetApiKeys handles GET /api/keys.
//
// Issue #199 makes the secret readable again so the dashboard can reveal and
// copy it, which reverses the F-6 contract. That reversal is scoped to
// dashboard credentials: a caller holding only an engine client key — the
// credential handed to Cursor or Claude Code — still gets the masked value, so
// one leaked key cannot dump the whole key set.
func (h *DashboardHandler) HandleGetApiKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Repo.GetApiKeys()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	privileged := middleware.CallerIsDashboardPrivileged(h.Repo, r)
	sanitized := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		if k == nil {
			continue
		}
		sanitized = append(sanitized, map[string]any{
			"id": k.ID, "key": apiKeySecretFor(k, privileged), "keyDisplay": apiKeyDisplay(k), "name": k.Name,
			"machineId": k.MachineID, "isActive": k.IsActive, "createdAt": k.CreatedAt,
			// The governance columns belong in the list, not only behind a
			// per-key fetch: the dashboard has to tell a constrained key from
			// an unconstrained one at a glance, and omitting them here made
			// every row read as "unrestricted" no matter what was set.
			"rateLimitRpm":         intOrZero(k.RateLimitRPM),
			"rateLimitTpm":         intOrZero(k.RateLimitTPM),
			"rateLimitConcurrency": intOrZero(k.RateLimitConcurrency),
			"expiresAt":            stringOrEmpty(k.ExpiresAt),
			"lastUsedAt":           stringOrEmpty(k.LastUsedAt),
			"usedCount":            intOrZero(k.UsedCount),
			"metadata":             stringOrEmpty(k.Metadata),
		})
	}
	handlerutil.WriteJSON(w, http.StatusOK, sanitized)
}

// apiKeySecretFor returns the stored secret to a dashboard credential and the
// masked value to everyone else. Rows written before issue #199 hold only an
// argon2id verifier, so their plaintext is unrecoverable and the caller gets an
// empty string; the row falls back to its masked display in the UI.
func apiKeySecretFor(k *models.APIKey, privileged bool) string {
	if !privileged || db.IsHashedKeyRow(k) {
		return ""
	}
	return k.Key
}

// apiKeyDisplay returns the masked value for a key, falling back to masking
// the plaintext on the fly for a row that never recorded one.
func apiKeyDisplay(k *models.APIKey) string {
	if k.KeyDisplay != nil && *k.KeyDisplay != "" {
		return *k.KeyDisplay
	}
	return maskClientKey(k.Key)
}

// maskClientKey shows the first/last few chars of a client key (dashboard
// display only).
func maskClientKey(key string) string {
	if len(key) <= 12 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-4:]
}

// intOrZero unwraps an optional int column, treating NULL and 0 alike: both
// mean "no limit configured".
func intOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// stringOrEmpty unwraps an optional text column, normalizing NULL to "".
func stringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// HandleCreateApiKey handles POST /api/keys.
// Creates new apiKey (generates uuid if empty, or key if empty sk-...).
func (h *DashboardHandler) HandleCreateApiKey(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		Name      string `json:"name"`
		MachineID string `json:"machineId"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if req.Key == "" {
		req.Key = "sk-" + strings.ReplaceAll(uuid.New().String(), "-", "")
	}

	if err := h.Repo.CreateApiKey(req.ID, req.Key, req.Name, req.MachineID); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The secret is stored in the clear so the dashboard can reveal and copy
	// it again later (issue #199). This reopens the F-6/DB-02 trade-off — a
	// database dump exposes every client key — but an operator who cannot
	// read a key back cannot use the row they just created either.

	// The masked value is kept alongside it: the list still renders a compact
	// identifier, and a row that predates this change has no plaintext to show.
	if err := h.Repo.SetApiKeyDisplay(req.ID, keikey.Mask(req.Key)); err != nil {
		log.Error("apikeys", "store api key display failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     req.ID,
		"key":    req.Key,
	})
}

// HandleRotateApiKey handles POST /api/keys/{id}/rotate.
//
// Rotation mints a replacement secret in place: the row keeps its id, policy,
// usage history and allowlist, so a leaked key can be replaced without
// deleting the record.
func (h *DashboardHandler) HandleRotateApiKey(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing apiKey id")
		return
	}
	existing, err := h.Repo.GetApiKeyByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "api key not found")
		return
	}

	plaintext := "sk-" + strings.ReplaceAll(uuid.New().String(), "-", "")
	if err := h.Repo.RotateApiKeySecret(id, plaintext, keikey.Mask(plaintext)); err != nil {
		log.Error("apikeys", "rotate: store secret failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The old key is live in the auth cache for its TTL; without this the
	// leaked secret keeps working after the operator rotated it away.
	keikey.DefaultAuthCache().InvalidateByID(id)

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     id,
		"key":    plaintext,
	})
}

// HandleDeleteApiKey handles DELETE /api/keys/{id}.
// Deletes client apiKey by ID.
func (h *DashboardHandler) HandleDeleteApiKey(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing apiKey id")
		return
	}

	if err := h.Repo.DeleteApiKey(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The auth cache holds the verified row for its TTL, so without this a key
	// deleted here keeps authenticating until the entry expires.
	keikey.DefaultAuthCache().InvalidateByID(id)

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// HandleToggleApiKey handles PUT /api/keys/{id}/toggle.
// Toggles isActive flag of client apiKey or sets it to specified boolean if provided.
func (h *DashboardHandler) HandleToggleApiKey(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing apiKey id")
		return
	}

	var req struct {
		IsActive *bool `json:"isActive"`
	}
	if r.Body != nil {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
	}

	var newStatus bool
	if req.IsActive != nil {
		newStatus = *req.IsActive
	} else {
		keys, err := h.Repo.GetApiKeys()
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		found := false
		for _, k := range keys {
			if k.ID == id {
				newStatus = !(k.IsActive == 1)
				found = true
				break
			}
		}
		if !found {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "api key not found")
			return
		}
	}

	if err := h.Repo.SetApiKeyStatus(id, newStatus); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Same reason as the delete path: the cached row would otherwise keep the
	// key authenticating after it was deactivated, and deactivating is exactly
	// what an operator does when they believe a key has leaked.
	keikey.DefaultAuthCache().InvalidateByID(id)

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"id":       id,
		"isActive": newStatus,
	})
}
