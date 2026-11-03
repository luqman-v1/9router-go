package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
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
		TokenQuota           *int    `json:"tokenQuota"`
		QueueTimeoutMs       *int    `json:"queueTimeoutMs"`
		// F-2 per-period budget. Pointers so an omitted field leaves the
		// stored value alone; an explicit 0 / "" is a deliberate "disable this
		// axis", which is what the dashboard sends when the operator blanks
		// the field.
		LimitMicros  *int64  `json:"limitMicros"`
		LimitTokens  *int64  `json:"limitTokens"`
		BudgetPeriod *string `json:"budgetPeriod"`
		AlertPct     *int    `json:"alertPct"`
		HardCutoff   *bool   `json:"hardCutoff"`
		ExpiresAt            *string `json:"expiresAt"`
		Metadata             *string `json:"metadata"`
		// F-3: the reusable policy template this key inherits from. Pointer so
		// an omitted field leaves the assignment alone; an empty string is a
		// deliberate "detach from the plan".
		PlanID *string `json:"planId"`
		// Renaming a key. `name` is a pointer so an omitted field leaves the
		// stored name alone; an empty string is a deliberate "clear the name",
		// which is what the dashboard's edit dialog sends when the operator
		// blanks the field.
		Name *string `json:"name"`
		// Model access, in the shape the Node dashboard sends: an object, not
		// the flat `models` list. Omitted leaves the stored allowlist alone;
		// present is validated and written whole (see apikeys_access.go).
		Access *keyAccessInput `json:"access"`
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
	quota, err := mergeLimit(req.TokenQuota, existing.TokenQuota)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "tokenQuota: "+err.Error())
		return
	}
	queueMs, err := mergeLimit(req.QueueTimeoutMs, existing.QueueTimeoutMs)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "queueTimeoutMs: "+err.Error())
		return
	}
	// Per-period budget. The same merge rule as the rate limits: an omitted
	// field keeps what is stored, an explicit zero clears that axis. The
	// period is validated against the four windows the engine resolves, so a
	// typo cannot silently fall back to monthly — the engine's own fallback
	// would otherwise make a misconfigured key look correctly configured.
	budgetMicros := existing.LimitMicros
	if req.LimitMicros != nil {
		if *req.LimitMicros < 0 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "limitMicros: must be zero or a positive amount")
			return
		}
		budgetMicros = *req.LimitMicros
	}
	budgetTokens := existing.LimitTokens
	if req.LimitTokens != nil {
		if *req.LimitTokens < 0 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "limitTokens: must be zero or a positive count")
			return
		}
		budgetTokens = *req.LimitTokens
	}
	budgetPeriod := existing.BudgetPeriod
	if req.BudgetPeriod != nil {
		budgetPeriod = strings.TrimSpace(*req.BudgetPeriod)
		if budgetPeriod != "" && !db.IsValidPlanPeriod(budgetPeriod) {
			handlerutil.WriteJSONError(w, http.StatusBadRequest,
				"budgetPeriod: must be daily, weekly, monthly or total")
			return
		}
	}
	alertPct := existing.AlertPct
	if req.AlertPct != nil {
		if *req.AlertPct < 0 || *req.AlertPct > 100 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "alertPct: must be between 0 and 100")
			return
		}
		alertPct = *req.AlertPct
	}
	hardCutoff := existing.HardCutoff
	if req.HardCutoff != nil {
		hardCutoff = *req.HardCutoff
	}

	// Every field is validated before the first write, so a refused request
	// leaves the row exactly as it was instead of applying the fields that
	// happened to be checked first.
	expiresAt := ""
	if req.ExpiresAt != nil {
		expiresAt = strings.TrimSpace(*req.ExpiresAt)
		if err := validateAPIKeyExpiry(expiresAt); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	meta := ""
	if req.Metadata != nil {
		meta = strings.TrimSpace(*req.Metadata)
		if err := validateAPIKeyMetadata(meta); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	name := ""
	if req.Name != nil {
		// A name is operator-facing only, but an unbounded one is stored in the
		// dashboard's list and in the auth cache, so cap it rather than let a
		// pasted megabyte ride along in every response.
		name = strings.TrimSpace(*req.Name)
		if len(name) > maxAPIKeyNameLen {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "name is too long")
			return
		}
	}
	// F-3 plan assignment. An unknown plan is refused rather than stored: a
	// dangling id would silently resolve to "no plan", so the key would keep
	// its old limits while the dashboard showed it on a template.
	planID := ""
	if req.PlanID != nil {
		planID = strings.TrimSpace(*req.PlanID)
		if planID != "" {
			if _, err := h.Repo.GetPlan(planID); err != nil {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, "unknown plan: "+planID)
				return
			}
		}
	}
	// Access is validated before the first write like every other field: the
	// handler writes the governance columns unconditionally, so a body rejected
	// here must not have applied the rate limits already.
	var accessPatterns []string
	if req.Access != nil {
		patterns, err := validateKeyAccess(req.Access)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		accessPatterns = patterns
	}
	if err := h.Repo.SetApiKeyRateLimit(id, rpm, tpm, conc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Repo.SetApiKeyBudget(id, quota, queueMs); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The per-period budget is written unconditionally, like the rate limits
	// above: the handler already holds the merged value, and a conditional
	// write would leave a cleared axis stale.
	if err := h.Repo.SetApiKeyPeriodBudget(id, budgetMicros, budgetTokens, budgetPeriod, alertPct, hardCutoff); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.ExpiresAt != nil {
		if err := h.Repo.SetApiKeyExpiry(id, expiresAt); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.Metadata != nil {
		if err := h.Repo.SetApiKeyMetadata(id, meta); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.Name != nil {
		if err := h.Repo.SetApiKeyName(id, name); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.PlanID != nil {
		if err := h.Repo.SetApiKeyPlan(id, planID); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Access was validated above with every other field and is written here, so
	// a refused body leaves the allowlist exactly as it was.
	if req.Access != nil {
		if err := h.Repo.SetAllowedModels(id, accessPatterns); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// The auth cache holds a verified key row for a few seconds so argon2
	// does not run on every request. A policy change is not a latency trade-off:
	// the operator just revoked access, so the cached row must go now — an
	// expired, newly limited, or re-planned key must not keep working until its
	// TTL lapses.
	keikey.DefaultAuthCache().InvalidateByID(id)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     id,
		"planId": planID,
		"limits": map[string]int{
			"rpm": rpm, "tpm": tpm, "concurrency": conc,
			"tokenQuota": quota, "queueTimeoutMs": queueMs,
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
