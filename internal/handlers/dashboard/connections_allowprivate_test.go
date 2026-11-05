package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleCreateConnection_PersistsAllowPrivateNetworking(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	body := `{
		"provider": "deepseek",
		"authType": "apikey",
		"name": "Local Gateway",
		"apiKey": "sk-local",
		"allowPrivateNetworking": true
	}`
	rec := postConnection(t, router, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("expected created connection id")
	}

	conn, err := repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		t.Fatalf("failed to reload connection: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("decode connection data: %v", err)
	}
	if on, _ := data["allowPrivateNetworking"].(bool); !on {
		t.Errorf("expected allowPrivateNetworking=true stored, got %v", data["allowPrivateNetworking"])
	}
}

func TestHandleUpdateConnection_AllowPrivateNetworkingPersistence(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	connID := "conn-gw-toggle"
	if err := repo.CreateProviderConnection(connID, "deepseek", "apikey", "Local Gateway", "sk-local"); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	// Turn the toggle on.
	body := `{"allowPrivateNetworking": true}`
	req := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil || conn == nil {
		t.Fatalf("failed to fetch updated connection: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("failed to unmarshal connection data: %v", err)
	}
	if on, _ := data["allowPrivateNetworking"].(bool); !on {
		t.Errorf("expected allowPrivateNetworking=true after update, got %v", data["allowPrivateNetworking"])
	}

	// Turn it back off.
	body2 := `{"allowPrivateNetworking": false}`
	req2 := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body2)))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 turning it off, got %d: %s", w2.Code, w2.Body.String())
	}

	conn2, err := repo.GetProviderConnectionByID(connID)
	if err != nil || conn2 == nil {
		t.Fatalf("failed to fetch updated connection: %v", err)
	}
	var data2 map[string]any
	if err := json.Unmarshal([]byte(conn2.Data), &data2); err != nil {
		t.Fatalf("failed to unmarshal connection data: %v", err)
	}
	if on, _ := data2["allowPrivateNetworking"].(bool); on {
		t.Errorf("expected allowPrivateNetworking=false after update, got %v", data2["allowPrivateNetworking"])
	}
}
