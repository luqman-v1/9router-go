package chat

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// Upstream (src/sse/services/auth.js:86-89) skips a codex account whose
// providerSpecificData.enabledModels is a non-empty array that does not contain
// the requested model. Without that gate a rotation sweep hands the request to
// an account that cannot serve it and OpenAI answers 400.

const (
	codexAstra = "gpt-6-astra"
	codexLuna  = "gpt-6-luna"
)

// codexConnData renders a codex connection blob, writing enabledModels under
// providerSpecificData (the key upstream reads) or at the top level (the older
// dashboard writer — both shapes exist in the wild).
func codexConnData(id, dataKey string, enabled []string, baseURL string) string {
	blob := map[string]any{"accessToken": "tok-" + id}
	if baseURL != "" {
		blob["baseUrl"] = baseURL
	}
	if dataKey == "providerSpecificData" {
		blob["providerSpecificData"] = map[string]any{"enabledModels": enabled}
	} else {
		blob["enabledModels"] = enabled
	}
	d, _ := json.Marshal(blob)
	return string(d)
}

// seedCodexConn inserts a codex connection pinned to the given enabledModels.
func seedCodexConn(t *testing.T, database *sql.DB, id, dataKey string, enabled []string, baseURL string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES (?, 'codex', 'oauth', ?, 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		id, id, codexConnData(id, dataKey, enabled, baseURL)); err != nil {
		t.Fatalf("seed codex connection %s: %v", id, err)
	}
}

// clearSeededConns drops the rows setupChatTestDB preloads so a test sees only
// what it seeds itself.
func clearSeededConns(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1','conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
}

// TestGetBestConnection_CodexEnabledModelsPickOnlyEligibleAccount proves the
// picker never answers with an account that has the model disabled.
func TestGetBestConnection_CodexEnabledModelsPickOnlyEligibleAccount(t *testing.T) {
	tests := []struct {
		name    string
		dataKey string
	}{
		{"nested providerSpecificData", "providerSpecificData"},
		{"top-level enabledModels", "top"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			clearSeededConns(t, database)
			// conn-cx-a is listed first (priority 1, newer updatedAt) and has
			// no Luna at all, so it is the row the picker would reach first.
			seedCodexConn(t, database, "conn-cx-a", tt.dataKey, []string{codexAstra}, "")
			seedCodexConn(t, database, "conn-cx-b", tt.dataKey, []string{codexAstra, codexLuna}, "")

			h := NewChatHandler(db.NewRepo(database))
			conn, _, err := h.getBestConnection("codex", "", nil, codexLuna)
			if err != nil {
				t.Fatalf("getBestConnection: %v", err)
			}
			if conn.ID != "conn-cx-b" {
				t.Errorf("picked %s; only the account with %s enabled may serve it", conn.ID, codexLuna)
			}
		})
	}
}

// TestGetBestConnection_CodexEnabledModelsEmptyListLeavesAccountEligible: an
// absent or empty list means "no restriction", so the account stays eligible
// for every model.
func TestGetBestConnection_CodexEnabledModelsEmptyListLeavesAccountEligible(t *testing.T) {
	tests := []struct {
		name    string
		enabled []string
	}{
		{"absent list", nil},
		{"empty list", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			clearSeededConns(t, database)
			seedCodexConn(t, database, "conn-cx-a", "providerSpecificData", tt.enabled, "")

			h := NewChatHandler(db.NewRepo(database))
			conn, _, err := h.getBestConnection("codex", "", nil, codexLuna)
			if err != nil {
				t.Fatalf("getBestConnection: %v", err)
			}
			if conn.ID != "conn-cx-a" {
				t.Errorf("picked %s, want conn-cx-a (an unpinned account stays eligible)", conn.ID)
			}
		})
	}
}

