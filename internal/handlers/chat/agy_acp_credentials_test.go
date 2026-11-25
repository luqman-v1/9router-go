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

// Regression: the request path marshals ConnectionData, which has no
// refreshToken field. A Google-login agy-acp connection stores its tokens in
// the DB row; copy mode must read them from there instead of failing with
// errNoACPCredentials (HTTP 502 on every turn after dashboard login).
func TestResolveACPCredentialsFromStoredRow(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)
	h := &ChatHandler{Repo: repo}

	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	data, _ := json.Marshal(map[string]any{
		"accessToken":  "stored-access",
		"refreshToken": "stored-refresh",
		"expiresAt":    expiry,
		"apiKey":       "stored-access",
	})
	if err := repo.CreateProviderConnectionFull(context.Background(), "agcpy-own", "agy-acp", "oauth", "own", nil, string(data)); err != nil {
		t.Fatal(err)
	}
	// Exactly what fallback.go hands down: ConnectionData minus refreshToken.
	requestPath := map[string]any{"apiKey": "stored-access", "accessToken": "stored-access"}
	access, refresh, gotExpiry, err := h.resolveACPCredentials(context.Background(), requestPath, "agcpy-own")
	if err != nil {
		t.Fatalf("stored-row copy mode failed: %v", err)
	}
	if access != "stored-access" || refresh != "stored-refresh" || gotExpiry.IsZero() {
		t.Fatalf("stored-row copy mode returned %q/%q/%v", access, refresh, gotExpiry)
	}
}

func TestResolveACPCredentialsNoTokensAnywhere(t *testing.T) {
	h := &ChatHandler{}
	_, _, _, err := h.resolveACPCredentials(context.Background(), map[string]any{"accessToken": "only-access"}, "conn-x")
	if err == nil {
		t.Fatal("expected error when no refresh token exists anywhere")
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
	if err := repo.CreateProviderConnectionFull(context.Background(), "ag-source", "antigravity", "oauth", "source", nil, string(data)); err != nil {
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
	if err := repo.CreateProviderConnectionFull(context.Background(), "other", "openai", "oauth", "other", nil, string(data)); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := h.resolveACPCredentials(context.Background(), map[string]any{"sourceConnection": "other"}, "agcpy-2")
	if err == nil {
		t.Fatal("expected error for non-antigravity source")
	}
}
