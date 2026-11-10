package dashboard

import (
	"net/http"
	"time"

	"9router/proxy/internal/budget"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/policy"
)

// HandleGetApiKeyBudget handles GET /api/keys/{id}/budget.
//
// It answers "how much of this key's period budget is spent right now?" for
// the dashboard: the resolved budget (plan first, then the key's own columns —
// the same precedence the request path enforces), the spend measured in the
// current window, the remaining headroom, the engine's state, and when the
// window rolls.
//
// The spend read is the exact statement the middleware runs per request
// (budget.SpendFor over the window), so the number the operator sees and the
// number that triggers a 402 are the same one: a key reported "ok" is being
// served, a key reported "exhausted" is being refused.
func (h *DashboardHandler) HandleGetApiKeyBudget(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing apiKey id")
		return
	}
	key, err := h.Repo.GetApiKeyByID(id)
	if err != nil {
		log.Error("apikeys", "budget read: look up key failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to read api key")
		return
	}
	if key == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "api key not found")
		return
	}
	// The resolved limits come from the same resolution the limiter uses:
	// global default has no budget axis, so this is plan-then-key, exactly as
	// limitsFor builds it.
	pol := policy.Resolve(h.Repo, key)
	limits := budget.Limits{
		LimitMicros: pol.LimitMicros,
		LimitTokens: pol.LimitTokens,
		Period:      pol.Period,
		AlertPct:    pol.AlertPct,
		HardCutoff:  pol.HardCutoff,
	}
	// A row whose secret was hashed away (#199) cannot be attributed in the
	// ledger: the spend read matches on the raw key and its masked form, and
	// a hashed row's key column is the sentinel, not a secret the client ever
	// sends. Reporting a zero spend would read as "fresh key" for a key that
	// has spent its budget, so the endpoint says what it cannot measure
	// instead of inventing a number. IsHashedKeyRow is the same predicate the
	// rest of the dashboard uses for this shape.
	if db.IsHashedKeyRow(key) {
		handlerutil.WriteJSON(w, http.StatusOK, budgetReadPayload(id, limits, budget.Assessment{Limits: limits},
			"the key's secret is hashed and cannot be attributed in the usage ledger; period spend is not measured for this key"))
		return
	}
	a, err := budget.Assess(r.Context(), h.Repo, key.Key, limits, time.Now())
	if err != nil {
		log.Error("apikeys", "budget read: measure spend failed", "error", err, "id", id)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to measure period spend")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, budgetReadPayload(id, limits, a, ""))
}

// budgetReadPayload shapes one read into the body the dashboard renders.
// Remaining is always present (zero when the axis is unset or spent) so the
// UI does not have to special-case a missing field. An unarmed key reports the
// ok status rather than an empty one: it is never refused, so "ok" is the
// truth, and the dashboard renders one status vocabulary for every key.
func budgetReadPayload(id string, limits budget.Limits, a budget.Assessment, note string) map[string]any {
	status := string(a.Status)
	if status == "" {
		status = string(budget.StatusOK)
	}
	body := map[string]any{
		"id":              id,
		"limitMicros":     limits.LimitMicros,
		"limitTokens":     limits.LimitTokens,
		"period":          orDefaultPeriod(limits.Period),
		"alertPct":        limits.AlertPct,
		"hardCutoff":      limits.HardCutoff,
		"armed":           limits.Armed(),
		"spentMicros":     a.Spend.Micros,
		"spentTokens":     a.Spend.Tokens,
		"remainingMicros": a.RemainingMicros,
		"remainingTokens": a.RemainingTokens,
		"resetAt":         a.ResetAt,
		"status":          status,
	}
	if note != "" {
		body["note"] = note
	}
	return body
}

// orDefaultPeriod reports the window the engine actually uses: an empty
// period resolves to monthly, and the dashboard must show what will be
// enforced rather than what was stored.
func orDefaultPeriod(p string) string {
	if p == "" {
		return budget.PeriodMonthly
	}
	return p
}
