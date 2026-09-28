package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
)

// setupSettingsTestDB extends the shared dashboard schema with the tables the
// backup export walks (providerNodes/proxyPools are created elsewhere in prod).
// DATA_DIR is isolated per test so the CLI-token files never touch ~/.9router.
func setupSettingsTestDB(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	repo, cleanup := setupTestDB(t)

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS providerNodes (
			id TEXT PRIMARY KEY,
			type TEXT,
			name TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS proxyPools (
			id TEXT PRIMARY KEY,
			isActive INTEGER DEFAULT 1,
			testStatus TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := repo.RawDB().Exec(stmt); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}
	return repo, cleanup
}

func TestHandleUpdateSettings_PasswordChange(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Settings persist without a password yet.
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"requireLogin":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for settings update, got %d: %s", rec.Code, rec.Body.String())
	}

	// First-time password set: no current password required.
	req = httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"newPassword":"s3cret-pass","currentPassword":""}`))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for first password set, got %d: %s", rec.Code, rec.Body.String())
	}

	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	hash, _ := raw["password"].(string)
	if hash == "" {
		t.Fatal("expected a stored password hash")
	}
	if hash == "s3cret-pass" {
		t.Fatal("password must be stored hashed, not plaintext")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret-pass")); err != nil {
		t.Errorf("stored hash does not match the new password: %v", err)
	}

	// Response must expose hasPassword and never the hash.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["hasPassword"] != true {
		t.Errorf("expected hasPassword=true, got %v", body["hasPassword"])
	}
	if _, leaked := body["password"]; leaked {
		t.Error("password hash must not be returned to the dashboard")
	}

	// Wrong current password is rejected.
	req = httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"newPassword":"other","currentPassword":"nope"}`))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong current password, got %d: %s", rec.Code, rec.Body.String())
	}

	// Correct current password rotates the hash.
	req = httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"newPassword":"rotated-pass","currentPassword":"s3cret-pass"}`))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for password rotation, got %d: %s", rec.Code, rec.Body.String())
	}
	h := &DashboardHandler{Repo: repo}
	if !h.verifyDashboardPassword("rotated-pass") {
		t.Error("rotated password should verify")
	}
	if h.verifyDashboardPassword("s3cret-pass") {
		t.Error("old password should no longer verify")
	}
	if h.verifyDashboardPassword("") {
		t.Error("empty password should never verify")
	}
}

func TestHandleExportDatabase_RequiresPassword(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// No password configured and no INITIAL_PASSWORD → reject.
	req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid password") {
		t.Errorf("expected Next-style error body, got %s", rec.Body.String())
	}
	// Local CLI token skips password re-auth (Next parity).
	req = httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for CLI token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleExportImportDatabase_RoundTrip(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	seed := []string{
		`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('conn-1', 'openai', 'apikey', 'Primary', 'a@b.c', 1, 1, '{"apiKey":"sk-test","model":"gpt"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt)
		 VALUES ('combo-1', 'fast', 'fallback', '["gpt","gemini"]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt)
		 VALUES ('key-1', 'sk-cli-1', 'cli', 'm1', 1, '2026-01-01T00:00:00Z')`,
		`INSERT INTO kv (scope, key, value) VALUES ('pricing', 'openai', '{"gpt":1.0}')`,
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt)
		 VALUES ('node-1', 'custom', 'local', '{"baseUrl":"http://x"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
		 VALUES ('pool-1', 1, 'passed', '{"type":"http","proxyUrl":"http://127.0.0.1:8080"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	}
	for _, stmt := range seed {
		if _, err := repo.RawDB().Exec(stmt); err != nil {
			t.Fatalf("seed failed: %v", err)
		}
	}

	// Export with the CLI token.
	req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export failed: %d %s", rec.Code, rec.Body.String())
	}
	exported := rec.Body.Bytes()

	var payload map[string]any
	if err := json.Unmarshal(exported, &payload); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	connections, _ := payload["providerConnections"].([]any)
	if len(connections) != 1 {
		t.Fatalf("expected 1 exported connection, got %d", len(connections))
	}
	conn := connections[0].(map[string]any)
	if conn["provider"] != "openai" {
		t.Errorf("expected provider=openai, got %v", conn["provider"])
	}
	if _, nested := conn["apiKey"]; !nested {
		t.Error("data blob fields should be merged into the exported row")
	}
	if _, hasData := conn["data"]; hasData {
		t.Error("raw data column should not survive the merge")
	}
	if active, ok := conn["isActive"].(bool); !ok || !active {
		t.Errorf("isActive should export as a boolean, got %#v", conn["isActive"])
	}
	if combos, _ := payload["combos"].([]any); len(combos) != 1 {
		t.Errorf("expected 1 combo, got %d", len(combos))
	}
	pricing, _ := payload["pricing"].(map[string]any)
	if _, ok := pricing["openai"]; !ok {
		t.Error("expected pricing.openai to be exported")
	}

	// Wipe, then restore from the backup.
	wipes := []string{
		`DELETE FROM providerConnections`,
		`DELETE FROM combos`,
		`DELETE FROM apiKeys`,
		`DELETE FROM providerNodes`,
		`DELETE FROM proxyPools`,
		`DELETE FROM kv`,
	}
	for _, stmt := range wipes {
		if _, err := repo.RawDB().Exec(stmt); err != nil {
			t.Fatalf("wipe failed: %v", err)
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/settings/database", bytes.NewReader(exported))
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", rec.Code, rec.Body.String())
	}

	var conns []string
	rows, err := repo.RawDB().Query(`SELECT data FROM providerConnections`)
	if err != nil {
		t.Fatalf("query restored connections: %v", err)
	}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		conns = append(conns, data)
	}
	rows.Close()
	if len(conns) != 1 {
		t.Fatalf("expected 1 restored connection, got %d", len(conns))
	}
	if !strings.Contains(conns[0], "sk-test") || !strings.Contains(conns[0], `"model":"gpt"`) {
		t.Errorf("restored data blob lost fields: %s", conns[0])
	}

	var comboModels string
	if err := repo.RawDB().QueryRow(`SELECT models FROM combos`).Scan(&comboModels); err != nil {
		t.Fatalf("query restored combo: %v", err)
	}
	if !strings.Contains(comboModels, "gemini") {
		t.Errorf("restored combo models wrong: %s", comboModels)
	}

	var pricingValue string
	if err := repo.RawDB().QueryRow(`SELECT value FROM kv WHERE scope='pricing' AND key='openai'`).Scan(&pricingValue); err != nil {
		t.Fatalf("query restored pricing: %v", err)
	}
	if !strings.Contains(pricingValue, "1") {
		t.Errorf("restored pricing wrong: %s", pricingValue)
	}

	var apiKey string
	if err := repo.RawDB().QueryRow(`SELECT key FROM apiKeys`).Scan(&apiKey); err != nil || apiKey != "sk-cli-1" {
		t.Errorf("restored api key wrong: %q err=%v", apiKey, err)
	}
}

// TestHandleImportDatabase_ClientContract pins the wire contract the Svelte
// dashboard uses: a raw JSON body (never multipart/form-data) and the
// x-9r-password header for re-auth, matching Next's settings/database route.
func TestHandleImportDatabase_ClientContract(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	payload := `{"settings":{"requireLogin":false},"providerConnections":[],"customModels":[]}`

	// Multipart uploads (the pre-fix client) are rejected, not silently ignored.
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("file", "backup.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/database", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid database payload") {
		t.Fatalf("multipart upload should be rejected, got %d %s", rec.Code, rec.Body.String())
	}

	// Raw JSON restores the data.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/database", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(cliTokenHeader, auth.CLIToken())
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw JSON import failed: %d %s", rec.Code, rec.Body.String())
	}

	// Once a password is set, the x-9r-password header must authorize too.
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret-pass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"password": string(hash)}); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/database", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(passwordHeader, "s3cret-pass")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("password header should authorize import: %d %s", rec.Code, rec.Body.String())
	}

	// The authorized import above wiped the settings row and re-inserted only
	// payload["settings"], so the stored hash is gone. Re-arm it, otherwise this
	// negative case would be answered by the default-password fallback rather
	// than by bcrypt and would keep passing even if header auth were broken.
	if err := repo.UpdateSettingsRaw(map[string]any{"password": string(hash)}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		password string
	}{
		{"wrong password", "wrong"},
		// The upstream default must not authorize once a password is stored.
		{"default password", defaultInitialPassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec = httptest.NewRecorder()
			req = httptest.NewRequest(http.MethodPost, "/api/settings/database", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(passwordHeader, tc.password)
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for %q, got %d: %s", tc.password, rec.Code, rec.Body.String())
			}
		})
	}
}
