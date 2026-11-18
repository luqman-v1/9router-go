package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestResolveACPCredentialsCopyMode(t *testing.T) {
	h := &ChatHandler{}
	connData := map[string]any{
		"accessToken":  "copied-access",
		"refreshToken": "copied-refresh",
		"expiresAt":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	access, refresh, expiry, err := h.resolveACPCredentials(context.Background(), connData, "conn-self")
	if err != nil {
		t.Fatal(err)
	}
	if access != "copied-access" || refresh != "copied-refresh" || expiry.IsZero() {
		t.Fatalf("copy mode returned %q/%q/%v", access, refresh, expiry)
	}
}

func TestResolveACPCredentialsReferenceMode(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	data, _ := json.Marshal(map[string]any{
		"accessToken":  "source-access",
		"refreshToken": "source-refresh",
		"expiresAt":    expiry,
		"apiKey":       "source-access",
	})
	if err := repo.CreateProviderConnectionFull("ag-source", "antigravity", "oauth", "source", nil, string(data)); err != nil {
		t.Fatal(err)
	}
	// Source has a non-expired token so no network refresh is attempted.
	connData := map[string]any{"sourceConnection": "ag-source"}
	access, refresh, gotExpiry, err := h.resolveACPCredentials(context.Background(), connData, "agcpy-1")
	if err != nil {
		t.Fatal(err)
	}
	if access != "source-access" || refresh != "source-refresh" {
		t.Fatalf("reference mode returned %q/%q", access, refresh)
	}
	if gotExpiry.IsZero() {
		t.Fatal("reference mode returned zero expiry")
	}
}

func TestResolveACPCredentialsWrongProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}
	data, _ := json.Marshal(map[string]any{"accessToken": "a", "refreshToken": "r"})
	if err := repo.CreateProviderConnectionFull("other", "openai", "oauth", "other", nil, string(data)); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := h.resolveACPCredentials(context.Background(), map[string]any{"sourceConnection": "other"}, "agcpy-2")
	if err == nil {
		t.Fatal("expected error for non-antigravity source")
	}
}
