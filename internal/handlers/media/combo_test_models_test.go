package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
)

func TestHandleComboTestModels(t *testing.T) {
	// Setup test mock server that answers 200 for good-model and 500 for bad-model
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "bad-model") {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"Model failed"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupMediaTestDB(t)
	defer cleanup()

	p1 := 1
	if err := database.CreateProviderConnectionFull("conn-1", "testprov", "apikey", "testconn", &p1, `{"apiKey":"sk-1","baseUrl":"`+srv.URL+`"}`); err != nil {
		t.Fatal(err)
	}

	if err := database.CreateCombo("test-combo", "test-combo", "llm", `["testprov/good-model", "testprov/bad-model"]`, "fallback"); err != nil {
		t.Fatal(err)
	}

	chatH := chat.NewChatHandler(database)
	handler := NewMediaHandler(database, nil, chatH)
	r := chi.NewRouter()
	r.Post("/api/combos/{id}/test-models", handler.HandleComboTestModels)

	t.Run("tests models for an existing combo and computes dead/alive summary", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/combos/test-combo/test-models", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp comboTestModelsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		if resp.Summary.Total != 2 {
			t.Errorf("expected 2 total models, got %d", resp.Summary.Total)
		}
		if resp.Summary.Alive != 1 {
			t.Errorf("expected 1 alive model, got %d", resp.Summary.Alive)
		}
		if resp.Summary.Dead != 1 {
			t.Errorf("expected 1 dead model, got %d", resp.Summary.Dead)
		}
		if len(resp.Results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(resp.Results))
		}
		if !resp.Results[0].OK {
			t.Errorf("expected first model OK, got error: %s", resp.Results[0].Error)
		}
		if resp.Results[1].OK {
			t.Errorf("expected second model to fail")
		}
	})

	t.Run("supports draft testing by providing models in the body directly", func(t *testing.T) {
		reqBody := `{"models":["testprov/good-model"],"kind":"llm"}`
		req := httptest.NewRequest(http.MethodPost, "/api/combos/draft/test-models", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp comboTestModelsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		if resp.Summary.Alive != 1 || resp.Summary.Dead != 0 {
			t.Errorf("expected 1 alive and 0 dead, got %+v", resp.Summary)
		}
	})

	t.Run("returns 404 when combo not found and no draft models", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/combos/non-existent/test-models", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("returns 400 when no models in request or combo", func(t *testing.T) {
		_ = database.CreateCombo("empty-combo", "empty-combo", "llm", `[]`, "fallback")
		req := httptest.NewRequest(http.MethodPost, "/api/combos/empty-combo/test-models", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})
}

func setupMediaTestDB(t testing.TB) (*db.Repo, func()) {
	database, cleanup := setupEmbeddingsTestDB(t.(*testing.T))
	_, err := database.Exec(`CREATE TABLE IF NOT EXISTS combos (
		id TEXT PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		kind TEXT,
		models TEXT NOT NULL,
		strategy TEXT DEFAULT 'fallback',
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL,
		matchRules TEXT DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("failed to create combos table: %v", err)
	}
	return db.NewRepo(database), cleanup
}
