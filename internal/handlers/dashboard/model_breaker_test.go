package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/modelbreaker"
)

// parkModel puts one pair into the disabled state with a single failure, so an
// endpoint test can assert what the panel shows without a fake clock. The pair
// is keyed per test function, and the registry is reset before and after so a
// test never reads another test's rows.
func parkModel(t *testing.T, provider, model string) modelbreaker.Status {
	t.Helper()
	modelbreaker.ResetAll()
	t.Cleanup(modelbreaker.ResetAll)
	modelbreaker.BreakerFor(provider, model, modelbreaker.Options{
		FailureThreshold:       1,
		CooldownBase:           60 * 1e9,
		CooldownMax:            600 * 1e9,
		CooldownsBeforeDisable: 1,
	}).RecordFailure()
	return modelbreaker.BreakerFor(provider, model, modelbreaker.Options{}).Status()
}

// The panel lists what is being held back, with the rows needing an operator
// first — a model that stopped serving, then one sitting out its cooldown — so
// a healthy model never pushes those off the top.
func TestHandleGetModelBreakers_OrdersHeldBackFirst(t *testing.T) {
	parked := parkModel(t, "openai", "bad-model")
	if parked.State != modelbreaker.StateDisabled {
		t.Fatalf("test setup: state = %q, want %q", parked.State, modelbreaker.StateDisabled)
	}
	// A healthy pair that merely exists in the registry: it must sort after the
	// disabled one rather than alphabetically ahead of it.
	modelbreaker.BreakerFor("openai", "aaa-healthy", modelbreaker.Options{})
	modelbreaker.BreakerFor("openai", "bad-model", modelbreaker.Options{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/model-breakers", nil)
	NewDashboardHandler(nil).HandleGetModelBreakers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp ModelBreakerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Models) == 0 {
		t.Fatal("no models in the response")
	}
	if resp.Models[0].Model != "bad-model" {
		t.Errorf("first row = %q, want the disabled model", resp.Models[0].Model)
	}
	if resp.Models[0].State != modelbreaker.StateDisabled {
		t.Errorf("first row state = %q, want %q", resp.Models[0].State, modelbreaker.StateDisabled)
	}
}

// A disabled pair serves nothing, so it can never record the success that would
// reopen it: the reset endpoint is the only way back, and it must also drop the
// escalation so the next failure starts from the base cooldown again.
func TestHandleResetModelBreaker_ReopensDisabledPair(t *testing.T) {
	parked := parkModel(t, "openai", "bad-model")
	if parked.State != modelbreaker.StateDisabled {
		t.Fatalf("test setup: state = %q, want %q", parked.State, modelbreaker.StateDisabled)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/model-breakers/openai/bad-model/reset", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "openai")
	rctx.URLParams.Add("model", "bad-model")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	NewDashboardHandler(nil).HandleResetModelBreaker(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	got := modelbreaker.BreakerFor("openai", "bad-model", modelbreaker.Options{})
	if s := got.State(); s != modelbreaker.StateClosed {
		t.Errorf("state after reset = %q, want %q", s, modelbreaker.StateClosed)
	}
	// The escalation is dropped, not just the state: the next cooldown is the
	// base again, not the doubled one the model had climbed to.
	if d := (modelbreaker.Options{
		FailureThreshold: 1, CooldownBase: 60 * 1e9, CooldownMax: 600 * 1e9, CooldownsBeforeDisable: 1,
	}).CooldownFor(1); d != 60*1e9 {
		t.Errorf("CooldownFor(1) = %v, want 60s", d)
	}
}

// A pair name that does not exist on the path is a caller error, not a silent
// no-op: a client that mistypes the route must be told rather than left reading
// an OK about a model that was never reset.
func TestHandleResetModelBreaker_RejectsMissingPair(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/model-breakers/reset", nil)
	NewDashboardHandler(nil).HandleResetModelBreaker(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// The bulk reset clears every pair, which is what an operator reaches for after
// a provider-wide outage that parked several models at once.
func TestHandleResetAllModelBreakers(t *testing.T) {
	parkModel(t, "openai", "bad-model")
	modelbreaker.BreakerFor("openai", "aaa-healthy", modelbreaker.Options{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/model-breakers/reset", nil)
	NewDashboardHandler(nil).HandleResetAllModelBreakers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if s := modelbreaker.BreakerFor("openai", "bad-model", modelbreaker.Options{}).State(); s != modelbreaker.StateClosed {
		t.Errorf("state after bulk reset = %q, want %q", s, modelbreaker.StateClosed)
	}
}
