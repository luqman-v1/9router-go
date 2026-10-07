package dashboard

import (
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/vault"
)

// HandleGetVaultStatus handles GET /api/vault/status.
//
// It reports whether sealing is active and how many connections are sealed
// versus still holding a plaintext credential, so an operator can see the
// migration state without reading the database.
func (h *DashboardHandler) HandleGetVaultStatus(w http.ResponseWriter, r *http.Request) {
	v := h.Repo.Vault()
	sealed, plaintext, err := h.Repo.VaultCounts()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"enabled":       v != nil && v.Enabled(),
		"sealedCount":   sealed,
		"plaintextCount": plaintext,
	})
}

// HandleRotateVault handles POST /api/vault/rotate.
//
// It re-wraps every stored data-encryption key under a new master key. The
// secret ciphertext is never touched — that is the whole point of envelope
// encryption — so this is cheap regardless of how many connections hold
// credentials.
//
// Rows whose DEK cannot be unwrapped are reported and skipped, never rewritten:
// a rotation must not destroy the rows it cannot read.
func (h *DashboardHandler) HandleRotateVault(w http.ResponseWriter, r *http.Request) {
	v := h.Repo.Vault()
	if v == nil || !v.Enabled() {
		handlerutil.WriteJSONError(
			w, http.StatusBadRequest,
			"Credential vault is disabled. Set ROUTER_MASTER_KEY before rotating.",
		)
		return
	}

	var req struct {
		MasterKey string `json:"masterKey"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	newKey, err := vault.DecodeMasterKey(req.MasterKey)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	rewrapped, failures, err := h.Repo.RewrapConnectionDEKs(v, newKey)
	if err != nil {
		if errors.Is(err, vault.ErrDisabled) {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := v.Rotate(newKey); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"rewrapped":  rewrapped,
		"failures":   failures,
	})
}