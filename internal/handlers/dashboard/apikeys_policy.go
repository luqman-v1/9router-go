package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/keikey"
)

// HandleUpdateApiKey handles PUT /api/keys/{id}.
//
// It applies a partial update to the per-key governance columns: rate limits,
// expiry, resale metadata and the key's display name. Every field is a pointer
// so an omitted field is left alone rather than silently reset to zero — an
// operator editing only the expiry must not wipe the rate limits.
//
// The secret is not reachable from here: the key is an argon2id verifier plus
// a lookup hash, and there is no field that reads or rewrites them.

// maxAPIKeyNameLen bounds the operator-facing key name. The name is echoed in
// every /api/keys response and rendered in the dashboard list, so a pasted
// megabyte would ride along on each call.
const maxAPIKeyNameLen = 200

// HandleUpdateApiKey handles PUT /api/keys/{id}, a partial update: an omitted
// field is left as stored rather than reset.
//
// The secret is not reachable from here: the key is an argon2id verifier plus
// a lookup hash, and there is no field that reads or rewrites them.
func (h *DashboardHandler) HandleUpdateApiKey(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing apiKey id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		RateLimitRPM         *int    `json:"rateLimitRpm"`
		RateLimitTPM         *int    `json:"rateLimitTpm"`
		RateLimitConcurrency *int    `json:"rateLimitConcurrency"`
		ExpiresAt            *string `json:"expiresAt"`
		Metadata             *string `json:"metadata"`
		// Renaming a key. `name` is a pointer so an omitted field leaves the
		// stored name alone; an empty string is a deliberate "clear the name",
		// which is what the dashboard's edit dialog sends when the operator
		// blanks the field.
		Name *string `json:"name"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
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

	// A limit is only meaningful when it is a whole non-negative number; a
	// negative value would read as "unlimited" in the limiter while looking
	// like a configured limit in the dashboard.
	rpm, err := mergeLimit(req.RateLimitRPM, existing.RateLimitRPM)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "rateLimitRpm: "+err.Error())
		return
	}
	tpm, err := mergeLimit(req.RateLimitTPM, existing.RateLimitTPM)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "rateLimitTpm: "+err.Error())
		return
	}
	conc, err := mergeLimit(req.RateLimitConcurrency, existing.RateLimitConcurrency)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "rateLimitConcurrency: "+err.Error())
		return
	}

	if err := h.Repo.SetApiKeyRateLimit(id, rpm, tpm, conc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.ExpiresAt != nil {
		if err := h.Repo.SetApiKeyExpiry(id, strings.TrimSpace(*req.ExpiresAt)); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.Metadata != nil {
		meta := strings.TrimSpace(*req.Metadata)
		if meta != "" {
			// Metadata is stored verbatim and read back by the operator; reject
			// anything that is not a JSON object rather than persisting a value
			// that later fails to parse.
			var probe map[string]any
			if err := json.Unmarshal([]byte(meta), &probe); err != nil {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, "metadata must be a JSON object")
				return
			}
		}
		if err := h.Repo.SetApiKeyMetadata(id, meta); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if req.Name != nil {
		// A name is operator-facing only, but an unbounded one is stored in the
		// dashboard's list and in the auth cache, so cap it rather than let a
		// pasted megabyte ride along in every response.
		name := strings.TrimSpace(*req.Name)
		if len(name) > maxAPIKeyNameLen {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "name is too long")
			return
		}
		if err := h.Repo.SetApiKeyName(id, name); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// The auth cache holds a verified key row for a few seconds so argon2
	// does not run on every request. A policy change is not a latency trade-off:
	// the operator just revoked access, so the cached row must go now — an
	// expired or newly limited key must not keep working until its TTL lapses.
	keikey.DefaultAuthCache().InvalidateByID(id)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     id,
		"limits": map[string]int{
			"rpm": rpm, "tpm": tpm, "concurrency": conc,
		},
	})
}

// mergeLimit resolves an optional field against the stored value.
func mergeLimit(patch, existing *int) (int, error) {
	if patch == nil {
		if existing == nil {
			return 0, nil
		}
		return *existing, nil
	}
	if *patch < 0 {
		return 0, errNegativeLimit
	}
	return *patch, nil
}

var errNegativeLimit = errNegative("must be zero (unlimited) or a positive count")

type errNegative string

func (e errNegative) Error() string { return string(e) }