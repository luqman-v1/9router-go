package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
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

func TestHandleRerank_ConnectionRotation(t *testing.T) {
	const okPayload = `{"object":"list","data":[{"index":0,"relevance_score":0.99}]}`

	postRerank := func(handler *MediaHandler, headers map[string]string, probe bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"cohere/rerank-v3","query":"q","documents":["a"]}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if probe {
			req = req.WithContext(handlerutil.WithProbeContext(req.Context()))
		}
		rec := httptest.NewRecorder()
		handler.HandleRerank(rec, req)
		return rec
	}

	t.Run("rotates_on_429_and_locks_failed_connection", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-rr-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"message":"rate limit exceeded"}`))
			case "Bearer sk-rr-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okPayload))
			default:
				t.Errorf("unexpected auth header: %q", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-rr-1", "cohere", "sk-rr-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-rr-2", "cohere", "sk-rr-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postRerank(handler, nil, false)
		if rec.Code != http.StatusOK || rec.Body.String() != okPayload {
			t.Fatalf("first response = (%d, %s), want (200, %s)", rec.Code, rec.Body.String(), okPayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Cooled conn-rr-1 must be skipped on the follow-up request.
		rec2 := postRerank(handler, nil, false)
		if rec2.Code != http.StatusOK || rec2.Body.String() != okPayload {
			t.Fatalf("second response = (%d, %s), want (200, %s)", rec2.Code, rec2.Body.String(), okPayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("rotates_on_401_invalid_key", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-rr-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"invalid token"}`))
			case "Bearer sk-rr-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okPayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-rr-1", "cohere", "sk-rr-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-rr-2", "cohere", "sk-rr-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postRerank(handler, nil, false)
		if rec.Code != http.StatusOK || rec.Body.String() != okPayload {
			t.Fatalf("first response = (%d, %s), want (200, %s)", rec.Code, rec.Body.String(), okPayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("first hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postRerank(handler, nil, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("second status = %d, want 200", rec2.Code)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 2 {
			t.Errorf("second hits = (%d, %d), want (1, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("does_not_rotate_or_lock_on_non_retryable_400", func(t *testing.T) {
		const clientErr = `{"message":"invalid documents"}`
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-rr-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(clientErr))
			case "Bearer sk-rr-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okPayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-rr-1", "cohere", "sk-rr-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-rr-2", "cohere", "sk-rr-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postRerank(handler, nil, false)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != clientErr {
			t.Fatalf("first response = (%d, %s), want (400, %s)", rec.Code, rec.Body.String(), clientErr)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Fatalf("first hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}

		rec2 := postRerank(handler, nil, false)
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("second status = %d, want 400", rec2.Code)
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 0 {
			t.Errorf("second hits = (%d, %d), want (2, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("respects_pinned_x_connection_id_header", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-rr-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			case "Bearer sk-rr-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okPayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-rr-1", "cohere", "sk-rr-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-rr-2", "cohere", "sk-rr-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postRerank(handler, map[string]string{"x-connection-id": "conn-rr-1"}, false)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 0 {
			t.Errorf("pinned hits = (%d, %d), want (1, 0)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("returns_last_upstream_error_when_all_connections_exhausted", func(t *testing.T) {
		const quotaErr = `{"message":"quota exceeded"}`
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "Bearer sk-rr-1":
				conn1Hits.Add(1)
			case "Bearer sk-rr-2":
				conn2Hits.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(quotaErr))
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-rr-1", "cohere", "sk-rr-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-rr-2", "cohere", "sk-rr-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postRerank(handler, nil, false)
		if rec.Code != http.StatusTooManyRequests || rec.Body.String() != quotaErr {
			t.Fatalf("exhausted response = (%d, %s), want (429, %s)", rec.Code, rec.Body.String(), quotaErr)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Errorf("exhausted hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}
	})

	t.Run("probe_context_rotates_without_persisting_lock", func(t *testing.T) {
		var conn1Hits, conn2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Header.Get("Authorization") {
			case "Bearer sk-rr-1":
				conn1Hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"message":"quota exceeded"}`))
			case "Bearer sk-rr-2":
				conn2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(okPayload))
			}
		}))
		defer upstream.Close()

		database, cleanup := setupEmbeddingsTestDB(t)
		defer cleanup()
		seedJSONMediaConn(t, database, "conn-rr-1", "cohere", "sk-rr-1", upstream.URL, 1)
		seedJSONMediaConn(t, database, "conn-rr-2", "cohere", "sk-rr-2", upstream.URL, 2)
		handler := newTestMediaHandler(db.NewRepo(database))

		rec := postRerank(handler, nil, true)
		if rec.Code != http.StatusOK || rec.Body.String() != okPayload {
			t.Fatalf("probe response = (%d, %s), want (200, %s)", rec.Code, rec.Body.String(), okPayload)
		}
		if conn1Hits.Load() != 1 || conn2Hits.Load() != 1 {
			t.Fatalf("probe hits = (%d, %d), want (1, 1)", conn1Hits.Load(), conn2Hits.Load())
		}

		// Follow-up non-probe request must still try conn-rr-1 first.
		rec2 := postRerank(handler, nil, false)
		if rec2.Code != http.StatusOK {
			t.Fatalf("follow-up status = %d, want 200", rec2.Code)
		}
		if conn1Hits.Load() != 2 || conn2Hits.Load() != 2 {
			t.Errorf("follow-up hits = (%d, %d), want (2, 2)", conn1Hits.Load(), conn2Hits.Load())
		}
	})
}
