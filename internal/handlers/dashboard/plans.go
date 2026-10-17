package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// Plan CRUD (F-3) — the dashboard surface for reusable policy templates.
//
// This file is a sibling of apikeys.go and apikeys_models.go on purpose: the
// policy columns a key carries are edited in apikeys_policy.go, the templates
// those keys inherit from are edited here, and the two must not collide.
//
// A plan is a TEMPLATE: assigning it to a key gives that key the plan's
// limits as defaults, and the key's own columns still win where they are set.
// Deleting a plan that keys still reference is refused rather than cascaded,
// because a cascade would silently change the limits of every key on it.

// maxPlanNameLen bounds the operator-facing plan name, the same way
// maxAPIKeyNameLen bounds a key's: the name rides along on every listing.
const maxPlanNameLen = 200

// maxPlanDescriptionLen bounds the description. It is display text only.
const maxPlanDescriptionLen = 2000

// maxPlanModels bounds a plan's allowlist with the same 200/256 shape the
// per-key allowlist uses, because both are matched against every candidate
// model on every request.
const maxPlanModels = maxAllowlistEntries

// planRequest is the wire shape of a plan write. Every field is a pointer so
// PUT can distinguish "leave it" from "set it to zero".
type planRequest struct {
	ID               string   `json:"id"`
	Name             *string  `json:"name"`
	Description      *string  `json:"description"`
	LimitMicros      *int64   `json:"limitMicros"`
	LimitTokens      *int64   `json:"limitTokens"`
	RPMLimit         *int     `json:"rpmLimit"`
	TPMLimit         *int     `json:"tpmLimit"`
	ConcurrencyLimit *int     `json:"concurrencyLimit"`
	Period           *string  `json:"period"`
	AlertPct         *int     `json:"alertPct"`
	HardCutoff       *bool    `json:"hardCutoff"`
	AllowedModels    []string `json:"allowedModels"`
}

