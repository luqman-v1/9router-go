package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/keikey"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

// HandleGetApiKeys handles GET /api/keys.
// F-6: client keys are stored as argon2id verifiers, so this read path can
// only ever return the masked display value — not even a fully authenticated
// dashboard caller (session cookie, CLI token, or requireLogin=false) gets
// the secret back. The full value is returned once, by HandleCreateApiKey.
func (h *DashboardHandler) HandleGetApiKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Repo.GetApiKeys()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sanitized := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		if k == nil {
			continue
		}
		sanitized = append(sanitized, map[string]any{
			"id": k.ID, "key": apiKeyDisplay(k), "keyDisplay": apiKeyDisplay(k), "name": k.Name,
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

// apiKeyDisplay returns the masked value for a key. Hashed rows keep it in
// keyDisplay; legacy rows that have not been hashed yet are masked on the fly.
func apiKeyDisplay(k *models.APIKey) string {
	if k.KeyDisplay != nil && *k.KeyDisplay != "" {
		return *k.KeyDisplay
	}
	return maskClientKey(k.Key)
}

// maskClientKey shows the first/last few chars of a client key (dashboard
// display only); the full value is returned once at creation.
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

	// F-6: derive the argon2id verifier and blank the plaintext column before
	// responding. The full value is still returned here, exactly once — the
	// dashboard's media example cards use it as a Bearer credential right
	// after creation.
	hash, err := keikey.Hash(req.Key)
	if err != nil {
		log.Error("apikeys", "hash api key failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to secure api key")
		return
	}
	if err := h.Repo.HashApiKeyRow(req.ID, hash, keikey.LookupHash(req.Key), keikey.Mask(req.Key)); err != nil {
		log.Error("apikeys", "store api key hash failed", "error", err)
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
// F-6 removes "reveal", so rotation is the only way an operator can replace a
// leaked key: the old secret is invalidated at once and the new one is
// returned exactly once, the same contract as creation. Without it a key that
// leaks can only be deleted, which is the wrong tool when the caller still
// needs the row — its policy, its usage history, its allowlist.
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
	hash, err := keikey.Hash(plaintext)
	if err != nil {
		log.Error("apikeys", "rotate: hash failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to secure api key")
		return
	}
	if err := h.Repo.HashApiKeyRow(id, hash, keikey.LookupHash(plaintext), keikey.Mask(plaintext)); err != nil {
		log.Error("apikeys", "rotate: store hash failed", "error", err)
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
