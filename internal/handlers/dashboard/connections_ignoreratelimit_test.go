package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// The edit modal's toggle persists through PUT /api/connections/{id}: turning
// it on writes data.ignoreRateLimit, turning it off removes the field, and an
// edit that does not mention the flag leaves the stored value alone.
func TestHandleUpdateConnection_IgnoreRateLimitToggle(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	const connID = "conn-ignore-rl"
	if err := repo.CreateProviderConnectionFull(connID, "deepseek", "apikey", "DeepSeek", nil, `{"apiKey":"sk-x"}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	put := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	storedIgnore := func(t *testing.T) (bool, map[string]any) {
		t.Helper()
		conn, err := repo.GetProviderConnectionByID(connID)
		if err != nil || conn == nil {
			t.Fatalf("fetch connection: %v", err)
		}
		var dataMap map[string]any
		if err := json.Unmarshal([]byte(conn.Data), &dataMap); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
		v, ok := dataMap["ignoreRateLimit"].(bool)
		return ok && v, dataMap
	}

	if rec := put(t, `{"ignoreRateLimit":true}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT ignoreRateLimit=true: status %d, body %s", rec.Code, rec.Body.String())
	}
	if on, _ := storedIgnore(t); !on {
		t.Error("ignoreRateLimit missing after enabling")
	}

	// An edit that does not mention the flag must keep it.
	if rec := put(t, `{"name":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT rename: status %d", rec.Code)
	}
	if on, _ := storedIgnore(t); !on {
		t.Error("ignoreRateLimit lost across an unrelated edit")
	}

	if rec := put(t, `{"ignoreRateLimit":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT ignoreRateLimit=false: status %d", rec.Code)
	}
	if on, dataMap := storedIgnore(t); on {
		t.Errorf("ignoreRateLimit still present after disabling: %v", dataMap["ignoreRateLimit"])
	}
}

// POST /api/connections must carry the flag into the new row's data blob, so
// the operator can opt out of the cooldown at creation time instead of saving
// first and editing after.
func TestHandleCreateConnection_IgnoreRateLimitSeed(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := httptest.NewRequest(http.MethodPost, "/api/connections",
		bytes.NewReader([]byte(`{"provider":"deepseek","authType":"apikey","name":"seeded","apiKey":"sk-x","ignoreRateLimit":true}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/connections: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("parse created id: %v (body %s)", err, rec.Body.String())
	}

	conn, err := repo.GetProviderConnectionByID(created.ID)
	if err != nil || conn == nil {
		t.Fatalf("fetch created connection: %v", err)
	}
	if !db.ConnectionIgnoresRateLimit(conn.Data) {
		t.Errorf("created row lost ignoreRateLimit: %s", conn.Data)
	}
}
