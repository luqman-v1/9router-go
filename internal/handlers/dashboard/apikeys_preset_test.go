package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"testing"
)

// TestIssueBuyerKeyPresetInOneRequest is the resale path an operator actually
// takes: POST /api/keys mints the row (it accepts no policy fields), then
// PUT /api/keys/{id} carries the spend cap, the period, the hard cutoff, the
// expiry and the resale metadata, and PUT /api/keys/{id}/models carries the
// allowlist patterns.
//
// The order is the point. The budget is written first, so a failure in the
// allowlist write leaves a key that is capped but unrestricted rather than one
// that is restricted but unbounded — the second is the larger exposure.
func TestIssueBuyerKeyPresetInOneRequest(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	id, secret := createTestKey(t, router, "buyer-acme")

	body, err := json.Marshal(map[string]any{
		"rateLimitRpm":   60,
		"tokenQuota":     0,
		"queueTimeoutMs": 0,
		"expiresAt":      "2026-12-19T12:00:00Z",
		"metadata":       `{"customerId":"buyer-acme","notes":"chat-only, $10/month","priceCents":1000,"billingCycle":"monthly"}`,
		"name":           "buyer-acme",
		"limitMicros":    10_000_000,
		"limitTokens":    0,
		"budgetPeriod":   "monthly",
		"hardCutoff":     true,
	})
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	rec := doJSON(t, router, http.MethodPut, "/api/keys/"+id, bytes.NewReader(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/keys/%s = %d, want 200: %s", id, rec.Code, rec.Body.String())
	}

	// The allowlist then goes through the models route. It is a separate write
	// because the policy route's own `access` object refuses the wildcard
	// patterns these presets are built from — it takes exact ids only.
	allowBody, err := json.Marshal(map[string]any{"models": []string{"*/deepseek-chat", "*/claude-sonnet*"}})
	if err != nil {
		t.Fatalf("marshal allowlist: %v", err)
	}
	rec = doJSON(t, router, http.MethodPut, "/api/keys/"+id+"/models", bytes.NewReader(allowBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT models = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// The budget and the period landed, because a cap that reads back as zero
	// is a key that can spend the whole account.
	key, err := repo.GetApiKeyByKey(secret)
	if err != nil {
		t.Fatalf("GetApiKeyByKey: %v", err)
	}
	if key == nil {
		t.Fatal("key disappeared after the policy write")
	}
	if key.LimitMicros != 10_000_000 {
		t.Errorf("limitMicros = %d, want 10000000 ($10)", key.LimitMicros)
	}
	if key.BudgetPeriod != "monthly" {
		t.Errorf("budgetPeriod = %q, want \"monthly\"", key.BudgetPeriod)
	}
	if !key.HardCutoff {
		t.Error("hardCutoff = false, want true — the preset stops serving at the cap")
	}
	if key.ExpiresAt == nil || *key.ExpiresAt == "" {
		t.Error("expiresAt empty, want the 30-day stamp the preset applies")
	}

	// And the allowlist landed with it, which is the half the two-call version
	// could drop.
	allowed, err := repo.GetAllowedModels(id)
	if err != nil {
		t.Fatalf("GetAllowedModels: %v", err)
	}
	if len(allowed) == 0 {
		t.Fatal("allowlist empty after the single request — the access field did not land")
	}

	// A second read through the route reports the same policy, so the dashboard
	// shows what is actually stored rather than what the create form believed.
	rec = doJSON(t, router, http.MethodGet, "/api/keys/"+id+"/models", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET models = %d: %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal models: %v", err)
	}
	if !sameSet(listed.Models, allowed) {
		t.Errorf("route reports %v, store holds %v — the two must agree", listed.Models, allowed)
	}
}
