package dashboard

import (
	"errors"
	"io"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// importApiKeyRequest is the body of POST /api/keys/import. Every field but
// the key is optional: an importer replaying a saved export sends the same
// body twice and expects the second call to be a no-op.
type importApiKeyRequest struct {
	ID            string   `json:"id"`
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	MachineID     string   `json:"machineId"`
	AllowedModels []string `json:"allowedModels"`
}

// HandleImportApiKey handles POST /api/keys/import (#1557).
//
// The endpoint exists so an operator can move keys between instances, or
// re-apply a saved export, without minting a new secret: the key travels in
// the request and is stored hashed. Replaying the same body is a 200 with the
// stored row rather than a 409, which is what makes a scripted import safe to
// retry; a body that would overwrite a different secret is a 409.
//
// The response never carries keyHash or lookupHash, and the secret column is
// the masked display value: the plaintext is not recoverable from a hashed
// row, so echoing it back would be a value the caller already has at best and
// a lie at worst.
func (h *DashboardHandler) HandleImportApiKey(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req importApiKeyRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) > maxAPIKeyNameLen {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "name is too long")
		return
	}
	if req.Key == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	result, err := h.Repo.ImportApiKey(r.Context(), db.ImportApiKeyParams{
		ID:            req.ID,
		Key:           req.Key,
		Name:          req.Name,
		MachineID:     req.MachineID,
		AllowedModels: req.AllowedModels,
	})
	switch {
	case errors.Is(err, db.ErrApiKeyImportInvalid):
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, db.ErrApiKeyImportConflict):
		// A conflict names what collided: an operator pasting the wrong
		// export needs to know whether it was the id or the secret.
		handlerutil.WriteJSONError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		log.Error("apikeys", "import: store key failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to import api key")
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	allowed, _ := h.Repo.GetAllowedModels(result.Key.ID)
	if allowed == nil {
		allowed = []string{}
	}
	handlerutil.WriteJSON(w, status, importApiKeyResponse(result, allowed))
}

// importApiKeyResponse renders the stored row with the columns an importer
// needs and nothing else. keyHash and lookupHash are deliberately absent: the
// first is a verifier and the second an index, and neither is a value a client
// has any use for.
func importApiKeyResponse(result db.ImportApiKeyResult, allowedModels []string) map[string]any {
	k := result.Key
	if k == nil {
		return map[string]any{"status": "ok", "created": result.Created}
	}
	return map[string]any{
		"status":        "ok",
		"created":       result.Created,
		"id":            k.ID,
		"key":           apiKeyDisplay(k),
		"name":          k.Name,
		"machineId":     k.MachineID,
		"allowedModels": allowedModels,
		"isActive":      k.IsActive,
		"createdAt":     k.CreatedAt,
	}
}
