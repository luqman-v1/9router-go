package chat

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/modelbreaker"
)

// oneFailureOptions parks a pair after a single model-scoped failure, so a test
// can reach the disabled state without a fake clock.
var oneFailureOptions = modelbreaker.Options{
	FailureThreshold:       1,
	CooldownBase:           60 * time.Second,
	CooldownMax:            10 * time.Minute,
	CooldownsBeforeDisable: 1,
}

// disablePairTest parks a provider/model pair in the disabled state — the only
// state the admission gate refuses, since a cooling model is allowed again once
// its cooldown elapses. The pair is keyed by the subtest's own model id so two
// subtests never share one registry entry.
func disablePairTest(t *testing.T, provider, model string) {
	t.Helper()
	modelbreaker.ResetAll()
	t.Cleanup(modelbreaker.ResetAll)
	modelbreaker.BreakerFor(provider, model, oneFailureOptions).RecordFailure()
}

// A disabled pair is refused before the upstream is dialled: the gateway
// answers 503 with the Retry-After the breaker reports, and the fake upstream
// never sees a request. That is the whole point of the layer — a model known to
// be failing must not cost a round trip and the quota it would burn.
func TestTryForwardWithConnection_ModelBreakerRefusesDisabledPair(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"done"}}]}`))
	}))
	defer srv.Close()

	provider, model := "deepseek", "breaker-disabled-model"
	disablePairTest(t, provider, model)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, provider, "conn-mb", "sk-mb", srv.URL)

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	err := NewChatHandler(db.NewRepo(database)).tryForwardWithConnection(forwardRequestParams{
		Ctx: context.Background(), W: rec, Provider: provider, Model: model,
		ConnectionID: "conn-mb", ConnData: &ConnectionData{APIKey: "sk-mb", BaseURL: srv.URL},
		Body: body, IsStream: false, TranslateResponse: false, Endpoint: "/v1/chat/completions",
	})
	if err == nil {
		t.Fatal("tryForwardWithConnection() = nil, want the model breaker refusal")
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("tryForwardWithConnection() error = %v, want an *upstreamError", err)
	}
	if ue.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want %d", ue.StatusCode, http.StatusServiceUnavailable)
	}
	// A disabled model carries no Retry-After: it does not come back on a timer,
	// so a wait would tell the client to retry something that stays refused
	// until an operator lifts it. The body names the condition instead.
	if got := ue.Header.Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want empty for a disabled pair", got)
	}
	if !ue.Local {
		t.Error("Local = false, want true: the credential was never dialled")
	}
	if !modelBreakerRefusal(ue) {
		t.Errorf("body does not name the breaker: %s", ue.Body)
	}
	if hits != 0 {
		t.Errorf("upstream was dialled %d times, want 0 — the gate must refuse before dispatch", hits)
	}
}

// A probe answers what the model does right now, not what the breaker thinks of
// it: the dashboard's Test button exists to report a failure the operator can
// read, and "cooling down" would describe this layer instead of the model.
func TestTryForwardWithConnection_ModelBreakerProbeContextBypassesGate(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"done"}}]}`))
	}))
	defer srv.Close()

	provider, model := "deepseek", "breaker-probe-model"
	disablePairTest(t, provider, model)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, provider, "conn-mbp", "sk-mbp", srv.URL)

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	ctx := handlerutil.WithProbeContext(context.Background())
	err := NewChatHandler(db.NewRepo(database)).tryForwardWithConnection(forwardRequestParams{
		Ctx: ctx, W: rec, Provider: provider, Model: model,
		ConnectionID: "conn-mbp", ConnData: &ConnectionData{APIKey: "sk-mbp", BaseURL: srv.URL},
		Body: body, IsStream: false, TranslateResponse: false, Endpoint: "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("tryForwardWithConnection() on a probe context = %v, want nil", err)
	}
	if hits != 1 {
		t.Errorf("upstream was dialled %d times, want 1", hits)
	}
}

