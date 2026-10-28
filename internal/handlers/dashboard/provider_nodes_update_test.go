package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// setupNodeTestDB extends the shared dashboard schema with the providerNodes
// table the node handlers read/write.
func setupNodeTestDB(t *testing.T) (*db.Repo, func()) {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	if _, err := repo.RawDB().Exec(`CREATE TABLE IF NOT EXISTS providerNodes (
		id TEXT PRIMARY KEY,
		type TEXT,
		name TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	)`); err != nil {
		cleanup()
		t.Fatalf("failed to create providerNodes: %v", err)
	}

	// The node rename moves usage and request history with it, so the fixture
	// needs the two tables the production schema always has.
	for _, query := range []string{
		// usageHistory mirrors the production shape rather than the two columns a
		// rename happens to move: the node handlers also sum tokens off it (a custom
		// provider's lifetime quota is measured from promptTokens/completionTokens),
		// and a fixture missing them fails on the query shape instead of on the
		// behaviour the test is about.
		`CREATE TABLE IF NOT EXISTS usageHistory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			provider TEXT,
			model TEXT,
			connectionId TEXT,
			apiKey TEXT,
			endpoint TEXT,
			promptTokens INTEGER DEFAULT 0,
			completionTokens INTEGER DEFAULT 0,
			cost REAL,
			status TEXT,
			tokens TEXT,
			meta TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS requestDetails (
			id TEXT PRIMARY KEY,
			timestamp TEXT NOT NULL,
			provider TEXT,
			model TEXT,
			connectionId TEXT,
			status TEXT,
			data TEXT NOT NULL
		);`,
	} {
		if _, err := repo.RawDB().Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create history table: %v", err)
		}
	}
	return repo, cleanup
}

func putNode(t *testing.T, router http.Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/provider-nodes/"+id, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHandleUpdateProviderNode_Validation(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	node, err := repo.CreateProviderNode("openai-compatible-chat-abc", "openai-compatible", "Old", `{"prefix":"oc","apiType":"chat","baseUrl":"https://a.example.com/v1"}`)
	if err != nil || node == nil {
		t.Fatalf("seed node: %v", err)
	}

	// Missing name.
	rec := putNode(t, router, node.ID, `{"prefix":"oc","apiType":"chat","baseUrl":"https://a.example.com/v1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing name, got %d: %s", rec.Code, rec.Body.String())
	}

	// Invalid apiType for openai-compatible.
	rec = putNode(t, router, node.ID, `{"name":"N","prefix":"oc","apiType":"bogus","baseUrl":"https://a.example.com/v1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid apiType, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unknown node.
	rec = putNode(t, router, "openai-compatible-chat-missing", `{"name":"N","prefix":"oc","apiType":"chat","baseUrl":"https://a.example.com/v1"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown node, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateProviderNode_SavesAndSyncsConnections(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	node, err := repo.CreateProviderNode("openai-compatible-chat-abc", "openai-compatible", "Old", `{"prefix":"oc","apiType":"chat","baseUrl":"https://a.example.com/v1"}`)
	if err != nil || node == nil {
		t.Fatalf("seed node: %v", err)
	}
	connData := `{"apiKey":"sk-x","providerSpecificData":{"prefix":"oc","apiType":"chat","baseUrl":"https://a.example.com/v1","nodeName":"Old"}}`
	if _, err := repo.RawDB().Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'openai-compatible-chat-abc', 'apikey', 'K', 1, 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	rec := putNode(t, router, node.ID, `{"name":"New","prefix":"oc2","apiType":"responses","baseUrl":"https://b.example.com/v1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Node ProviderNodeResponse `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Node.Name != "New" || out.Node.Prefix != "oc2" || out.Node.APIType != "responses" || out.Node.BaseURL != "https://b.example.com/v1" {
		t.Fatalf("unexpected node payload: %+v", out.Node)
	}

	conn, err := repo.GetProviderConnectionByID("conn-1")
	if err != nil || conn == nil {
		t.Fatalf("reload connection: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("decode conn data: %v", err)
	}
	psd, _ := data["providerSpecificData"].(map[string]any)
	if psd["prefix"] != "oc2" || psd["apiType"] != "responses" || psd["baseUrl"] != "https://b.example.com/v1" || psd["nodeName"] != "New" {
		t.Fatalf("connection not synced: %v", psd)
	}
}

func TestHandleUpdateProviderNode_AnthropicStripsMessages(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	node, err := repo.CreateProviderNode("anthropic-compatible-abc", "anthropic-compatible", "AC", `{"prefix":"ac","baseUrl":"https://ac.example.com/v1"}`)
	if err != nil || node == nil {
		t.Fatalf("seed node: %v", err)
	}

	// apiType is ignored for anthropic nodes; /messages suffix is stripped.
	rec := putNode(t, router, node.ID, `{"name":"AC2","prefix":"ac","apiType":"responses","baseUrl":"https://ac.example.com/v1/messages"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Node ProviderNodeResponse `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Node.BaseURL != "https://ac.example.com/v1" {
		t.Fatalf("expected sanitized baseUrl, got %q", out.Node.BaseURL)
	}
}

func TestHandleGetConnectionModels_CompatibleProbe(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	connData := `{"apiKey":"sk-x","providerSpecificData":{"prefix":"oc","apiType":"chat","baseUrl":"http://upstream.example/v1"}}`
	if _, err := repo.RawDB().Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-oc', 'openai-compatible-chat-abc', 'apikey', 'K', 1, 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	calls := withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
		if p.url != "http://upstream.example/v1/models" {
			t.Errorf("unexpected probe url %q", p.url)
		}
		if p.headers.Get("Authorization") != "Bearer sk-x" {
			t.Errorf("unexpected auth %q", p.headers.Get("Authorization"))
		}
		return http.StatusOK, []byte(`{"data":[{"id":"gpt-4o-mini"}]}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/providers/conn-oc/models", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Models) != 1 || out.Models[0]["id"] != "gpt-4o-mini" {
		t.Fatalf("unexpected models: %v", out.Models)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 probe, got %d", len(*calls))
	}
}

func TestHandleGetConnectionModels_NonCompatibleRejected(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	if _, err := repo.RawDB().Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-oa', 'openai', 'apikey', 'K', 1, 1, '{"apiKey":"sk-x"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/providers/conn-oa/models", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetConnectionModels_Antigravity(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	if _, err := repo.RawDB().Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag-test', 'antigravity', 'oauth', 'AG', 1, 1, '{"accessToken":"ya29.fake","providerSpecificData":{"projectId":"ag-proj"}}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	calls := withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
		if !strings.Contains(p.url, ":fetchAvailableModels") {
			t.Errorf("unexpected probe url %q", p.url)
		}
		if p.headers.Get("Authorization") != "Bearer ya29.fake" {
			t.Errorf("unexpected auth %q", p.headers.Get("Authorization"))
		}
		fakeResp := `{
			"models": {
				"gemini-2.5-flash": {
					"displayName": "Gemini 2.5 Flash",
					"supportsImages": true,
					"supportsThinking": true,
					"isInternal": false
				},
				"chat_internal_1": {
					"displayName": "Internal Chat",
					"isInternal": true
				}
			}
		}`
		return http.StatusOK, []byte(fakeResp), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/providers/conn-ag-test/models", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Provider string           `json:"provider"`
		Models   []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Provider != "antigravity" {
		t.Errorf("expected antigravity, got %s", out.Provider)
	}
	if len(out.Models) != 1 || out.Models[0]["id"] != "gemini-2.5-flash" {
		t.Fatalf("unexpected models: %v", out.Models)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 probe call, got %d", len(*calls))
	}
}
