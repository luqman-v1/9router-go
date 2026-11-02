package dashboard

import (
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/resilience"
)

// Provider-level resilience status for the dashboard.
//
// The numbers come from the same in-memory layer the request path consults, so
// what an operator sees here is what a request would have been judged against —
// not a second estimate that can drift from the gate.
type ResilienceStatusResponse struct {
	Breakers  []BreakerStatusResponse `json:"breakers"`
	Semaphore []SemaphoreStatResponse `json:"semaphore"`
}

// BreakerStatusResponse is one provider's circuit breaker snapshot.
type BreakerStatusResponse struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	FailureCount int    `json:"failureCount"`
	SuccessCount int    `json:"successCount"`
	RetryAfterMs int64  `json:"retryAfterMs"`
	OpenedAt     string `json:"openedAt,omitempty"`
}

// SemaphoreStatResponse is one account gate snapshot.
type SemaphoreStatResponse struct {
	Key            string `json:"key"`
	Running        int    `json:"running"`
	Queued         int    `json:"queued"`
	MaxConcurrency int    `json:"maxConcurrency"`
	BlockedUntil   string `json:"blockedUntil,omitempty"`
}

// HandleGetResilience handles GET /api/resilience.
//
// Answers "is any provider being held back right now, and is any account
// queuing?" — the question an operator asks when throughput drops and the
// per-provider error rate looks normal. Without this the breaker is invisible:
// requests simply fail with a 503 and nothing says the gateway stopped dialling
// a provider on purpose.
func (h *DashboardHandler) HandleGetResilience(w http.ResponseWriter, r *http.Request) {
	resp := ResilienceStatusResponse{
		Breakers:  []BreakerStatusResponse{},
		Semaphore: []SemaphoreStatResponse{},
	}
	if h.Resilience != nil {
		breakers, gates := h.Resilience.Statuses()
		resp.Breakers = make([]BreakerStatusResponse, 0, len(breakers))
		for _, b := range breakers {
			resp.Breakers = append(resp.Breakers, BreakerStatusResponse{
				Name:         b.Name,
				State:        string(b.State),
				FailureCount: b.FailureCount,
				SuccessCount: b.SuccessCount,
				RetryAfterMs: b.RetryAfterMs,
				OpenedAt:     b.OpenedAt,
			})
		}
		resp.Semaphore = make([]SemaphoreStatResponse, 0, len(gates))
		for _, g := range gates {
			resp.Semaphore = append(resp.Semaphore, SemaphoreStatResponse(g))
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleResetResilience handles POST /api/resilience/reset.
//
// Closes every breaker and clears every gate. Used when an operator has fixed a
// provider and does not want to wait out the reset timeout, and by tests.
func (h *DashboardHandler) HandleResetResilience(w http.ResponseWriter, r *http.Request) {
	if h.Resilience == nil {
		handlerutil.WriteJSONError(w, http.StatusNotImplemented, "resilience layer not wired")
		return
	}
	h.Resilience.Reset()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ensure the resilience package stays a direct dependency of this handler: the
// field below is what the two endpoints read, and a build that drops the import
// should fail here rather than at the call site.
var _ = resilience.DefaultController
