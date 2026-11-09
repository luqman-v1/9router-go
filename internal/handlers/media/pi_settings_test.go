package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
)

// piTestHome points the home directory at a scratch directory and empties the
// PATH for the whole test, so neither the real ~/.pi nor a real `pi` binary
// can leak into the assertions.
func piTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	return home
}

func piWriteConfig(t *testing.T, relPath, content string) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

func piBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response %s: %v", rec.Body.String(), err)
	}
	return body
}

func TestPiSettingsGet(t *testing.T) {
	t.Run("without an install answers installed false", func(t *testing.T) {
		piTestHome(t)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleGet(rec, httptest.NewRequest(http.MethodGet, "/api/cli-tools/pi-settings", nil))

		body := piBody(t, rec)
		if rec.Code != http.StatusOK || body["installed"] != false {
			t.Fatalf("expected installed=false, got %d %v", rec.Code, body)
		}
		if body["config"] != nil {
			t.Errorf("expected config null, got %v", body["config"])
		}
	})

	t.Run("an agent-scoped models.json is reported with its 9router state", func(t *testing.T) {
		piTestHome(t)
		piWriteConfig(t, filepath.Join(".pi", "agent", "models.json"),
			`{"providers":{"9router":{"baseUrl":"http://localhost:20130/v1"}},"other":1}`)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleGet(rec, httptest.NewRequest(http.MethodGet, "/api/cli-tools/pi-settings", nil))

		body := piBody(t, rec)
		if body["installed"] != true || body["has9Router"] != true {
			t.Fatalf("expected installed+has9Router, got %v", body)
		}
		config, _ := body["config"].(map[string]any)
		if _, ok := config["other"]; !ok {
			t.Errorf("expected the whole config relayed, got %v", config)
		}
		if !strings.HasSuffix(body["configPath"].(string), filepath.Join(".pi", "agent", "models.json")) {
			t.Errorf("expected the agent path, got %v", body["configPath"])
		}
	})

	t.Run("has9Router also reads the upstream default port", func(t *testing.T) {
		piTestHome(t)
		piWriteConfig(t, filepath.Join(".pi", "models.json"),
			`{"providers":{"custom":{"baseUrl":"http://127.0.0.1:20128/v1"}}}`)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleGet(rec, httptest.NewRequest(http.MethodGet, "/api/cli-tools/pi-settings", nil))

		if body := piBody(t, rec); body["has9Router"] != true {
			t.Fatalf("expected has9Router via the 20128 fallback, got %v", body)
		}
	})
}

