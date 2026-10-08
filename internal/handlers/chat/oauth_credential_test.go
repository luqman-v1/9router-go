package chat

import (
	json "encoding/json/v2"
	"testing"
	"time"

	"9router/proxy/internal/db"
	_ "9router/proxy/internal/providers"
)

// A Kiro connection can carry BOTH an apiKey and an OAuth accessToken. Which one
// reaches the upstream is decided by resolveProviderAuthToken, and the OAuth
// refresh step must not silently substitute a different credential for it —
// upstream (open-sse/executors/kiro.js buildHeaders) only uses the apiKey for
// `authMethod: "api_key"` connections, and sending the other one makes
// CodeWhisperer answer 403 "The bearer token included in the request is invalid."
func TestRefreshOAuth_KeepsDeliberateCredential(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// expiresAt in the future: the token is valid, so refresh must be a no-op
	// that hands back whatever credential the caller already chose.
	connData := map[string]any{
		"apiKey":               "deliberate-api-key",
		"accessToken":          "oauth-access-token",
		"refreshToken":         "refresh-token",
		"expiresAt":            time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
		"providerSpecificData": map[string]any{"authMethod": "api_key"},
	}
	raw, err := json.Marshal(connData)
	if err != nil {
		t.Fatalf("marshal conn data: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('kiro-conn', 'kiro', 'oauth', 'Kiro', 1, 1, ?, '2026-10-07T00:00:00Z', '2026-10-07T00:00:00Z')`, string(raw)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	chosen := resolveProviderAuthToken("kiro", &ConnectionData{
		APIKey:               "deliberate-api-key",
		AccessToken:          "oauth-access-token",
		ProviderSpecificData: map[string]any{"authMethod": "api_key"},
	}, "deliberate-api-key")
	if chosen != "deliberate-api-key" {
		t.Fatalf("precondition: resolveProviderAuthToken chose %q, want the apiKey", chosen)
	}

	got, _, err := h.refreshOAuthTokenIfExpired("kiro-conn", chosen)
	if err != nil {
		t.Fatalf("refreshOAuthTokenIfExpired: %v", err)
	}
	if got != chosen {
		t.Fatalf("token = %q, want the caller's %q: a valid, non-expired connection must keep the credential the caller chose",
			got, chosen)
	}
}
