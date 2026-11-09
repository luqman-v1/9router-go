package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
)

// g05FixturePayload is the migration rehearsal corpus: every access state the
// spec distinguishes, a malformed row that must be skipped rather than abort
// the import, alias/combo grants, a proxy binding and an over-limit header.
func g05FixturePayload() map[string]any {
	return map[string]any{
		"settings": map[string]any{
			"providerOverrides": map[string]any{
				"conn-big": map[string]any{
					"headers": map[string]any{"X-Oversize": strings.Repeat("v", 9000)},
				},
			},
		},
		"providerConnections": []any{
			map[string]any{"id": "conn-1", "provider": "openai", "authType": "api-key", "name": "main", "isActive": true},
			map[string]any{"id": "conn-big", "provider": "anthropic", "authType": "api-key", "name": "big", "isActive": true},
		},
		"proxyPools": []any{
			map[string]any{"id": "pool-1", "name": "primary", "isActive": true},
		},
		"apiKeys": []any{
			map[string]any{"id": "k-open", "key": "sk-open-000000000000", "name": "open", "isActive": true,
				"access": map[string]any{"restricted": false, "allow": []any{}}},
			map[string]any{"id": "k-empty", "key": "sk-empty-00000000000", "name": "empty", "isActive": true,
				"access": map[string]any{"restricted": true, "allow": []any{}}},
			map[string]any{"id": "k-list", "key": "sk-list-000000000000", "name": "list", "isActive": true,
				"access": map[string]any{"restricted": true, "allow": []any{"gpt-4o", "claude-3"}}},
			// malformed: no id, skipped by the import loop
			map[string]any{"key": "sk-no-id-0000000000", "isActive": true},
			// malformed: wrong type for the access object, falls back to allowedModels
			map[string]any{"id": "k-odd", "key": "sk-odd-0000000000000", "isActive": true,
				"access": "not-an-object", "allowedModels": []any{"legacy-model"}},
		},
		"combos": []any{
			map[string]any{"id": "combo-1", "name": "grants", "kind": "chat", "models": []any{"k-list:gpt-4o"}},
		},
		"modelAliases": map[string]any{"fast": map[string]any{"provider": "openai", "model": "gpt-4o-mini"}},
	}
}

// TestG05RehearsalRoundTrip is the P5 Step 1 rehearsal against a synthetic
// corpus. It pins the three things that make a migration safe to run on real
// data: a dry run changes no content, the apply is atomic and idempotent, and
// the report never carries a credential.
func TestG05RehearsalRoundTrip(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	post := func(payload map[string]any, query string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/settings/database"+query, bytes.NewReader(body))
		req.Header.Set(cliTokenHeader, auth.CLIToken())
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	fixture := g05FixturePayload()

	if rec := post(fixture, "?dryRun=1"); rec.Code != http.StatusOK {
		t.Fatalf("dry run failed: %d %s", rec.Code, rec.Body.String())
	}
	for table, query := range map[string]string{
		"apiKeys":             "SELECT COUNT(*) FROM apiKeys",
		"providerConnections": "SELECT COUNT(*) FROM providerConnections",
		"combos":              "SELECT COUNT(*) FROM combos",
		"access":              "SELECT COUNT(*) FROM api_key_model_access",
	} {
		var n int
		if err := repo.RawDB().QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("dry run wrote %d rows into %s", n, table)
		}
	}

	if rec := post(fixture, ""); rec.Code != http.StatusOK {
		t.Fatalf("apply failed: %d %s", rec.Code, rec.Body.String())
	}

	assertAccess := func(keyID string, want []string) {
		t.Helper()
		rows, err := repo.RawDB().Query(`SELECT model FROM api_key_model_access WHERE api_key_id = ? ORDER BY model`, keyID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				t.Fatal(err)
			}
			got = append(got, m)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(want)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s access = %v, want %v", keyID, got, want)
		}
	}
	assertAccess("k-open", nil)
	assertAccess("k-empty", []string{"!*"})
	assertAccess("k-list", []string{"claude-3", "gpt-4o"})
	assertAccess("k-odd", []string{"legacy-model"})

	var keyCount int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM apiKeys`).Scan(&keyCount); err != nil {
		t.Fatal(err)
	}
	if keyCount != 4 {
		t.Fatalf("apiKeys = %d, want 4 (the id-less row must be skipped, not inserted)", keyCount)
	}

	// Apply again: the same corpus must converge on the same rows, not double
	// them. A backup restored twice is the ordinary case after a failed cutover.
	if rec := post(fixture, ""); rec.Code != http.StatusOK {
		t.Fatalf("second apply failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM apiKeys`).Scan(&keyCount); err != nil {
		t.Fatal(err)
	}
	if keyCount != 4 {
		t.Fatalf("after re-apply apiKeys = %d, want 4 — import is not idempotent", keyCount)
	}
	var accessCount int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM api_key_model_access`).Scan(&accessCount); err != nil {
		t.Fatal(err)
	}
	if accessCount != 4 {
		t.Fatalf("after re-apply access rows = %d, want 4", accessCount)
	}
}

// A mid-import failure must leave the target exactly as it was. The import
// wipes first, so an error after the wipe that still committed would destroy
// the live state and restore nothing.
func TestG05RehearsalFailureKeepsTargetUnchanged(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	if _, err := repo.RawDB().Exec(`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt)
		VALUES ('live-1','sk-live','live','m1',1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// providerConnections.provider is NOT NULL, so a connection row without one
	// fails the insert — after every DELETE has already run inside the
	// transaction. That is the point: the failure has to land after the wipes
	// for this test to prove the rollback rather than a driver-level rejection.
	payload := map[string]any{
		"apiKeys": []any{map[string]any{"id": "k-new", "key": "sk-new", "isActive": true}},
		"providerConnections": []any{
			map[string]any{"id": "c-bad", "name": "no provider"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("the malformed connection was accepted, so the atomicity check proves nothing")
	}

	var live int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM apiKeys WHERE id = 'live-1'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("failed import deleted the live key: live-1 rows = %d (status %d)", live, rec.Code)
	}
	var inserted int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM apiKeys WHERE id = 'k-new'`).Scan(&inserted); err != nil {
		t.Fatal(err)
	}
	if inserted != 0 {
		t.Fatalf("failed import left a partial write: k-new rows = %d", inserted)
	}
}

// The rehearsal report must describe the payload without reproducing its
// credentials: it is copied into tickets and run logs.
func TestG05RehearsalReportHasNoSecrets(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	payload := g05FixturePayload()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database?dryRun=1", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run failed: %d %s", rec.Code, rec.Body.String())
	}

	out := rec.Body.String()
	for _, secret := range []string{"sk-open-000000000000", "sk-empty-00000000000", "sk-list-000000000000", strings.Repeat("v", 9000)} {
		if strings.Contains(out, secret) {
			t.Fatalf("dry-run report contains a credential value (%d chars)", len(secret))
		}
	}
	// The warning must still name what is wrong, or an operator cannot act on it.
	if !strings.Contains(out, "incompatible_header_limit") || !strings.Contains(out, "X-Oversize") {
		t.Fatalf("report dropped the over-limit header warning: %s", out)
	}
}
