package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// Per-API-key model access control (F-7) — dashboard surface.
//
// This file is a sibling of apikeys.go on purpose: the two are owned by
// different slices and must not collide.

// HandleGetApiKeyModels handles GET /api/keys/{id}/models.
// Returns the key's allowlist patterns. An empty list means "no allowlist",
// which the chat gate reads as allow-everything.
func (h *DashboardHandler) HandleGetApiKeyModels(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing apiKey id")
		return
	}

	allowed, err := h.Repo.GetAllowedModels(id)
	if err != nil {
		log.Error("apikeys", "read model allowlist failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to read model allowlist")
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"models": allowed,
	})
}

// HandleSetApiKeyModels handles PUT /api/keys/{id}/models.
// Replaces the allowlist in one transaction. Accepts either
// {"models": ["a", "b"]} or a bare ["a", "b"] body. An empty list clears the
// allowlist and restores the allow-everything default.
func (h *DashboardHandler) HandleSetApiKeyModels(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing apiKey id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	allowed, err := parseModelAllowlist(body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.Repo.SetAllowedModels(id, allowed); err != nil {
		log.Error("apikeys", "write model allowlist failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save model allowlist")
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     id,
		"models": allowed,
	})
}

// parseModelAllowlist accepts the wrapped and the bare array form. Entries are
// trimmed and blanks dropped; the repository de-duplicates the rest against the
// (api_key_id, model) primary key.
func parseModelAllowlist(body []byte) ([]string, error) {
	if len(body) == 0 {
		return []string{}, nil
	}

	var wrapped struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Models != nil {
		return validateModelAllowlist(cleanModelPatterns(wrapped.Models))
	}

	var bare []string
	if err := json.Unmarshal(body, &bare); err != nil {
		return nil, fmt.Errorf("models must be a string array")
	}
	return validateModelAllowlist(cleanModelPatterns(bare))
}

// maxAllowlistEntries and maxAllowlistEntryLength bound one key's allowlist.
// Every entry is matched against every candidate id on every request, so an
// unbounded list is a load a single dashboard call can set up. Upstream applies
// the same 200/256 limits in validateKeyAccessInput.
const (
	maxAllowlistEntries     = 200
	maxAllowlistEntryLength = 256
)

// validateModelAllowlist rejects an oversized allowlist with a message naming
// the limit, so the dashboard can show the operator what to fix rather than a
// generic 400.
func validateModelAllowlist(models []string) ([]string, error) {
	if len(models) > maxAllowlistEntries {
		return nil, fmt.Errorf("too many model patterns: %d, maximum is %d", len(models), maxAllowlistEntries)
	}
	for _, m := range models {
		if len(m) > maxAllowlistEntryLength {
			return nil, fmt.Errorf("model pattern exceeds %d characters: %q", maxAllowlistEntryLength, truncateForError(m))
		}
	}
	return models, nil
}

// truncateForError keeps a rejected pattern readable in an error message
// instead of echoing an arbitrarily long operator paste back at them.
func truncateForError(s string) string {
	const head = 32
	if len(s) <= head {
		return s
	}
	return s[:head] + "…"
}

// cleanModelPatterns trims entries and drops blanks, so a trailing comma or a
// pasted newline cannot become an unmatchable pattern.
func cleanModelPatterns(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}