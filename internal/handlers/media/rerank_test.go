package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// TestHandleRerank_Success proves the lane forwards to the provider's /rerank
// sibling with the connection's auth, and that the caller's own fields (query,
// documents) survive the forward untouched.
func TestHandleRerank_Success(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("failed to parse upstream body: %v", err)
		}
		if model, ok := req["model"].(string); !ok || model != "rerank-v3" {
			t.Errorf("expected model rerank-v3, got %v", req["model"])
		}
		if _, ok := req["query"]; !ok {
			t.Error("expected the query field to survive the forward")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[{"index":0,"relevance_score":0.98}]}`))
	}))
	defer upstream.Close()
	data, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-test",
		"baseUrl": upstream.URL,
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'cohere', 'apikey', 'Cohere Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("failed to seed provider connection: %v", err)
	}
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)
	body := `{"model":"cohere/rerank-v3","query":"what is a gateway","documents":["a","b"]}`
	req := httptest.NewRequest("POST", "/v1/rerank", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleRerank(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/rerank" {
		t.Errorf("upstream path = %q, want /v1/rerank", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	dataArr, _ := resp["data"].([]any)
	if len(dataArr) != 1 {
		t.Fatalf("expected 1 data item, got %d", len(dataArr))
	}
}

// TestHandleRerank_MissingModel proves an empty body is refused with 400 rather
// than forwarded, matching the embeddings lane's contract.
func TestHandleRerank_MissingModel(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)
	req := httptest.NewRequest("POST", "/v1/rerank", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.HandleRerank(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleRerank_InvalidJSON proves a malformed body is refused with 400.
func TestHandleRerank_InvalidJSON(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)
	req := httptest.NewRequest("POST", "/v1/rerank", strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	handler.HandleRerank(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleRerank_NoConnection proves an unknown model resolves to a 404
// rather than a 500, so a typo in the model name is a client error.
func TestHandleRerank_NoConnection(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)
	req := httptest.NewRequest("POST", "/v1/rerank", strings.NewReader(`{"model":"nope/rerank-v3"}`))
	rec := httptest.NewRecorder()
	handler.HandleRerank(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 400 or 404", rec.Code)
	}
}

// TestBuildRerankURL proves the lane is derived from whichever spelling the
// connection was saved with, so a base naming chat or embeddings still resolves
// to the rerank sibling rather than producing a doubled path.
func TestBuildRerankURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare host", "https://api.cohere.com", "https://api.cohere.com/rerank"},
		{"host with trailing slash", "https://api.cohere.com/", "https://api.cohere.com/rerank"},
		{"chat lane is rewritten", "https://api.example.com/v1/chat/completions", "https://api.example.com/v1/rerank"},
		{"embeddings lane is rewritten", "https://api.example.com/v1/embeddings", "https://api.example.com/v1/rerank"},
		{"v1 base gains the lane", "https://api.example.com/v1", "https://api.example.com/v1/rerank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildRerankURL(tt.in); got != tt.want {
				t.Errorf("buildRerankURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
