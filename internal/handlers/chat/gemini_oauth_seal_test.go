package chat

import (
	"context"
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/oauth"
	"9router/proxy/internal/vault"
)

func TestChatOAuthRefresh_VaultSealedConnections(t *testing.T) {
	t.Run("RefreshOAuthTokenIfExpired refreshes and seals vault connection", func(t *testing.T) {
		const providerID = "oauth-vault-lazy-test"
		oauth.ResetRefreshCache()
		oauth.ResetRefreshSlots()
		oauth.Register(providerID, func(context.Context, *oauth.Params) (*oauth.TokenResult, error) {
			return &oauth.TokenResult{
				AccessToken:  "rotated-at-secret",
				RefreshToken: "rotated-rt-secret",
				ExpiresIn:    3600,
			}, nil
		})
		t.Cleanup(func() { oauth.Unregister(providerID) })

		database, cleanup := setupChatTestDB(t)
		defer cleanup()

		v, err := vault.New([]byte("01234567890123456789012345678901"))
		if err != nil {
			t.Fatalf("build vault: %v", err)
		}
		repo := db.NewRepo(database)
		repo.SetVault(v)
		h := NewChatHandler(repo)

		payload, err := json.Marshal(map[string]any{
			"accessToken":  "expired-at-secret",
			"apiKey":       "expired-at-secret",
			"refreshToken": "initial-rt-secret",
			"expiresAt":    time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		prio := 1
		if err := repo.CreateProviderConnectionFull(context.Background(), "conn-lazy", providerID, "oauth", "Lazy", &prio, string(payload)); err != nil {
			t.Fatalf("create sealed connection: %v", err)
		}

		got, _, err := h.RefreshOAuthTokenIfExpired("conn-lazy", "expired-at-secret")
		if err != nil {
			t.Fatalf("RefreshOAuthTokenIfExpired error: %v", err)
		}
		if got != "rotated-at-secret" {
			t.Fatalf("token = %q, want %q", got, "rotated-at-secret")
		}

		var raw string
		if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, "conn-lazy").Scan(&raw); err != nil {
			t.Fatalf("read raw data: %v", err)
		}
		if strings.Contains(raw, "rotated-at-secret") || strings.Contains(raw, "rotated-rt-secret") {
			t.Fatalf("refreshed tokens stored plaintext at rest: %s", raw)
		}

		conn, err := repo.GetProviderConnectionByID("conn-lazy")
		if err != nil {
			t.Fatalf("hydrate connection: %v", err)
		}
		if !strings.Contains(conn.Data, "rotated-at-secret") || !strings.Contains(conn.Data, "rotated-rt-secret") {
			t.Fatalf("hydrated data missing rotated tokens: %s", conn.Data)
		}
	})

	t.Run("ForceRefreshOAuthToken refreshes and seals vault connection", func(t *testing.T) {
		const providerID = "oauth-vault-force-test"
		oauth.ResetRefreshCache()
		oauth.ResetRefreshSlots()
		oauth.Register(providerID, func(context.Context, *oauth.Params) (*oauth.TokenResult, error) {
			return &oauth.TokenResult{
				AccessToken:  "forced-at-secret",
				RefreshToken: "forced-rt-secret",
				ExpiresIn:    3600,
			}, nil
		})
		t.Cleanup(func() { oauth.Unregister(providerID) })

		database, cleanup := setupChatTestDB(t)
		defer cleanup()

		v, err := vault.New([]byte("01234567890123456789012345678901"))
		if err != nil {
			t.Fatalf("build vault: %v", err)
		}
		repo := db.NewRepo(database)
		repo.SetVault(v)
		h := NewChatHandler(repo)

		payload, err := json.Marshal(map[string]any{
			"accessToken":  "stale-force-at",
			"apiKey":       "stale-force-at",
			"refreshToken": "force-rt-secret",
			"expiresAt":    time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		prio := 1
		if err := repo.CreateProviderConnectionFull(context.Background(), "conn-force", providerID, "oauth", "Force", &prio, string(payload)); err != nil {
			t.Fatalf("create sealed connection: %v", err)
		}

		got, _, err := h.ForceRefreshOAuthToken("conn-force")
		if err != nil {
			t.Fatalf("ForceRefreshOAuthToken error: %v", err)
		}
		if got != "forced-at-secret" {
			t.Fatalf("token = %q, want %q", got, "forced-at-secret")
		}

		var raw string
		if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, "conn-force").Scan(&raw); err != nil {
			t.Fatalf("read raw data: %v", err)
		}
		if strings.Contains(raw, "forced-at-secret") || strings.Contains(raw, "forced-rt-secret") {
			t.Fatalf("forced refresh tokens stored plaintext at rest: %s", raw)
		}

		conn, err := repo.GetProviderConnectionByID("conn-force")
		if err != nil {
			t.Fatalf("hydrate connection: %v", err)
		}
		if !strings.Contains(conn.Data, "forced-at-secret") || !strings.Contains(conn.Data, "forced-rt-secret") {
			t.Fatalf("hydrated data missing forced tokens: %s", conn.Data)
		}
	})
}
