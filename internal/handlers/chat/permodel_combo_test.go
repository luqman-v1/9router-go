package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

// A per-model cap must bind on the COMBO path, not only on the single-provider
// fallback: the reviewer's replay seeded limit=1 on a custom-provider combo
// member, called the combo twice, and both requests dialled the capped model
// because admission lived only in handleAccountFallback. Admission now lives in
// tryForwardWithConnection, which every dispatch path calls, so the second
// combo request must fail over to member B.
func TestPerModelCapBindsOnComboDispatch(t *testing.T) {
	var cappedHits, spareHits atomic.Int32
	cappedUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cappedHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-capped"}}]}`))
	}))
	defer cappedUpstream.Close()
	spareUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spareHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-spare"}}]}`))
	}))
	defer spareUpstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM combos`); err != nil {
		t.Fatalf("clear combos: %v", err)
	}

	repo := db.NewRepo(database)
	seedAvailabilityConnection(t, repo, "conn-capped", "openai-compatible-bench", 1, cappedUpstream.URL)
	seedAvailabilityConnection(t, repo, "conn-spare", "openai-compatible-spare", 1, spareUpstream.URL)

	comboModels, _ := json.Marshal([]string{"openai-compatible-bench/hot-model", "openai-compatible-spare/cool-model"})
	if _, err := database.Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c-pm', 'c-pm', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels),
	); err != nil {
		t.Fatalf("seed combo: %v", err)
	}
	_ = repo.SetComboStrategy("c-pm", db.ComboStrategy{Strategy: "fallback"})

	// limit=1 on the hot model: the first combo turn may dial it, the second
	// must fail over to the spare member instead of dialling it again.
	if err := repo.UpdateSettingsRaw(map[string]any{
		"perModelRateLimit": map[string]any{
			"enabled": true, "limit": 1, "windowMs": 60000,
			"overrides": map[string]any{
				"openai-compatible-bench/hot-model": map[string]any{"limit": 1},
			},
		},
	}); err != nil {
		t.Fatalf("seed per-model cap: %v", err)
	}

	h := NewChatHandler(repo)
	call := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.handleComboFallback(context.Background(), rec,
			[]byte(`{"model":"c-pm","messages":[{"role":"user","content":"hi"}]}`),
			[]string{"openai-compatible-bench/hot-model", "openai-compatible-spare/cool-model"},
			"fallback", false, false, "c-pm", 0)
		return rec
	}

	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("first combo turn: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cappedHits.Load() != 1 {
		t.Fatalf("first turn capped hits = %d, want 1", cappedHits.Load())
	}

	rec := call()
	if rec.Code != http.StatusOK {
		t.Fatalf("second combo turn: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cappedHits.Load() != 1 {
		t.Errorf("second turn dialled the capped model again: capped=%d — the cap must bind on the combo path", cappedHits.Load())
	}
	if spareHits.Load() != 1 {
		t.Errorf("second turn spare hits = %d, want 1 (failover to the next member)", spareHits.Load())
	}
}
