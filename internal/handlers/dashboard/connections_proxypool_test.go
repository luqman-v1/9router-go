package dashboard

import (
	"context"

	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Upstream parity: PUT /api/providers/{id} accepts a top-level proxyPoolId,
// rejects unknown pools, and unbinds on null/""/"__none__".
func TestHandleUpdateConnection_ProxyPoolBinding(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := repo.RawDB().Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}
	if _, err := repo.RawDB().Exec(
		`INSERT INTO proxyPools (id, isActive, data, createdAt, updatedAt) VALUES (?, 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		"pool-ok", `{"name":"Pool OK","proxyUrl":"http://127.0.0.1:8080"}`,
	); err != nil {
		t.Fatalf("failed to seed proxy pool: %v", err)
	}

	router := setupTestRouter(repo)
	const connID = "conn-proxy-binding"
	if err := repo.CreateProviderConnectionFull(context.Background(), connID, "openai", "apikey", "OpenAI", nil, `{"apiKey":"sk-test"}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// Bind
	if rec := put(`{"proxyPoolId":"pool-ok"}`); rec.Code != http.StatusOK {
		t.Fatalf("bind status = %d: %s", rec.Code, rec.Body.String())
	}
	data := readConnectionData(t, repo, connID)
	if data["proxyPoolId"] != "pool-ok" {
		t.Errorf("top-level proxyPoolId = %v, want pool-ok", data["proxyPoolId"])
	}
	psd, _ := data["providerSpecificData"].(map[string]any)
	if psd == nil || psd["proxyPoolId"] != "pool-ok" {
		t.Errorf("providerSpecificData.proxyPoolId = %v, want pool-ok", psd)
	}
	if data["apiKey"] != "sk-test" {
		t.Errorf("apiKey must be preserved, got %v", data["apiKey"])
	}

	// Unknown pool is rejected and leaves the binding untouched.
	if rec := put(`{"proxyPoolId":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown pool status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var errBody map[string]map[string]any
	_ = json.Unmarshal([]byte(put(`{"proxyPoolId":"nope"}`).Body.String()), &errBody)
	if errBody["error"]["message"] != "Proxy pool not found" {
		t.Errorf("unexpected error payload: %v", errBody)
	}
	if data = readConnectionData(t, repo, connID); data["proxyPoolId"] != "pool-ok" {
		t.Errorf("binding changed after a rejected update: %v", data["proxyPoolId"])
	}

	// Unbind via each accepted sentinel.
	for _, body := range []string{`{"proxyPoolId":null}`, `{"proxyPoolId":""}`, `{"proxyPoolId":"__none__"}`} {
		if rec := put(body); rec.Code != http.StatusOK {
			t.Fatalf("unbind %s status = %d: %s", body, rec.Code, rec.Body.String())
		}
		data = readConnectionData(t, repo, connID)
		if _, exists := data["proxyPoolId"]; exists {
			t.Errorf("top-level proxyPoolId still present after %s: %v", body, data["proxyPoolId"])
		}
		psd, _ = data["providerSpecificData"].(map[string]any)
		if _, exists := psd["proxyPoolId"]; exists {
			t.Errorf("providerSpecificData.proxyPoolId still present after %s", body)
		}

		// Re-bind for the next sentinel.
		if rec := put(`{"proxyPoolId":"pool-ok"}`); rec.Code != http.StatusOK {
			t.Fatalf("re-bind status = %d", rec.Code)
		}
	}
}
