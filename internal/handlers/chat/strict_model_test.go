package chat

import (
	"context"

	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

func TestFilterConnectionsForModel_Conditions(t *testing.T) {
	conn1 := &models.ProviderConnection{ID: "c1", Data: `{"assignedModel":"gpt-4o"}`}
	conn2 := &models.ProviderConnection{ID: "c2", Data: `{"freebuffModel":"o3-mini"}`}
	conn3 := &models.ProviderConnection{ID: "c3", Data: `{"providerSpecificData":{"assignedModel":"gpt-4o"}}`}
	conn4 := &models.ProviderConnection{ID: "c4", Data: `{"providerSpecificData":{"freebuffModel":"claude-3-5-sonnet"}}`}
	conn5 := &models.ProviderConnection{ID: "c5", Data: `{"apiKey":"sk-none"}`}
	connNil := (*models.ProviderConnection)(nil)
	connMalformed := &models.ProviderConnection{ID: "c6", Data: `{invalid-json}`}

	all := []*models.ProviderConnection{conn1, conn2, conn3, conn4, conn5, connNil, connMalformed}

	// 1. settings == nil
	res := filterConnectionsForModel("openai", all, "gpt-4o", nil)
	if len(res) != len(all) {
		t.Fatalf("expected all connections when settings == nil, got %d", len(res))
	}

	// 2. settings.ProviderStrategies == nil
	res = filterConnectionsForModel("openai", all, "gpt-4o", &db.SettingsData{})
	if len(res) != len(all) {
		t.Fatalf("expected all connections when ProviderStrategies == nil, got %d", len(res))
	}

	// 3. model == ""
	settings := &db.SettingsData{
		ProviderStrategies: map[string]db.ProviderStrategy{
			"openai": {StrictModelAssignment: true},
		},
	}
	res = filterConnectionsForModel("openai", all, "", settings)
	if len(res) != len(all) {
		t.Fatalf("expected all connections when model is empty, got %d", len(res))
	}

	// 4. provider not in strategies
	res = filterConnectionsForModel("anthropic", all, "gpt-4o", settings)
	if len(res) != len(all) {
		t.Fatalf("expected all connections when provider not in strategies, got %d", len(res))
	}

	// 5. StrictModelAssignment == false
	settings.ProviderStrategies["openai"] = db.ProviderStrategy{StrictModelAssignment: false}
	res = filterConnectionsForModel("openai", all, "gpt-4o", settings)
	if len(res) != len(all) {
		t.Fatalf("expected all connections when StrictModelAssignment is false, got %d", len(res))
	}

	// 6. StrictModelAssignment == true, filter for gpt-4o
	settings.ProviderStrategies["openai"] = db.ProviderStrategy{StrictModelAssignment: true}
	res = filterConnectionsForModel("openai", all, "gpt-4o", settings)
	if len(res) != 2 {
		t.Fatalf("expected 2 connections for gpt-4o (conn1 and conn3), got %d", len(res))
	}
	if res[0].ID != "c1" || res[1].ID != "c3" {
		t.Errorf("unexpected connection IDs: %s, %s", res[0].ID, res[1].ID)
	}

	// 7. Filter for o3-mini (freebuffModel)
	res = filterConnectionsForModel("openai", all, "o3-mini", settings)
	if len(res) != 1 || res[0].ID != "c2" {
		t.Fatalf("expected 1 connection (c2) for o3-mini, got %v", res)
	}

	// 8. Filter for claude-3-5-sonnet (providerSpecificData.freebuffModel)
	res = filterConnectionsForModel("openai", all, "claude-3-5-sonnet", settings)
	if len(res) != 1 || res[0].ID != "c4" {
		t.Fatalf("expected 1 connection (c4) for claude-3-5-sonnet, got %v", res)
	}

	// 9. Filter for model with no matches
	res = filterConnectionsForModel("openai", all, "nonexistent-model", settings)
	if len(res) != 0 {
		t.Fatalf("expected 0 connections for nonexistent-model, got %d", len(res))
	}
}

func TestGetBestConnection_StrictAssignmentIntegration(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Clean out any default connections for provider 'test-strict'
	_, _ = database.Exec(`DELETE FROM providerConnections WHERE provider = 'test-strict'`)

	// Create 2 connections for test-strict
	// Conn A: assignedModel: "model-a"
	if err := repo.CreateProviderConnection(context.Background(), "conn-a", "test-strict", "apikey", "Conn A", "key-a"); err != nil {
		t.Fatalf("failed to create conn-a: %v", err)
	}
	if err := repo.UpdateConnectionData(context.Background(), "conn-a", `{"apiKey":"key-a","assignedModel":"model-a"}`); err != nil {
		t.Fatalf("failed to update conn-a data: %v", err)
	}
	if err := repo.SetConnectionPriority("conn-a", 10); err != nil {
		t.Fatalf("failed to set conn-a priority: %v", err)
	}

	// Conn B: providerSpecificData.assignedModel: "model-b"
	if err := repo.CreateProviderConnection(context.Background(), "conn-b", "test-strict", "apikey", "Conn B", "key-b"); err != nil {
		t.Fatalf("failed to create conn-b: %v", err)
	}
	if err := repo.UpdateConnectionData(context.Background(), "conn-b", `{"apiKey":"key-b","providerSpecificData":{"assignedModel":"model-b"}}`); err != nil {
		t.Fatalf("failed to update conn-b data: %v", err)
	}
	if err := repo.SetConnectionPriority("conn-b", 5); err != nil {
		t.Fatalf("failed to set conn-b priority: %v", err)
	}

	// Enable StrictModelAssignment for test-strict in settings
	settingsJSON := `{
		"providerStrategies": {
			"test-strict": {
				"strictModelAssignment": true
			}
		}
	}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	// Request model-a: should get conn-a
	conn, _, err := handler.getBestConnection("test-strict", "", nil, "model-a")
	if err != nil {
		t.Fatalf("unexpected error resolving model-a: %v", err)
	}
	if conn.ID != "conn-a" {
		t.Errorf("expected conn-a, got %s", conn.ID)
	}

	// Request model-b: should get conn-b
	conn, _, err = handler.getBestConnection("test-strict", "", nil, "model-b")
	if err != nil {
		t.Fatalf("unexpected error resolving model-b: %v", err)
	}
	if conn.ID != "conn-b" {
		t.Errorf("expected conn-b, got %s", conn.ID)
	}

	// Request unassigned model: should return expected error
	_, _, err = handler.getBestConnection("test-strict", "", nil, "unassigned-model")
	if err == nil {
		t.Fatal("expected error for unassigned model under strict assignment, got nil")
	}
	expectedErr := "no connection assigned to model unassigned-model for provider test-strict under strict assignment"
	if !strings.Contains(err.Error(), expectedErr) {
		t.Errorf("expected error containing %q, got %q", expectedErr, err.Error())
	}

	// Now disable StrictModelAssignment
	settingsJSONDisabled := `{
		"providerStrategies": {
			"test-strict": {
				"strictModelAssignment": false
			}
		}
	}`
	if _, err := database.Exec(`UPDATE settings SET data = ? WHERE id = 1`, settingsJSONDisabled); err != nil {
		t.Fatalf("failed to update settings: %v", err)
	}

	// Request unassigned model: should now succeed by picking highest priority connection (conn-b priority 5 < 10)
	conn, _, err = handler.getBestConnection("test-strict", "", nil, "unassigned-model")
	if err != nil {
		t.Fatalf("unexpected error when strict assignment disabled: %v", err)
	}
	if conn.ID != "conn-b" {
		t.Errorf("expected conn-b, got %s", conn.ID)
	}
}