func TestPiSettingsPost(t *testing.T) {
	t.Run("baseUrl is required", func(t *testing.T) {
		piTestHome(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/cli-tools/pi-settings", strings.NewReader(`{}`))
		NewPiHandler(nil).HandlePost(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("a fresh config gets the default model block", func(t *testing.T) {
		piTestHome(t)
		rec := httptest.NewRecorder()
		body := `{"baseUrl":"http://localhost:20130","apiKey":"sk-test"}`
		NewPiHandler(nil).HandlePost(rec, httptest.NewRequest(http.MethodPost, "/api/cli-tools/pi-settings", strings.NewReader(body)))

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
		}
		raw, err := os.ReadFile(piWrittenPath(t))
		if err != nil {
			t.Fatalf("config was not written: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("written config is not JSON: %v\n%s", err, raw)
		}
		providers := config["providers"].(map[string]any)
		block := providers["9router"].(map[string]any)
		if block["baseUrl"] != "http://localhost:20130/v1" {
			t.Errorf("expected /v1 appended, got %v", block["baseUrl"])
		}
		if block["apiKey"] != "sk-test" || block["api"] != "openai-completions" {
			t.Errorf("unexpected key/api: %v", block)
		}
		models := block["models"].([]any)
		first := models[0].(map[string]any)
		if first["id"] != "provider/model-id" || first["contextWindow"].(float64) != 128000 || first["maxTokens"].(float64) != 16384 {
			t.Errorf("unexpected default model entry: %v", first)
		}
	})

	t.Run("the previously saved block merges (upstream 342ecb0d)", func(t *testing.T) {
		piTestHome(t)
		piWriteConfig(t, filepath.Join(".pi", "agent", "models.json"), `{
			"providers": {
				"9router": {"baseUrl":"http://old:1/v1","apiKey":"sk-stored","api":"openai-responses","custom":"keep"},
				"other": {"baseUrl":"http://elsewhere/v1"}
			}
		}`)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandlePost(rec, httptest.NewRequest(http.MethodPost, "/api/cli-tools/pi-settings",
			strings.NewReader(`{"baseUrl":"http://new:20130"}`)))

		raw, err := os.ReadFile(piWrittenPath(t))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("parse: %v", err)
		}
		providers := config["providers"].(map[string]any)
		if _, ok := providers["other"]; !ok {
			t.Errorf("foreign provider block was dropped: %v", providers)
		}
		block := providers["9router"].(map[string]any)
		if block["baseUrl"] != "http://new:20130/v1" {
			t.Errorf("baseUrl not updated: %v", block)
		}
		if block["apiKey"] != "sk-stored" || block["api"] != "openai-responses" || block["custom"] != "keep" {
			t.Errorf("previous fields not merged: %v", block)
		}
		// The models list is replaced, not merged.
		models := block["models"].([]any)
		if len(models) != 1 || models[0].(map[string]any)["id"] != "provider/model-id" {
			t.Errorf("expected the default model list, got %v", models)
		}
	})

	t.Run("a models list normalizes ids and objects", func(t *testing.T) {
		piTestHome(t)
		rec := httptest.NewRecorder()
		body := `{"baseUrl":"http://x","models":["m1",{"id":"m2","contextWindow":200000,"maxTokens":64000},{"id":"","name":"dropped"}]}`
		NewPiHandler(nil).HandlePost(rec, httptest.NewRequest(http.MethodPost, "/api/cli-tools/pi-settings", strings.NewReader(body)))

		raw, err := os.ReadFile(piWrittenPath(t))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("parse: %v", err)
		}
		models := config["providers"].(map[string]any)["9router"].(map[string]any)["models"].([]any)
		if len(models) != 3 {
			t.Fatalf("expected every entry kept with upstream defaults, got %v", models)
		}
		first := models[0].(map[string]any)
		if first["id"] != "m1" || first["name"] != "m1" || first["contextWindow"].(float64) != 128000 {
			t.Errorf("string entry not normalized: %v", first)
		}
		second := models[1].(map[string]any)
		if second["id"] != "m2" || second["name"] != "m2" || second["contextWindow"].(float64) != 200000 || second["maxTokens"].(float64) != 64000 {
			t.Errorf("object entry not normalized: %v", second)
		}
		// An id-less object reads as the upstream default id, keeping the name.
		third := models[2].(map[string]any)
		if third["id"] != "provider/model-id" || third["name"] != "dropped" {
			t.Errorf("empty id not defaulted: %v", third)
		}
	})

	t.Run("an absent key falls back to the first active dashboard key", func(t *testing.T) {
		piTestHome(t)
		repo := piTestRepo(t)
		rec := httptest.NewRecorder()
		NewPiHandler(repo).HandlePost(rec, httptest.NewRequest(http.MethodPost, "/api/cli-tools/pi-settings",
			strings.NewReader(`{"baseUrl":"http://x"}`)))

		raw, err := os.ReadFile(piWrittenPath(t))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := config["providers"].(map[string]any)["9router"].(map[string]any)["apiKey"]; got != "sk-active" {
			t.Errorf("expected the active dashboard key, got %v", got)
		}
	})
}

func TestPiSettingsDelete(t *testing.T) {
	t.Run("missing file answers success without writing", func(t *testing.T) {
		piTestHome(t)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleDelete(rec, httptest.NewRequest(http.MethodDelete, "/api/cli-tools/pi-settings", nil))
		if body := piBody(t, rec); body["message"] != "No config file to reset" {
			t.Fatalf("expected the no-config message, got %v", body)
		}
	})

	t.Run("only the 9router block is removed", func(t *testing.T) {
		piTestHome(t)
		path := piWriteConfig(t, filepath.Join(".pi", "models.json"),
			`{"providers":{"9router":{"baseUrl":"http://x/v1"},"other":{"baseUrl":"http://y/v1"}}}`)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleDelete(rec, httptest.NewRequest(http.MethodDelete, "/api/cli-tools/pi-settings", nil))

		if body := piBody(t, rec); body["message"] != "9Router removed from Pi" {
			t.Fatalf("expected removal message, got %v", body)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("parse: %v", err)
		}
		providers := config["providers"].(map[string]any)
		if _, ok := providers["9router"]; ok {
			t.Errorf("9router block survived: %v", providers)
		}
		if _, ok := providers["other"]; !ok {
			t.Errorf("foreign block was dropped: %v", providers)
		}
	})

	t.Run("an emptied providers map is removed with the block", func(t *testing.T) {
		piTestHome(t)
		path := piWriteConfig(t, filepath.Join(".pi", "models.json"),
			`{"providers":{"9router":{"baseUrl":"http://x/v1"}},"extra":true}`)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleDelete(rec, httptest.NewRequest(http.MethodDelete, "/api/cli-tools/pi-settings", nil))

		if body := piBody(t, rec); body["message"] != "9Router removed from Pi" {
			t.Fatalf("expected removal message, got %v", body)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, has := config["providers"]; has {
			t.Errorf("empty providers map survived: %v", config)
		}
		if config["extra"] != true {
			t.Errorf("sibling keys must survive: %v", config)
		}
	})
}

// piWrittenPath reads back the file the POST wrote: the agent path is the
// write target when no config existed yet.
func piWrittenPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return filepath.Join(home, ".pi", "agent", "models.json")
}

// piTestRepo opens a scratch database with the production schema and seeds one
// active and one inactive key, so the resolveAPIKey fallback has rows to pick
// from.
func piTestRepo(t *testing.T) *db.Repo {
	t.Helper()
	sqlDB, err := db.OpenDatabase(filepath.Join(t.TempDir(), "pi-test.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := dbtest.CreateTables(sqlDB); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	_, err = sqlDB.Exec(`INSERT INTO apiKeys (id, key, isActive, createdAt) VALUES
		('k-inactive', 'sk-inactive', 0, '2026-01-01'),
		('k-active', 'sk-active', 1, '2026-01-02')`)
	if err != nil {
		t.Fatalf("seed keys: %v", err)
	}
	return db.NewRepo(sqlDB)
}