// A disabled pair is not charged further: RecordFailure on a disabled breaker
// is a no-op by design, so the breaker this test parked must still be disabled
// after the refused attempt rather than silently cycling through cooldowns.
func TestTryForwardWithConnection_ModelBreakerRefusalLeavesPairDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"done"}}]}`))
	}))
	defer srv.Close()

	provider, model := "deepseek", "breaker-stays-disabled"
	disablePairTest(t, provider, model)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, provider, "conn-mbs", "sk-mbs", srv.URL)

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	params := forwardRequestParams{
		Ctx: context.Background(), W: rec, Provider: provider, Model: model,
		ConnectionID: "conn-mbs", ConnData: &ConnectionData{APIKey: "sk-mbs", BaseURL: srv.URL},
		Body: body, IsStream: false, TranslateResponse: false, Endpoint: "/v1/chat/completions",
	}
	_ = NewChatHandler(db.NewRepo(database)).tryForwardWithConnection(params)
	if got := modelbreaker.BreakerFor(provider, model, oneFailureOptions).State(); got != modelbreaker.StateDisabled {
		t.Errorf("State() after the refusal = %q, want %q", got, modelbreaker.StateDisabled)
	}
}

// Only a model-scoped failure is charged. A 404 names a missing model, so it
// counts; a 429 is the account's quota and belongs to the rotation layer, so it
// must leave the breaker closed — otherwise one throttled account would park a
// model that every other connection serves fine.
func TestModelBreakerFailureChargesOnlyModelScope(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantCharged bool
	}{
		{"404 names a missing model", http.StatusNotFound, `{"error":{"message":"model gpt-5-mini not found","type":"invalid_request_error"}}`, true},
		{"typed model_not_found", http.StatusNotFound, `{"error":{"message":"gone","type":"model_not_found"}}`, true},
		{"429 is the account's quota", http.StatusTooManyRequests, `{"error":{"message":"Too Many Requests","type":"rate_limit_error"}}`, false},
		{"503 is the provider's outage", http.StatusServiceUnavailable, `{"error":{"message":"overloaded","type":"server_error"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modelbreaker.ResetAll()
			t.Cleanup(modelbreaker.ResetAll)
			// A breaker keeps the options it was created with, so the gate's
			// own policy is what both the charge and the assertion below use —
			// a second option set would silently create a different breaker.
			modelBreakerOptions = modelbreaker.Options{
				FailureThreshold:       2,
				CooldownBase:           60 * time.Second,
				CooldownMax:            10 * time.Minute,
				CooldownsBeforeDisable: 1,
			}
			t.Cleanup(func() { modelBreakerOptions = modelbreaker.Options{} })
			provider, model := "openai", "charge-model-"+t.Name()
			b := modelbreaker.BreakerFor(provider, model, modelBreakerOptions)
			// The failure under test, plus exactly one more: with a threshold
			// of 2, the counted failures decide the state on their own. A
			// dropped failure leaves the count at 1 — still serving — while a
			// charged one brings it to 2 and disables the pair.
			modelBreakerFailure(provider, model, tt.status, []byte(tt.body))
			b.RecordFailure()
			if charged := b.State() == modelbreaker.StateDisabled; charged != tt.wantCharged {
				t.Errorf("failure charged = %t (state %q), want %t", charged, b.State(), tt.wantCharged)
			}
		})
	}
}

// modelBreakerRefusal reports whether an error body names the breaker, so a
// caller (and a test) can tell "this model is out of rotation" from a 503 the
// upstream produced.
func modelBreakerRefusal(ue *upstreamError) bool {
	return ue != nil && strings.Contains(string(ue.Body), "model_cooling_down")
}

// The refusal must be parseable JSON, not merely contain the code.
//
// The bug this pins: the body was two concatenated raw strings split between
// `"message":…,` and `"type":…`, which emits `""type"`. That reads as a
// deliberate marker to a substring check — the old assertion used
// strings.Contains("model_cooling_down") and stayed green — while every client
// that decodes the 503 envelope gets "invalid character 't' after object key".
// Only unmarshalling the whole envelope catches it.
func TestModelBreakerUnavailableBodyIsParseableJSON(t *testing.T) {
	ue := modelBreakerUnavailable(45 * time.Second)

	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(ue.Body, &envelope); err != nil {
		t.Fatalf("refusal body is not parseable JSON: %v\nbody: %s", err, ue.Body)
	}
	if envelope.Error.Code != "model_cooling_down" {
		t.Errorf("code = %q, want model_cooling_down", envelope.Error.Code)
	}
	if envelope.Error.Type == "" {
		t.Error("type is empty — the key was dropped or mangled, so a client cannot classify the failure")
	}
	if envelope.Error.Message == "" {
		t.Error("message is empty — the operator-facing reason is gone")
	}
	// Retry-After rides the header in seconds, and a disabled pair carries none.
	if got := ue.Header.Get("Retry-After"); got != "45" {
		t.Errorf("Retry-After = %q, want 45", got)
	}
	if got := modelBreakerUnavailable(0).Header.Get("Retry-After"); got != "" {
		t.Errorf("Retry-After on a timerless refusal = %q, want empty", got)
	}
}
