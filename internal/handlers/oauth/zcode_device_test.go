package oauth

import (
	"encoding/json"
	"testing"

	"9router/proxy/internal/db"
)

// A re-login mints a new plan JWT and bumps token_version, which kills the
// previously stored credential. Keying the connection on the token hash would
// leave the old row active and rotation would keep dialling a dead token, so
// the row must be reused by account and keep its device identity.
func TestSaveDeviceConnectionZcode_reusesAccountRowAndIdentity(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	first := handler.saveDeviceConnection("zcode", deviceTokens{
		access: "plan-jwt-one",
		email:  "user@example.com",
		extra: map[string]any{
			"deviceId":    "dev-1",
			"sessionSeed": "seed-1",
			"accountUUID": "acct-1",
		},
	})
	if first.id == "" {
		t.Fatal("first login inserted no connection")
	}

	second := handler.saveDeviceConnection("zcode", deviceTokens{
		access: "plan-jwt-two",
		email:  "user@example.com",
		extra: map[string]any{
			"deviceId":    "dev-2",
			"sessionSeed": "seed-2",
			"accountUUID": "acct-1",
		},
	})
	if second.id != first.id {
		t.Fatalf("re-login created a second row: %q vs %q", second.id, first.id)
	}

	var count int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM providerConnections WHERE provider = 'zcode'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one zcode row after re-login, got %d", count)
	}

	var raw string
	if err := database.QueryRow(
		"SELECT data FROM providerConnections WHERE id = ?", first.id).Scan(&raw); err != nil {
		t.Fatalf("read data: %v", err)
	}
	var data struct {
		APIKey               string         `json:"apiKey"`
		ProviderSpecificData map[string]any `json:"providerSpecificData"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("parse data: %v", err)
	}
	if data.APIKey != "plan-jwt-two" {
		t.Errorf("expected the newest plan JWT, got %q", data.APIKey)
	}
	if got, _ := data.ProviderSpecificData["deviceId"].(string); got != "dev-1" {
		t.Errorf("device identity was replaced: %q", got)
	}
	if got, _ := data.ProviderSpecificData["sessionSeed"].(string); got != "seed-1" {
		t.Errorf("session seed was replaced: %q", got)
	}
}

// Accounts must stay independent: a second z.ai account gets its own row.
func TestSaveDeviceConnectionZcode_separateAccountsStaySeparate(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	a := handler.saveDeviceConnection("zcode", deviceTokens{access: "jwt-a", email: "a@example.com"})
	b := handler.saveDeviceConnection("zcode", deviceTokens{access: "jwt-b", email: "b@example.com"})
	if a.id == "" || b.id == "" || a.id == b.id {
		t.Fatalf("expected two distinct rows, got %q and %q", a.id, b.id)
	}
	var count int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM providerConnections WHERE provider = 'zcode' AND isActive = 1").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected both accounts active, got %d", count)
	}
}