// planResponse is the wire shape of a plan read.
type planResponse struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	LimitMicros      int64    `json:"limitMicros"`
	LimitTokens      int64    `json:"limitTokens"`
	RPMLimit         int      `json:"rpmLimit"`
	TPMLimit         int      `json:"tpmLimit"`
	ConcurrencyLimit int      `json:"concurrencyLimit"`
	Period           string   `json:"period"`
	AlertPct         int      `json:"alertPct"`
	HardCutoff       bool     `json:"hardCutoff"`
	AllowedModels    []string `json:"allowedModels"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

// HandleListPlans handles GET /api/plans. The default plan is always present,
// so the list is never empty on a bootstrapped database.
func (h *DashboardHandler) HandleListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.Repo.ListPlans("default")
	if err != nil {
		log.Error("plans", "list failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to list plans")
		return
	}
	out := make([]planResponse, 0, len(plans))
	for _, p := range plans {
		out = append(out, planToResponse(p))
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"plans": out})
}

// HandleCreatePlan handles POST /api/plans. The id is generated here, so a
// client cannot collide with an existing plan or pin a reserved name.
func (h *DashboardHandler) HandleCreatePlan(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req planRequest
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	plan, err := h.planFromRequest(req, db.Plan{Period: db.PlanPeriodMonthly})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan.ID = uuid.New().String()
	plan.TenantID = "default"

	if err := h.Repo.CreatePlan(plan); err != nil {
		log.Error("plans", "create failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to create plan")
		return
	}
	stored, err := h.Repo.GetPlan(plan.ID)
	if err != nil {
		log.Error("plans", "read back failed", "error", err, "id", plan.ID)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to read the new plan")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, planToResponse(stored))
}

// HandleGetPlan handles GET /api/plans/{id}.
func (h *DashboardHandler) HandleGetPlan(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing plan id")
		return
	}
	plan, err := h.Repo.GetPlan(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "plan not found")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, planToResponse(plan))
}

// HandleUpdatePlan handles PUT /api/plans/{id} as a partial update: an omitted
// field keeps its stored value rather than being reset to zero.
func (h *DashboardHandler) HandleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing plan id")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	existing, err := h.Repo.GetPlan(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "plan not found")
		return
	}

	var req planRequest
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	plan, err := h.planFromRequest(req, existing)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan.ID = id
	plan.TenantID = existing.TenantID

	// Every field is validated before the first write, so a refused request
	// leaves the row exactly as it was.
	if err := h.Repo.UpdatePlan(plan); err != nil {
		log.Error("plans", "update failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to update plan")
		return
	}
	stored, err := h.Repo.GetPlan(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to read the updated plan")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, planToResponse(stored))
}

// HandleDeletePlan handles DELETE /api/plans/{id}.
//
// A plan that keys still reference is refused with 409 rather than cascaded:
// re-pointing those keys would change their limits without the operator
// asking, and the count lets the dashboard say how many are in the way.
func (h *DashboardHandler) HandleDeletePlan(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing plan id")
		return
	}
	keys, err := h.Repo.CountPlanKeys(id)
	if err != nil {
		log.Error("plans", "count keys failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to count the plan's keys")
		return
	}
	if keys > 0 {
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("%d api key(s) still use this plan", keys),
			"keys":  keys,
		})
		return
	}
	if err := h.Repo.DeletePlan(id); err != nil {
		log.Error("plans", "delete failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to delete plan")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// HandlePlanKeys handles GET /api/plans/{id}/keys — how many keys inherit the
// plan. The dashboard uses it to explain the 409 before the operator hits it.
func (h *DashboardHandler) HandlePlanKeys(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing plan id")
		return
	}
	keys, err := h.Repo.CountPlanKeys(id)
	if err != nil {
		log.Error("plans", "count keys failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to count the plan's keys")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "keys": keys})
}

// planFromRequest merges a partial request onto the stored plan.
//
// base is what an omitted field keeps: a fresh plan's defaults on create, the
// stored row on update. That is what makes PUT partial without a second
// "which fields were sent" pass.
func (h *DashboardHandler) planFromRequest(req planRequest, base db.Plan) (db.Plan, error) {
	plan := base

	if req.Name != nil {
		plan.Name = strings.TrimSpace(*req.Name)
	}
	if plan.Name == "" {
		return db.Plan{}, fmt.Errorf("name is required")
	}
	if len(plan.Name) > maxPlanNameLen {
		return db.Plan{}, fmt.Errorf("name is too long")
	}
	if req.Description != nil {
		plan.Description = strings.TrimSpace(*req.Description)
	}
	if len(plan.Description) > maxPlanDescriptionLen {
		return db.Plan{}, fmt.Errorf("description is too long")
	}

	if req.LimitMicros != nil {
		plan.LimitMicros = *req.LimitMicros
	}
	if req.LimitTokens != nil {
		plan.LimitTokens = *req.LimitTokens
	}
	if req.RPMLimit != nil {
		plan.RPMLimit = *req.RPMLimit
	}
	if req.TPMLimit != nil {
		plan.TPMLimit = *req.TPMLimit
	}
	if req.ConcurrencyLimit != nil {
		plan.ConcurrencyLimit = *req.ConcurrencyLimit
	}
	if req.AlertPct != nil {
		plan.AlertPct = *req.AlertPct
	}
	if req.HardCutoff != nil {
		plan.HardCutoff = *req.HardCutoff
	}
	if req.Period != nil {
		plan.Period = strings.TrimSpace(*req.Period)
	}
	if req.AllowedModels != nil {
		models, err := validateModelAllowlist(cleanModelPatterns(req.AllowedModels))
		if err != nil {
			return db.Plan{}, err
		}
		if len(models) > maxPlanModels {
			return db.Plan{}, fmt.Errorf("too many allowed models (limit %d)", maxPlanModels)
		}
		plan.AllowedModels = models
	}

	if plan.LimitMicros < 0 || plan.LimitTokens < 0 {
		return db.Plan{}, fmt.Errorf("budget limits must be zero (unlimited) or positive")
	}
	if plan.RPMLimit < 0 || plan.TPMLimit < 0 || plan.ConcurrencyLimit < 0 {
		return db.Plan{}, fmt.Errorf("rate limits must be zero (unlimited) or positive")
	}
	// A period with no budget to measure is harmless, but an unknown one would
	// make the budget engine guess a window, so only the four known values pass.
	if !db.IsValidPlanPeriod(plan.Period) {
		return db.Plan{}, fmt.Errorf("period must be one of daily, weekly, monthly, total")
	}
	if plan.AlertPct < 0 || plan.AlertPct > 100 {
		return db.Plan{}, fmt.Errorf("alertPct must be between 0 and 100")
	}
	return plan, nil
}

// planToResponse renders a plan for the dashboard. Timestamps are RFC3339 UTC
// strings, the same shape every other dashboard read uses.
func planToResponse(p db.Plan) planResponse {
	out := planResponse{
		ID:               p.ID,
		Name:             p.Name,
		Description:      p.Description,
		LimitMicros:      p.LimitMicros,
		LimitTokens:      p.LimitTokens,
		RPMLimit:         p.RPMLimit,
		TPMLimit:         p.TPMLimit,
		ConcurrencyLimit: p.ConcurrencyLimit,
		Period:           p.Period,
		AlertPct:         p.AlertPct,
		HardCutoff:       p.HardCutoff,
		AllowedModels:    p.AllowedModels,
		CreatedAt:        p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if out.AllowedModels == nil {
		out.AllowedModels = []string{}
	}
	return out
}
