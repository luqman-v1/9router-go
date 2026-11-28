package media_test

import (
	"database/sql"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"9router/proxy/internal/handlers"
	"9router/proxy/internal/handlers/chat"
)

func setupMultimodalEmbeddingsRouterDB(t *testing.T) (*sql.DB, *db.Repo, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_mm_emb_router_*.sqlite")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	tmpFile.Close()
	database, err := db.OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("open db: %v", err)
	}
	if err := dbtest.CreateTables(database); err != nil {
		database.Close()
		os.Remove(tmpFile.Name())
		t.Fatalf("create tables: %v", err)
	}
	repo := db.NewRepo(database)
	if err := repo.CreateApiKey("mm-emb-key", "test-api-key", "MM Emb Test", "m"); err != nil {
		database.Close()
		os.Remove(tmpFile.Name())
		t.Fatalf("create api key: %v", err)
	}
	cleanup := func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}
	return database, repo, cleanup
}

func TestSetupServerRouter_MultimodalEmbeddingsAlias(t *testing.T) {
	chat.SetUsageWritesSynchronousForTest(true)
	defer chat.SetUsageWritesSynchronousForTest(false)

	database, repo, cleanup := setupMultimodalEmbeddingsRouterDB(t)
	defer cleanup()

	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("failed to parse upstream body: %v", err)
		}
		if model, _ := req["model"].(string); model != "text-embedding-ada-002" {
			t.Errorf("expected model text-embedding-ada-002, got %v", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer upstream.Close()

	data, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-mm-emb', 'openai', 'apikey', 'OpenAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	r := chi.NewRouter()
	handlers.SetupServerRouter(r, repo, nil)

	cases := []struct {
		name string
		path string
	}{
		{name: "v1 prefixed", path: "/v1/multimodal-embeddings"},
		{name: "bare", path: "/multimodal-embeddings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"openai/text-embedding-ada-002","input":"hello"}`
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-api-key")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("parse response: %v", err)
			}
			if resp["object"] != "list" {
				t.Errorf("expected object 'list', got %v", resp["object"])
			}
		})
	}

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM usageHistory`).Scan(&count); err != nil {
		t.Fatalf("count usageHistory: %v", err)
	}
	if count != len(cases) {
		t.Fatalf("usageHistory rows = %d, want %d", count, len(cases))
	}
	if hits := upstreamHits.Load(); hits != int32(len(cases)) {
		t.Fatalf("upstream hits = %d, want %d", hits, len(cases))
	}
}
