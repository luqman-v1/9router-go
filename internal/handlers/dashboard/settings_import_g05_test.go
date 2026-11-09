package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
)

// A Node backup carries a key's restriction as access{restricted,allow}. The
// Go importer only ever read allowedModels, so importing a Node backup turned
// every restricted key back into an unrestricted one — a fail-open restore,
// the exact outcome a restriction exists to prevent.
func TestG05ImportHonoursNodeAccessField(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	payload := map[string]any{
		"apiKeys": []any{
			// restricted-empty: blocks every model
			map[string]any{
				"id": "key-empty", "key": "sk-a", "name": "a", "isActive": true,
				"access": map[string]any{"restricted": true, "allow": []any{}},
			},
			// restricted-list
			map[string]any{
				"id": "key-list", "key": "sk-b", "name": "b", "isActive": true,
				"access": map[string]any{"restricted": true, "allow": []any{"gpt-4o"}},
			},
			// unrestricted: no rows at all
			map[string]any{
				"id": "key-open", "key": "sk-c", "name": "c", "isActive": true,
				"access": map[string]any{"restricted": false, "allow": []any{}},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", rec.Code, rec.Body.String())
	}

	rows := func(id string) []string {
		t.Helper()
		rs, err := repo.RawDB().Query(`SELECT model FROM api_key_model_access WHERE api_key_id = ? ORDER BY model`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		var out []string
		for rs.Next() {
			var m string
			if err := rs.Scan(&m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
		if err := rs.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := rows("key-empty"); len(got) != 1 || got[0] != "!*" {
		t.Fatalf("restricted-empty restored as %v, want [!*] — a blocked key came back open", got)
	}
	if got := rows("key-list"); len(got) != 1 || got[0] != "gpt-4o" {
		t.Fatalf("restricted-list restored as %v, want [gpt-4o]", got)
	}
	if got := rows("key-open"); len(got) != 0 {
		t.Fatalf("unrestricted restored as %v, want no rows", got)
	}
}

// Importing a backup that carries an allowlist always failed: the insert never
// supplied created_at, which the column declares NOT NULL. Every restore of a
// key with model restrictions was rejected with a constraint error.
func TestG05ImportAcceptsAllowlist(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	payload := map[string]any{
		"apiKeys": []any{map[string]any{
			"id": "key-1", "key": "sk-cli-1", "name": "cli", "isActive": true,
			"allowedModels": []any{"gpt-4o"},
		}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import with allowlist failed: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM api_key_model_access WHERE api_key_id = 'key-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("allowlist row count = %d, want 1", n)
	}
}

// A dry run must report what the import would do and write nothing. The G05
// spec makes this a precondition for rehearsing a real migration.
func TestG05ImportDryRunWritesNothing(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	if _, err := repo.RawDB().Exec(`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt)
		VALUES ('live-1','sk-live','live','m1',1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{
		"apiKeys": []any{map[string]any{
			"id": "key-1", "key": "sk-cli-1", "name": "cli", "isActive": true,
			"access": map[string]any{"restricted": true, "allow": []any{"gpt-4o"}},
		}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database?dryRun=1", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run failed: %d %s", rec.Code, rec.Body.String())
	}

	var live int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM apiKeys WHERE id = 'live-1'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("dry run deleted the live key: live-1 rows = %d", live)
	}
	var imported int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM apiKeys WHERE id = 'key-1'`).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	if imported != 0 {
		t.Fatalf("dry run wrote the imported key: key-1 rows = %d", imported)
	}
	var access int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM api_key_model_access`).Scan(&access); err != nil {
		t.Fatal(err)
	}
	if access != 0 {
		t.Fatalf("dry run wrote %d access rows", access)
	}

	var report map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("dry run body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if report["dryRun"] != true {
		t.Fatalf("dry run response does not identify itself: %v", report)
	}
	if _, ok := report["apiKeys"]; !ok {
		t.Fatalf("dry run response carries no per-table counts: %v", report)
	}
}

// Export must emit both fields: allowedModels for a Go backup, and access for
// the Node dashboard that reads backups taken on this runtime.
func TestG05ExportEmitsBothAccessShapes(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	specs := []struct {
		id      string
		models  []string
		wantAcc map[string]any
	}{
		{id: "k-open", models: nil, wantAcc: map[string]any{"restricted": false, "allow": []any{}}},
		{id: "k-empty", models: []string{"!*"}, wantAcc: map[string]any{"restricted": true, "allow": []any{}}},
		{id: "k-list", models: []string{"gpt-4o"}, wantAcc: map[string]any{"restricted": true, "allow": []any{"gpt-4o"}}},
	}
	for _, s := range specs {
		if err := repo.CreateApiKey(s.id, "sk-"+s.id+"-abcdefghijklmnop", s.id, "m"); err != nil {
			t.Fatalf("CreateApiKey %s: %v", s.id, err)
		}
		if len(s.models) > 0 {
			if err := repo.SetAllowedModels(s.id, s.models); err != nil {
				t.Fatalf("SetAllowedModels %s: %v", s.id, err)
			}
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export failed: %d %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	rows, _ := payload["apiKeys"].([]any)
	byID := map[string]map[string]any{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if id, _ := row["id"].(string); id != "" {
			byID[id] = row
		}
	}

	for _, s := range specs {
		row := byID[s.id]
		if row == nil {
			t.Fatalf("%s missing from export", s.id)
		}
		// allowedModels must always be present, even when empty.
		if _, ok := row["allowedModels"]; !ok {
			t.Errorf("%s: allowedModels missing", s.id)
		}
		acc, ok := row["access"].(map[string]any)
		if !ok {
			t.Fatalf("%s: access missing or not an object: %v", s.id, row["access"])
		}
		if acc["restricted"] != s.wantAcc["restricted"] {
			t.Errorf("%s: access.restricted = %v, want %v", s.id, acc["restricted"], s.wantAcc["restricted"])
		}
		allow, _ := acc["allow"].([]any)
		want, _ := s.wantAcc["allow"].([]any)
		if len(allow) != len(want) {
			t.Errorf("%s: access.allow = %v, want %v", s.id, allow, want)
		}
	}
}

// A provider override that breaks the G17 limits must be reported, not
// silently trimmed. The warning names the connection, the header and the
// offending length, and the header itself survives the import untouched —
// truncating an operator's header would change what goes on the wire.
func TestG05ImportWarnsOnIncompatibleHeaderLimit(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	longValue := strings.Repeat("v", 8193)
	settings := map[string]any{
		"providerOverrides": map[string]any{
			"conn-1": map[string]any{
				"headers": map[string]any{"X-Big": longValue},
			},
		},
	}
	payload := map[string]any{"settings": settings}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database?dryRun=1", bytes.NewReader(body))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run failed: %d %s", rec.Code, rec.Body.String())
	}

	var report map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("dry run body is not JSON: %v (%s)", err, rec.Body.String())
	}
	warnings, _ := report["warnings"].([]any)
	if len(warnings) == 0 {
		t.Fatalf("no warning for an over-limit header: %v", report)
	}
	w, _ := warnings[0].(map[string]any)
	if w["code"] != "incompatible_header_limit" {
		t.Fatalf("warning code = %v, want incompatible_header_limit", w["code"])
	}
	if w["connectionId"] != "conn-1" {
		t.Errorf("warning connectionId = %v, want conn-1", w["connectionId"])
	}
	if w["headerName"] != "X-Big" {
		t.Errorf("warning headerName = %v, want X-Big", w["headerName"])
	}
	if w["length"] != float64(len(longValue)) {
		t.Errorf("warning length = %v, want %d", w["length"], len(longValue))
	}
}
