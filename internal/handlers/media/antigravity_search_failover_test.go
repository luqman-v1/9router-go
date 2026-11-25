package media

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/providers"
)

func TestHandleSearch_AntigravityFailoverOnValidationRequired(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"reason":"VALIDATION_REQUIRED"}]}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com/x","title":"X"}}],"groundingSupports":[{"segment":{"text":"ok","startIndex":0,"endIndex":2},"groundingChunkIndices":[0]}]}}]}}`))
	}))
	defer upstream.Close()

	ag := providers.KnownProviders["antigravity"]
	origBase := ag.BaseURL
	ag.BaseURL = upstream.URL
	providers.KnownProviders["antigravity"] = ag
	defer func() {
		ag.BaseURL = origBase
		providers.KnownProviders["antigravity"] = ag
	}()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	for i, id := range []string{"conn-ag-1", "conn-ag-2"} {
		connData, _ := json.Marshal(map[string]any{"apiKey": "ag-token-" + id, "projectId": "test-project-id"})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, 'antigravity', 'oauth', 'AG', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, i+1, string(connData)); err != nil {
			t.Fatalf("seed connection: %v", err)
		}
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(`{"model":"ag","query":"latest AI news"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d: %s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected 2 upstream calls (failover), got %d", calls)
	}
	// The attempt locks both the normalized key and the raw request model.
	lockedRaw, err := repo.IsConnectionModelLocked("conn-ag-1", "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("lock check: %v", err)
	}
	if !lockedRaw {
		t.Errorf("expected first account locked for gemini-2.5-flash after 403 VALIDATION_REQUIRED")
	}
}

func TestHandleSearch_XquikFailover(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tweets":[{"id":"1","text":"hi","createdAt":"2026-01-01T00:00:00Z"}],"has_next_page":false,"next_cursor":""}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	for i, id := range []string{"conn-xq-1", "conn-xq-2"} {
		connData, _ := json.Marshal(map[string]any{"apiKey": "k-" + id, "baseUrl": upstream.URL})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, 'xquik', 'apikey', 'XQ', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, i+1, string(connData)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"query":"hello","max_results":5}`
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := handler.handleXquikSearch(rec, req, []byte(body), &chat.ModelInfo{Provider: "xquik", Model: "xquik"}); err != nil {
		t.Fatalf("expected failover success, got %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", calls)
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := res["answer"]; !ok {
		t.Errorf("expected answer key in envelope, got %v", res)
	}
	metrics, _ := res["metrics"].(map[string]any)
	if metrics["response_time_ms"] == nil || metrics["upstream_latency_ms"] == nil {
		t.Errorf("expected timing metrics, got %v", metrics)
	}
	locked, err := repo.IsConnectionModelLocked("conn-xq-1", "xquik")
	if err != nil {
		t.Fatalf("lock check: %v", err)
	}
	if !locked {
		t.Errorf("expected first xquik account locked after 429")
	}
}

func TestHandleSearch_AntigravityHonorsPinnedConnectionHeader(t *testing.T) {
	var conn1Hits, conn2Hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer ag-token-conn-ag-1":
			conn1Hits++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"rate limited"}}`))
		case "Bearer ag-token-conn-ag-2":
			conn2Hits++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()

	ag := providers.KnownProviders["antigravity"]
	origBase := ag.BaseURL
	ag.BaseURL = upstream.URL
	providers.KnownProviders["antigravity"] = ag
	t.Cleanup(func() {
		ag.BaseURL = origBase
		providers.KnownProviders["antigravity"] = ag
	})

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	for i, id := range []string{"conn-ag-1", "conn-ag-2"} {
		connData, _ := json.Marshal(map[string]any{"apiKey": "ag-token-" + id, "projectId": "test-project-id"})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, 'antigravity', 'oauth', 'AG', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, i+1, string(connData)); err != nil {
			t.Fatalf("seed connection: %v", err)
		}
	}

	handler := newTestMediaHandler(db.NewRepo(database))

	t.Run("selects_pinned_second_connection", func(t *testing.T) {
		conn1Hits, conn2Hits = 0, 0
		req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"model":"ag","query":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Connection-ID", "conn-ag-2")
		rec := httptest.NewRecorder()
		handler.HandleSearch(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for pinned conn-ag-2, got %d: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits != 0 || conn2Hits != 1 {
			t.Fatalf("hits = (%d, %d), want (0, 1)", conn1Hits, conn2Hits)
		}
	})

	t.Run("does_not_rotate_away_from_pinned_connection_on_429", func(t *testing.T) {
		conn1Hits, conn2Hits = 0, 0
		req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"model":"ag","query":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Connection-ID", "conn-ag-1")
		rec := httptest.NewRecorder()
		handler.HandleSearch(rec, req)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 for pinned conn-ag-1, got %d: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits != 1 || conn2Hits != 0 {
			t.Fatalf("hits = (%d, %d), want (1, 0)", conn1Hits, conn2Hits)
		}
	})
}

func TestHandleSearch_XquikHonorsPinnedConnectionHeader(t *testing.T) {
	var conn1Hits, conn2Hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("x-api-key") {
		case "k-conn-xq-1":
			conn1Hits++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
		case "k-conn-xq-2":
			conn2Hits++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tweets":[{"id":"1","text":"hi","createdAt":"2026-01-01T00:00:00Z"}],"has_next_page":false,"next_cursor":""}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	for i, id := range []string{"conn-xq-1", "conn-xq-2"} {
		connData, _ := json.Marshal(map[string]any{"apiKey": "k-" + id, "baseUrl": upstream.URL})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, 'xquik', 'apikey', 'XQ', ?, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, i+1, string(connData)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	handler := newTestMediaHandler(db.NewRepo(database))
	body := `{"query":"hello","max_results":5}`

	t.Run("selects_pinned_second_connection", func(t *testing.T) {
		conn1Hits, conn2Hits = 0, 0
		req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Connection-ID", "conn-xq-2")
		rec := httptest.NewRecorder()
		if err := handler.handleXquikSearch(rec, req, []byte(body), &chat.ModelInfo{Provider: "xquik", Model: "xquik"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if conn1Hits != 0 || conn2Hits != 1 {
			t.Fatalf("hits = (%d, %d), want (0, 1)", conn1Hits, conn2Hits)
		}
	})

	t.Run("does_not_rotate_away_from_pinned_connection_on_429", func(t *testing.T) {
		conn1Hits, conn2Hits = 0, 0
		req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Connection-ID", "conn-xq-1")
		rec := httptest.NewRecorder()
		err := handler.handleXquikSearch(rec, req, []byte(body), &chat.ModelInfo{Provider: "xquik", Model: "xquik"})
		if err == nil {
			t.Fatal("expected error for pinned 429 connection")
		}
		if conn1Hits != 1 || conn2Hits != 0 {
			t.Fatalf("hits = (%d, %d), want (1, 0)", conn1Hits, conn2Hits)
		}
	})
}

func TestHandleSearch_RotatesPastEmptyAPIKeyConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tweets":[{"id":"1","text":"ok","createdAt":"2026-01-01T00:00:00Z"}],"has_next_page":false,"next_cursor":""}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-xq-empty', 'xquik', 'apikey', 'XQ Empty', 1, 1, '{"baseUrl":"` + upstream.URL + `"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-xq-valid', 'xquik', 'apikey', 'XQ Valid', 2, 1, '{"apiKey":"k-valid","baseUrl":"` + upstream.URL + `"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	handler := newTestMediaHandler(db.NewRepo(database))
	body := `{"query":"hello","max_results":5}`
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := handler.handleXquikSearch(rec, req, []byte(body), &chat.ModelInfo{Provider: "xquik", Model: "xquik"}); err != nil {
		t.Fatalf("expected failover past empty apiKey connection, got %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
