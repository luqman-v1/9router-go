package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
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
	// Access is a second table, so it is read in one query for the whole list
	// rather than once per key. A read failure must NOT read as "no
	// restriction": the gate fails closed on exactly that error (see
	// internal/handlers/chat/access.go), so reporting the keys as unrestricted
	// here would have the dashboard advertise an open key the request path is
	// in fact denying. Every key is reported restricted-with-nothing-allowed
	// instead, which is the safe direction and matches what the gate will do.
	allowlists, err := h.Repo.GetAllowedModelsForKeys(apiKeyIDs(keys))
	accessReadFailed := err != nil
	if accessReadFailed {
		log.Error("apikeys", "read model allowlists for the key list failed; reporting every key as restricted", "error", err)
		allowlists = map[string][]string{}
	}
	// The effective policy is the key's own rows unioned with its plan's, which
	// is what the gate enforces. The plan half is one joined query for the whole
	// page for the same reason the allowlist is.
	plans, planErr := h.Repo.GetPlansForKeys(apiKeyIDs(keys))
	if planErr != nil && !errors.Is(planErr, db.ErrNoPlansTable) {
		// The plan half of the union is unknown for every key on this page, so
		// the union is unknown — the same fail-closed answer as above rather
		// than reporting the own rows as if they were the whole policy.
		log.Error("apikeys", "read plans for the key list failed; reporting every key as restricted", "error", planErr)
		accessReadFailed = true
	}

	sanitized := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		if k == nil {
			continue
		}
		var access keyAccess
		if accessReadFailed {
			access = keyAccess{Restricted: true, Allow: []string{}}
		} else {
			// The key's rows alone understate the policy whenever a plan is
			// attached, and the direction is always widening (see
			// withPlanPatterns), so the union is what "what may this key call"
			// has to answer.
			access = accessFromPatterns(unionModelPatterns(allowlists[k.ID], plans[k.ID].AllowedModels))
		}
		sanitized = append(sanitized, map[string]any{
			// The Node dashboard reads access{restricted,allow}; Go's own
			// clients read the columns above. `access` is the EFFECTIVE policy
			// — the key's rows unioned with its plan's — because that is what
			// the gate enforces and what an operator reading the list means by
			// "what may this key call".
			"access": access,
			"id":     k.ID, "key": apiKeySecretFor(k, privileged), "keyDisplay": apiKeyDisplay(k), "name": k.Name,
			"machineId": k.MachineID, "isActive": k.IsActive, "createdAt": k.CreatedAt,
			// The governance columns belong in the list, not only behind a
			// per-key fetch: the dashboard has to tell a constrained key from
			// an unconstrained one at a glance, and omitting them here made
			// every row read as "unrestricted" no matter what was set.
			"rateLimitRpm":         intOrZero(k.RateLimitRPM),
			"rateLimitTpm":         intOrZero(k.RateLimitTPM),
			"rateLimitConcurrency": intOrZero(k.RateLimitConcurrency),
			"tokenQuota":           intOrZero(k.TokenQuota),
			"queueTimeoutMs":       intOrZero(k.QueueTimeoutMs),
			"expiresAt":            stringOrEmpty(k.ExpiresAt),
			"lastUsedAt":           stringOrEmpty(k.LastUsedAt),
			"usedCount":            intOrZero(k.UsedCount),
			"metadata":             stringOrEmpty(k.Metadata),
			// #365 The spend a tokenQuota is measured against. The dashboard
			// renders it against the quota as a progress bar and offers a
			// reset, so it has to travel with the row: without it every
			// exhausted key looked untouched and every untouched one looked
			// exhausted.
			"usedTokens": apiKeyUsedTokens(r.Context(), h.Repo, k.ID),
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
	if handlerutil.RuneCount(key) <= 12 {
		return "***"
	}
	return handlerutil.RunePrefix(key, 6) + "…" + handlerutil.RuneSuffix(key, 4)
}

// apiKeyIDs collects the non-empty ids of a key listing, for the single
// batched allowlist read behind the list's `access` field.
func apiKeyIDs(keys []*models.APIKey) []string {
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != nil && k.ID != "" {
			ids = append(ids, k.ID)
		}
	}
	return ids
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

// apiKeyUsedTokens reads the spend a key's tokenQuota is measured against.
//
// The listing is bounded by the number of client keys an operator has minted —
// tens, not thousands — so one read per row is cheaper than the alternative: a
// join against a masked-key column that carries no index. A read failure
// reports 0 rather than failing the listing, because the progress bar is
// informational; the limiter enforces the same quota from the same repository
// read on the request path.
func apiKeyUsedTokens(ctx context.Context, repo *db.Repo, keyID string) int {
	if repo == nil || keyID == "" {
		return 0
	}
	used, err := repo.GetApiKeyTokenUsageByID(ctx, keyID)
	if err != nil {
		log.Warn("apikeys", "usage read failed for list row", "error", err, "apiKeyId", keyID)
		return 0
	}
	return used
}

// apiKeyQuotaResetAt was the reset stamp reader. The mark is a usageHistory row
// id now (#365), and the list does not carry it: the dashboard's progress bar
// needs the spend, and the reset action reads the mark from its own response.

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

// HandleResetApiKeyQuota handles POST /api/keys/{id}/reset-quota (#365).
//
// It gives an exhausted key a fresh token budget without deleting the usage
// history behind it: the spend a quota is measured against is summed from the
// reset stamp forward, so analytics keep every row while the limiter starts the
// key at zero on its next request. The response carries the spend the key now
// has, which is what the dashboard renders as an empty progress bar.
func (h *DashboardHandler) HandleResetApiKeyQuota(w http.ResponseWriter, r *http.Request) {
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

	mark, err := h.Repo.ResetApiKeyQuota(r.Context(), id)
	if err != nil {
		log.Error("apikeys", "quota reset failed", "error", err, "apiKeyId", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	used, err := h.Repo.GetApiKeyTokenUsageByID(r.Context(), id)
	if err != nil {
		// The reset itself succeeded; a failed read-back is reported as 0
		// rather than turned into a failed reset the operator would retry.
		log.Warn("apikeys", "quota reset: usage read failed", "error", err, "apiKeyId", id)
		used = 0
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"id":         id,
		"usedTokens": used,
		"tokenQuota": intOrZero(existing.TokenQuota),
		// The mark is a usageHistory row id, so it is reported as one: a
		// timestamp here would invite callers to compare it with the
		// second-precision stamp in the history and get the boundary wrong.
		"quotaResetId": mark,
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
