package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
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

	// `access` reports the EFFECTIVE policy: the key's own rows unioned with
	// the rows its plan grants. The chat gate unions the two (see
	// withPlanPatterns in internal/handlers/chat/access.go) and can only widen,
	// so the key's rows alone would understate what a plan-bound key may call.
	// `models` above stays the key's own rows — that is the field this endpoint
	// has always written, and silently rewriting it to include a plan would make
	// a subsequent read-modify-write through it promote the plan's rows onto the
	// key.
	effective, err := h.effectiveAccessPatterns(id, allowed)
	if err != nil {
		// The plan half is unknown, so the union is unknown. Reporting the own
		// rows as `access` would describe a policy the gate is not enforcing;
		// reporting the key as restricted keeps the two consistent in the safe
		// direction, the same way the gate fails closed.
		log.Error("apikeys", "read plan for key access failed; reporting the key as restricted", "error", err, "id", id)
		effective = []string{db.SentinelDenyAll}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"models": allowed,
		// The same policy in the shape the Node dashboard reads, so a client
		// that speaks only `access` still sees this key's restriction.
		"access": accessFromPatterns(effective),
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