// TestPinnedConnectionIneligible_CodexEnabledModels: a client pin
// (x-connection-id) must be ignored for an account that has the model
// disabled, exactly like a model-locked row, so the request falls through to
// the configured strategy.
func TestPinnedConnectionIneligible_CodexEnabledModels(t *testing.T) {
	tests := []struct {
		name           string
		conn           *models.ProviderConnection
		wantIneligible bool
	}{
		{
			name:           "model absent from the account's list",
			conn:           &models.ProviderConnection{ID: "c1", Provider: "codex", IsActive: 1, Data: `{"providerSpecificData":{"enabledModels":["gpt-6-astra"]}}`},
			wantIneligible: true,
		},
		{
			name:           "model present in the account's list",
			conn:           &models.ProviderConnection{ID: "c1", Provider: "codex", IsActive: 1, Data: `{"providerSpecificData":{"enabledModels":["gpt-6-astra","gpt-6-luna"]}}`},
			wantIneligible: false,
		},
		{
			name:           "account without an enabledModels list",
			conn:           &models.ProviderConnection{ID: "c1", Provider: "codex", IsActive: 1, Data: `{"accessToken":"tok"}`},
			wantIneligible: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, cleanup := setupHandlerForForward(t)
			defer cleanup()

			ineligible, reason := h.pinnedConnectionIneligible(tt.conn, nil, codexLuna)
			if ineligible != tt.wantIneligible {
				t.Fatalf("ineligible = %v (%s), want %v", ineligible, reason, tt.wantIneligible)
			}
		})
	}
}

// TestHandleAccountFallback_CodexRotationSkipsIneligibleAccount proves the
// request reaches the account that holds the model: under round-robin both
// requests must land on conn-cx-b, never on the account without it.
func TestHandleAccountFallback_CodexRotationSkipsIneligibleAccount(t *testing.T) {
	var served []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("fake upstream got invalid JSON: %v", err)
		}
		served = append(served, req.Model)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	clearSeededConns(t, database)
	seedCodexConn(t, database, "conn-cx-a", "providerSpecificData", []string{codexAstra}, "")
	// conn-cx-b is the only account holding the model and points at the fake.
	seedCodexConn(t, database, "conn-cx-b", "providerSpecificData", []string{codexAstra, codexLuna}, srv.URL+"/v1/responses")

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	if err := repo.SetProviderStrategy("codex", db.ProviderStrategy{RotateStrategy: "round-robin"}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	body := []byte(`{"model":"` + codexLuna + `","input":[{"role":"user","content":"hi"}]}`)
	for i := range 2 {
		rec := httptest.NewRecorder()
		if err := h.handleAccountFallback(context.Background(), rec, "codex", codexLuna, "", body, false, false, "/v1/responses"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}

	if len(served) != 2 {
		t.Fatalf("upstream saw %d requests, want 2 (%v)", len(served), served)
	}
	for i, model := range served {
		if model != codexLuna {
			t.Fatalf("request %d was sent to upstream as %q; the only account holding %s must serve it", i+1, model, codexLuna)
		}
	}
}

// TestFilterConnectionsByEnabledModels_OtherProvidersUntouched pins the scope:
// the pin is a codex-only concept, no other provider's rows are read for it.
func TestFilterConnectionsByEnabledModels_OtherProvidersUntouched(t *testing.T) {
	conns := []*models.ProviderConnection{
		{ID: "ds-1", Provider: "deepseek", Data: `{"enabledModels":["deepseek-chat"]}`},
		{ID: "cx-1", Provider: "codex", Data: `{"enabledModels":["gpt-6-astra"]}`},
	}
	if got := filterConnectionsByEnabledModels("deepseek", conns, codexLuna); len(got) != 2 {
		t.Errorf("deepseek candidates = %d, want 2: the codex-only pin must not filter another provider", len(got))
	}
	if got := filterConnectionsByEnabledModels("codex", conns, ""); len(got) != 2 {
		t.Errorf("no requested model must leave the candidates untouched, got %d", len(got))
	}
}

// TestGetBestConnection_CodexNoAccountHasModel surfaces the operator mistake as
// an error naming the model instead of forwarding to an account that cannot
// serve it.
func TestGetBestConnection_CodexNoAccountHasModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	clearSeededConns(t, database)
	seedCodexConn(t, database, "conn-cx-a", "providerSpecificData", []string{codexAstra}, "")

	h := NewChatHandler(db.NewRepo(database))
	_, _, err := h.getBestConnection("codex", "", nil, codexLuna)
	if err == nil {
		t.Fatal("expected an error when no account has the model enabled")
	}
	if !strings.Contains(err.Error(), codexLuna) {
		t.Errorf("error %q must name the unservable model", err)
	}
}
