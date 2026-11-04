package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

// resetLiveCatalog clears the process-wide cache and restores the real endpoints
// so each case starts from a cold, production-shaped state.
func resetLiveCatalog(t *testing.T) {
	t.Helper()
	prevKiro, prevGrok, prevAnthro := kiroCatalogBaseURL, grokCLICatalogURL, anthropicCatalogURL
	liveCatalogStore.mu.Lock()
	liveCatalogStore.entries = make(map[string]liveCatalogEntry)
	liveCatalogStore.mu.Unlock()
	t.Cleanup(func() {
		kiroCatalogBaseURL, grokCLICatalogURL, anthropicCatalogURL = prevKiro, prevGrok, prevAnthro
		liveCatalogStore.mu.Lock()
		liveCatalogStore.entries = make(map[string]liveCatalogEntry)
		liveCatalogStore.mu.Unlock()
	})
}

// TestHandleModels_KiroLiveCatalogReplacesStatic proves the live catalog wins
// over the static registry — that is what makes upstream publish `kr/auto`
// instead of the 44 registry entries.
func TestHandleModels_KiroLiveCatalogReplacesStatic(t *testing.T) {
	resetLiveCatalog(t)

	var gotPath, gotAuth, gotFingerprint string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotFingerprint = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"modelId":"auto","modelName":"Auto","rateMultiplier":1,"tokenLimits":{"maxInputTokens":300000}},{"modelId":"claude-sonnet-4","modelName":"Sonnet 4","rateMultiplier":1}]}`))
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	connData := `{"accessToken":"ya29.kiro-test","providerSpecificData":{"profileArn":"arn:aws:codewhisperer:eu-west-1:123456789012:profile/ABC"}}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-kiro-live', 'kiro', 'oauth', 'Kiro Live', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed kiro: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()
	ids := modelsIDs(t, h)
	joined := strings.Join(ids, "\n")

	if !strings.Contains(joined, "kr/auto") || !strings.Contains(joined, "kr/auto-thinking") {
		t.Errorf("live catalog models missing, got:\n%s", joined)
	}
	if !strings.Contains(joined, "kr/claude-sonnet-4-agentic") {
		t.Errorf("kiro agentic variant missing, got:\n%s", joined)
	}
	// Static-only entries must disappear once the live catalog takes over.
	if strings.Contains(joined, "kr/claude-opus-5") {
		t.Errorf("static registry entry leaked next to a live catalog, got:\n%s", joined)
	}
	if gotAuth != "Bearer ya29.kiro-test" {
		t.Errorf("expected bearer token, got %q", gotAuth)
	}
	if !strings.Contains(gotFingerprint, "KiroIDE-") {
		t.Errorf("expected Kiro IDE fingerprint UA, got %q", gotFingerprint)
	}
	if !strings.Contains(gotPath, "eu-west-1") {
		t.Errorf("expected region from profileArn in path, got %q", gotPath)
	}
}

// Upstream only consults the live resolver when enabledModels is not pinned.
func TestHandleModels_EnabledModelsSkipLiveCatalog(t *testing.T) {
	resetLiveCatalog(t)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"modelId":"auto"}]}`))
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	connData := `{"accessToken":"ya29.kiro-test","providerSpecificData":{"enabledModels":["pinned-model"]}}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-kiro-pinned', 'kiro', 'oauth', 'Kiro Pinned', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed kiro: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()
	joined := strings.Join(modelsIDs(t, h), "\n")

	if called {
		t.Error("live catalog must not be fetched when enabledModels is pinned")
	}
	if !strings.Contains(joined, "kr/pinned-model") {
		t.Errorf("pinned model must be listed, got:\n%s", joined)
	}
}

