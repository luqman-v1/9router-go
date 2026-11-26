package dashboard

import (
	"context"

	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleUpdateConnection_AssignedModel(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Create initial connection with apiKey and existing providerSpecificData
	connID := "conn-test-update-1"
	if err := repo.CreateProviderConnection(context.Background(), connID, "anthropic", "apikey", "Anthropic Conn", "sk-ant-secret"); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	initialData := `{
		"apiKey": "sk-ant-secret",
		"authToken": "bearer-token-123",
		"providerSpecificData": {
			"existingConfig": "preserved-value"
		}
	}`
	if err := repo.UpdateConnectionData(context.Background(), connID, initialData); err != nil {
		t.Fatalf("failed to initialize connection data: %v", err)
	}

	// 1. Update with only { "assignedModel": "claude-3-7-sonnet" }
	body1 := `{"assignedModel": "claude-3-7-sonnet"}`
	req1 := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body1)))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w1.Code, w1.Body.String())
	}

	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("failed to fetch updated connection: %v", err)
	}
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &dataMap); err != nil {
		t.Fatalf("failed to unmarshal connection data: %v", err)
	}

	if dataMap["apiKey"] != "sk-ant-secret" {
		t.Errorf("expected apiKey to be preserved as sk-ant-secret, got %v", dataMap["apiKey"])
	}
	if dataMap["authToken"] != "bearer-token-123" {
		t.Errorf("expected authToken to be preserved, got %v", dataMap["authToken"])
	}
	if dataMap["assignedModel"] != "claude-3-7-sonnet" {
		t.Errorf("expected assignedModel to be claude-3-7-sonnet, got %v", dataMap["assignedModel"])
	}
	psd, ok := dataMap["providerSpecificData"].(map[string]any)
	if !ok || psd["existingConfig"] != "preserved-value" {
		t.Errorf("expected existingConfig in providerSpecificData to be preserved, got %v", psd)
	}

	// 2. Update with { "name": "...", "priority": 1, "isActive": true, "providerSpecificData": { "assignedModel": "claude-3-opus" } }
	body2 := `{
		"name": "Updated Name",
		"priority": 2,
		"isActive": true,
		"providerSpecificData": {
			"assignedModel": "claude-3-opus",
			"newOption": "new-val"
		}
	}`
	req2 := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body2)))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w2.Code, w2.Body.String())
	}

	conn, err = repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("failed to fetch connection: %v", err)
	}
	if conn.Name == nil || *conn.Name != "Updated Name" {
		t.Errorf("expected updated name, got %v", conn.Name)
	}
	if conn.Priority == nil || *conn.Priority != 2 {
		t.Errorf("expected priority 2, got %v", conn.Priority)
	}
	dataMap = nil
	if err := json.Unmarshal([]byte(conn.Data), &dataMap); err != nil {
		t.Fatalf("failed to unmarshal connection data: %v", err)
	}
	if dataMap["apiKey"] != "sk-ant-secret" {
		t.Errorf("expected apiKey to be preserved, got %v", dataMap["apiKey"])
	}
	psd = dataMap["providerSpecificData"].(map[string]any)
	if psd["existingConfig"] != "preserved-value" {
		t.Errorf("expected existingConfig preserved in providerSpecificData, got %v", psd["existingConfig"])
	}
	if psd["assignedModel"] != "claude-3-opus" {
		t.Errorf("expected assignedModel claude-3-opus in providerSpecificData, got %v", psd["assignedModel"])
	}
	if psd["newOption"] != "new-val" {
		t.Errorf("expected newOption in providerSpecificData, got %v", psd["newOption"])
	}

	// 3. Test routing via PUT /api/providers/{id}
	body3 := `{"assignedModel": "claude-3-5-haiku"}`
	req3 := httptest.NewRequest(http.MethodPut, "/api/providers/"+connID, bytes.NewReader([]byte(body3)))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("expected status 200 for PUT /api/providers/{id}, got %d: %s", w3.Code, w3.Body.String())
	}

	conn, err = repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("failed to fetch connection: %v", err)
	}
	dataMap = nil
	_ = json.Unmarshal([]byte(conn.Data), &dataMap)
	if dataMap["assignedModel"] != "claude-3-5-haiku" {
		t.Errorf("expected assignedModel claude-3-5-haiku after PUT /api/providers/{id}, got %v", dataMap["assignedModel"])
	}
	if dataMap["apiKey"] != "sk-ant-secret" {
		t.Errorf("expected apiKey to be preserved, got %v", dataMap["apiKey"])
	}

	// 4. Test update with { "data": { "assignedModel": "claude-3-sonnet" } }
	body4 := `{"data": {"assignedModel": "claude-3-sonnet"}}`
	req4 := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body4)))
	req4.Header.Set("Content-Type", "application/json")
	w4 := httptest.NewRecorder()
	router.ServeHTTP(w4, req4)

	if w4.Code != http.StatusOK {
		t.Fatalf("expected status 200 for data update, got %d: %s", w4.Code, w4.Body.String())
	}
	conn, err = repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("failed to fetch connection: %v", err)
	}
	dataMap = nil
	_ = json.Unmarshal([]byte(conn.Data), &dataMap)
	if dataMap["assignedModel"] != "claude-3-sonnet" {
		t.Errorf("expected assignedModel claude-3-sonnet, got %v", dataMap["assignedModel"])
	}
	if dataMap["apiKey"] != "sk-ant-secret" {
		t.Errorf("expected apiKey to be preserved, got %v", dataMap["apiKey"])
	}
}
