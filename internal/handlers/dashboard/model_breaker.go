package dashboard

import (
	"net/http"
	"sort"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/modelbreaker"
)

// Per-model circuit breaker status for the dashboard.
//
// The provider breaker above answers "is this provider healthy at all?", which
// is the wrong question when one model in a healthy provider fails
// deterministically: it parks every sibling. This panel reports the per-model
// layer, so an operator sees which model is being held back and why — the same
// request-path state that refuses the request, not a second estimate.
//
// Every status comes from one read of the registry (AllStatuses), so the panel
// costs one call regardless of how many models are tracked, and the sort is
// local to the response.
type ModelBreakerStatusResponse struct {
	Models []modelbreaker.Status `json:"models"`
}

// HandleGetModelBreakers handles GET /api/model-breakers.
func (h *DashboardHandler) HandleGetModelBreakers(w http.ResponseWriter, r *http.Request) {
	statuses := modelbreaker.AllStatuses()
	// Cooling and disabled rows first: an operator opening this panel is
	// looking for what is being held back right now, and a healthy model
	// should not push those off the top. Ties keep the registry's own order,
	// which is stable across calls for the same set.
	sort.SliceStable(statuses, func(i, j int) bool {
		return modelBreakerRank(statuses[i].State) < modelBreakerRank(statuses[j].State)
	})
	handlerutil.WriteJSON(w, http.StatusOK, ModelBreakerStatusResponse{Models: statuses})
}

// modelBreakerRank orders the states by how urgently they need an operator:
// a model that stopped serving first, then one sitting out its cooldown, then
// everything healthy.
func modelBreakerRank(s modelbreaker.State) int {
	switch s {
	case modelbreaker.StateDisabled:
		return 0
	case modelbreaker.StateCooling:
		return 1
	default:
		return 2
	}
}

// HandleResetModelBreaker handles POST /api/model-breakers/{provider}/{model}/reset.
//
// A disabled pair is otherwise stuck until the process restarts: it serves no
// traffic, so it can never record the success that would reopen it. This is the
// operator's way back, and it drops the escalation so the model starts from the
// first cooldown cycle rather than the one that got it disabled.
func (h *DashboardHandler) HandleResetModelBreaker(w http.ResponseWriter, r *http.Request) {
	provider, model := getURLParam(r, "provider"), getURLParam(r, "model")
	if provider == "" || model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "provider and model are required")
		return
	}
	modelbreaker.BreakerFor(provider, model, modelbreaker.Options{}).Reset()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// HandleResetAllModelBreakers handles POST /api/model-breakers/reset.
//
// The bulk form of the single reset, for the case where a provider-wide outage
// parked several models and the operator has already fixed it.
func (h *DashboardHandler) HandleResetAllModelBreakers(w http.ResponseWriter, r *http.Request) {
	modelbreaker.ResetAll()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