// A live catalog failure must fall back to the static registry, never to an
// empty provider.
func TestHandleModels_LiveCatalogFailureFallsBackToStatic(t *testing.T) {
	resetLiveCatalog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-kiro-dead', 'kiro', 'oauth', 'Kiro Dead', 1, 1, '{"accessToken":"ya29.dead"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed kiro: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()
	joined := strings.Join(modelsIDs(t, h), "\n")
	if !strings.Contains(joined, "kr/claude-opus-5") {
		t.Errorf("static catalog must be used when the live catalog fails, got:\n%s", joined)
	}
}

// Grok CLI publishes only what the account may use.
func TestHandleModels_GrokCLILiveCatalog(t *testing.T) {
	resetLiveCatalog(t)

	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"grok-4.7","context_length":256000}]}`))
	}))
	defer srv.Close()
	grokCLICatalogURL = srv.URL + "/v1/models"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	connData := `{"accessToken":"xai-test-token","providerSpecificData":{"email":"dev@example.com"}}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-grok-live', 'grok-cli', 'oauth', 'Grok Live', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed grok-cli: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()
	joined := strings.Join(modelsIDs(t, h), "\n")

	if !strings.Contains(joined, "gcli/grok-4.7") {
		t.Errorf("live grok model missing, got:\n%s", joined)
	}
	if strings.Contains(joined, "gcli/grok-4.5-high") {
		t.Errorf("static grok entries must be replaced by the live catalog, got:\n%s", joined)
	}
	if gotHeaders.Get("x-grok-client-identifier") != grokCLIIdentifier {
		t.Errorf("expected grok client identifier header, got %q", gotHeaders.Get("x-grok-client-identifier"))
	}
	if gotHeaders.Get("x-email") != "dev@example.com" {
		t.Errorf("expected x-email header from providerSpecificData, got %q", gotHeaders.Get("x-email"))
	}
}

// Anthropic live catalog lists models from the live catalog with the proper
// x-api-key and anthropic-version headers.
func TestHandleModels_AnthropicLiveCatalog(t *testing.T) {
	resetLiveCatalog(t)

	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"claude-opus-5-5-20260101","name":"Claude Opus 5.5"},{"id":"claude-sonnet-5-5-20260101","name":"Claude Sonnet 5.5"}]}`))
	}))
	defer srv.Close()
	anthropicCatalogURL = srv.URL + "/v1/models"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	connData := `{"apiKey":"anthropic-test-key","providerSpecificData":{}}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-anthropic-live', 'anthropic', 'apikey', 'Anthropic Live', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed anthropic: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()
	joined := strings.Join(modelsIDs(t, h), "\n")

	if !strings.Contains(joined, "anthropic/claude-opus-5-5-20260101") {
		t.Errorf("live anthropic model missing, got:\n%s", joined)
	}
	if !strings.Contains(joined, "anthropic/claude-sonnet-5-5-20260101") {
		t.Errorf("live anthropic model missing, got:\n%s", joined)
	}
	if gotHeaders.Get("x-api-key") != "anthropic-test-key" {
		t.Errorf("expected x-api-key header, got %q", gotHeaders.Get("x-api-key"))
	}
	if gotHeaders.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("expected anthropic-version header, got %q", gotHeaders.Get("anthropic-version"))
	}
}

// A live catalog failure must fall back to the static registry, never to an
// empty provider.
func TestHandleModels_AnthropicLiveCatalogFallsBackToStatic(t *testing.T) {
	resetLiveCatalog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	anthropicCatalogURL = srv.URL + "/v1/models"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-anthropic-dead', 'anthropic', 'apikey', 'Anthropic Dead', 1, 1, '{"apiKey":"anthropic-dead-key"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed anthropic: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()
	joined := strings.Join(modelsIDs(t, h), "\n")

	if !strings.Contains(joined, "anthropic/claude-sonnet-4-20250514") {
		t.Errorf("static catalog must be used when the live catalog fails, got:\n%s", joined)
	}
}

// Concurrency guard: a cold cache must not fan out once per request. Ten
// simultaneous /v1/models calls have to collapse into a single upstream fetch.
func TestHandleModels_LiveCatalogCoalescesConcurrentRequests(t *testing.T) {
	resetLiveCatalog(t)

	var hits atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release // hold the first fetch so every caller piles up behind it
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"modelId":"auto"}]}`))
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-kiro-storm', 'kiro', 'oauth', 'Kiro Storm', 1, 1, '{"accessToken":"ya29.storm"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed kiro: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()

	const callers = 10
	ids := make(chan []string, callers)
	for i := 0; i < callers; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/v1/models", nil)
			rec := httptest.NewRecorder()
			h.HandleModels(rec, req)
			ids <- modelsIDsFromBody(rec.Body.Bytes())
		}()
	}
	// Let every goroutine reach the resolver before the single fetch completes.
	time.Sleep(150 * time.Millisecond)
	close(release)

	for i := 0; i < callers; i++ {
		if got := <-ids; !strings.Contains(strings.Join(got, "\n"), "kr/auto") {
			t.Fatalf("caller %d did not receive the live catalog: %v", i, got)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("expected exactly 1 upstream fetch for %d concurrent callers, got %d", callers, n)
	}
}

// modelsIDsFromBody decodes a /v1/models response body into model ids.
func modelsIDsFromBody(body []byte) []string {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids
}
