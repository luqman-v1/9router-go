package dashboard

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"9router/proxy/internal/db"
	"9router/proxy/internal/guardrails"
	"9router/proxy/internal/handlerutil"
)

// maxGuardrailLogLimit bounds the audit page so a query cannot ask for every
// row ever written.
const maxGuardrailLogLimit = 500

// HandleCreateGuardrailPolicy handles POST /api/guardrails/policies.
func (h *DashboardHandler) HandleCreateGuardrailPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := h.guardrailPolicyFromBody(w, r, nil)
	if !ok {
		return
	}
	p.ID = uuid.New().String()
	p.TenantID = db.DefaultGuardrailTenant
	if err := h.Repo.CreateGuardrailPolicy(p); err != nil {
		// The unique (tenant, scope, scope_id) index is the only constraint a
		// create can hit, and an operator needs the reason rather than the
		// raw SQLite message.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			handlerutil.WriteJSONError(w, http.StatusConflict,
				"a policy already exists for this scope; edit or delete it first")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, p)
}

// HandleUpdateGuardrailPolicy handles PUT /api/guardrails/policies/{id}.
func (h *DashboardHandler) HandleUpdateGuardrailPolicy(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing policy id")
		return
	}
	existing, err := h.Repo.GetGuardrailPolicy(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "guardrail policy not found")
		return
	}

	p, ok := h.guardrailPolicyFromBody(w, r, &id)
	if !ok {
		return
	}
	if err := h.Repo.UpdateGuardrailPolicy(p); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, p)
}

// HandleGetGuardrailPolicies handles GET /api/guardrails/policies.
//
// scope is required. With no scopeId parameter the whole scope is listed,
// including policies bound to one provider/model/chain/key, which is what the
// dashboard needs: those rows live under their own scope id and a scope-wide
// read would hide them. An explicit scopeId (including an empty one) keeps the
// exact-match behaviour the resolver uses.
func (h *DashboardHandler) HandleGetGuardrailPolicies(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	scope := strings.TrimSpace(query.Get("scope"))
	scopeID := strings.TrimSpace(query.Get("scopeId"))
	if scope == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "scope is required")
		return
	}
	if !validGuardrailScope(scope) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unknown scope: "+scope)
		return
	}
	var (
		policies []*db.GuardrailPolicy
		err      error
	)
	if query.Has("scopeId") {
		policies, err = h.Repo.ListGuardrailPolicies(scope, scopeID)
	} else {
		policies, err = h.Repo.ListGuardrailPoliciesForScope(r.Context(), scope)
	}
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, policies)
}

// HandleDeleteGuardrailPolicy handles DELETE /api/guardrails/policies/{id}.
func (h *DashboardHandler) HandleDeleteGuardrailPolicy(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing policy id")
		return
	}
	if err := h.Repo.DeleteGuardrailPolicy(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// HandleListGuardrailLogs handles GET /api/guardrails/logs.
func (h *DashboardHandler) HandleListGuardrailLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > maxGuardrailLogLimit {
			n = maxGuardrailLogLimit
		}
		limit = n
	}
	logs, err := h.Repo.ListGuardrailLogs(limit)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, logs)
}

// guardrailPolicyFromBody parses and validates a policy payload.
func (h *DashboardHandler) guardrailPolicyFromBody(w http.ResponseWriter, r *http.Request, id *string) (*db.GuardrailPolicy, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return nil, false
	}
	defer r.Body.Close()

	var req struct {
		Name           string                     `json:"name"`
		Scope          string                     `json:"scope"`
		ScopeID        string                     `json:"scopeId"`
		Enabled        *bool                      `json:"enabled"`
		Config         string                     `json:"config"`
		CustomPatterns []guardrails.CustomPattern `json:"customPatterns,omitempty"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return nil, false
		}
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Scope = strings.TrimSpace(req.Scope)
	req.ScopeID = strings.TrimSpace(req.ScopeID)
	if req.Name == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "name is required")
		return nil, false
	}
	if !validGuardrailScope(req.Scope) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"scope must be one of global, provider, model, chain, apikey")
		return nil, false
	}
	if err := validateGuardrailConfig(req.Config); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	p := &db.GuardrailPolicy{
		Name:    req.Name,
		Scope:   req.Scope,
		ScopeID: req.ScopeID,
		Enabled: enabled,
		Config:  req.Config,
	}
	if id != nil {
		p.ID = *id
	}
	return p, true
}

// validateGuardrailConfig rejects a config the engine could not act on, so an
// operator never saves a policy that silently does nothing.
func validateGuardrailConfig(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errEmptyGuardrailConfig
	}
	var probe struct {
		Detectors      []string                   `json:"detectors"`
		Action         string                     `json:"action"`
		CustomPatterns []guardrails.CustomPattern `json:"customPatterns,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return errGuardrailConfigJSON
	}
	// Allow empty detectors if customPatterns are present.
	if len(probe.Detectors) == 0 && len(probe.CustomPatterns) == 0 {
		return errGuardrailNoDetectors
	}
	for _, d := range probe.Detectors {
		if d != "pii" && d != "injection" && d != "custom" {
			return errGuardrailUnknownDetector
		}
	}
	switch probe.Action {
	case "allow", "log_only", "warn", "mask", "block":
	default:
		return errGuardrailUnknownAction
	}
	// Validate custom patterns
	for _, cp := range probe.CustomPatterns {
		if strings.TrimSpace(cp.Regex) == "" {
			return errGuardrailCustomPatternEmptyRegex
		}
		if _, err := regexp.Compile(cp.Regex); err != nil {
			return errGuardrailCustomPatternInvalidRegex
		}
	}
	return nil
}

func validGuardrailScope(scope string) bool {
	switch scope {
	case "global", "provider", "model", "chain", "apikey":
		return true
	}
	return false
}

type guardrailConfigError string

func (e guardrailConfigError) Error() string { return string(e) }

const (
	errEmptyGuardrailConfig               = guardrailConfigError("config is required")
	errGuardrailConfigJSON                = guardrailConfigError("config must be a JSON object")
	errGuardrailNoDetectors               = guardrailConfigError("config.detectors must list at least one detector or customPatterns must be provided")
	errGuardrailUnknownAction             = guardrailConfigError("config.action must be one of allow, log_only, warn, mask, block")
	errGuardrailUnknownDetector           = guardrailConfigError("config.detectors may only contain pii, injection, and custom")
	errGuardrailCustomPatternEmptyRegex   = guardrailConfigError("custom pattern regex cannot be empty")
	errGuardrailCustomPatternInvalidRegex = guardrailConfigError("custom pattern regex is invalid")
)
