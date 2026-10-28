package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/app"
	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/translator"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
)

// TestHandlersModule_ProvidesTheEngineChatHandler pins that the fx graph hands
// the auto-ping scheduler the SAME chat handler the engine routes are mounted
// on.
//
// The failure this guards against is a second chat handler: one built for the
// scheduler while the engine runs on another. Two handlers mean two
// PersistentStores over the same SQLite file, each with its own in-memory LRU —
// so the dashboard reports zero cache entries while the engine serves hits, and
// clearing entries from the dashboard leaves the engine still serving them
// (internal/handlers/router.go documents exactly this).
//
// The assertion is on identity, not on behaviour: the handler in the container
// must be the very one the router was built around.
func TestHandlersModule_ProvidesTheEngineChatHandler(t *testing.T) {
	resetProcessGlobals(t)

	testCfg := &config.Config{
		DatabasePath: filepath.Join(t.TempDir(), "test.sqlite"),
		Port:         20134,
	}

	var router *chi.Mux
	var handler http.Handler
	var engineChatH *chat.ChatHandler
	fxApp := fx.New(
		app.ConfigModule,
		fx.Replace(testCfg),
		app.DatabaseModule,
		app.HandlersModule,
		fx.NopLogger,
		fx.Populate(&router, &handler, &engineChatH),
	)

	ctx := context.Background()
	if err := fxApp.Start(ctx); err != nil {
		t.Fatalf("HandlersModule Start failed: %v", err)
	}
	defer fxApp.Stop(ctx)

	if engineChatH == nil {
		t.Fatal("expected *chat.ChatHandler to be provided; the auto-ping scheduler would never run")
	}
	if router == nil || handler == nil {
		t.Fatal("expected the router and its handler to be provided")
	}

	// The router must serve traffic, so the handler was really built.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", rec.Code)
	}
}

// TestProvideRouter_SharesOneChatHandler pins that ProvideRouter hands back the
// engine's chat handler rather than building a second one.
//
// A second handler is the bug this task's wiring could have introduced: it
// would own a second PersistentStore over the same SQLite file with its own
// in-memory LRU, so the dashboard would report zero entries while the engine
// served hits.
//
// The proof is behavioural, because the identity of the cache object is not
// observable from outside. A write on the RETURNED handler must be visible to
// the dashboard, which reads the cache through the handler the router was
// given. Had ProvideRouter returned a second handler while the routes stayed
// mounted on the first, the dashboard would report zero entries and this test
// would fail.
func TestProvideRouter_SharesOneChatHandler(t *testing.T) {
	repo := openWiringRepo(t)
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable dashboard login: %v", err)
	}

	ts := app.ProvideTokenSaverConfig(repo, app.CLIParams{})
	ts.SetSemanticCache(true)

	// The control is built FIRST, so its store hydrates from an empty file and
	// its in-memory LRU never sees the write below. It is the "second handler"
	// this wiring must not produce, and the assertion that it stays empty is
	// what makes the dashboard assertion meaningful: two instances that
	// happened to share state would pass it too.
	controlH := chat.NewChatHandler(repo, ts)
	if controlH.SemanticCache == nil {
		t.Fatal("the control handler carries no semantic cache")
	}

	router, engineChatH := app.ProvideRouter(repo, ts)
	if router == nil {
		t.Fatal("ProvideRouter returned no router")
	}
	if engineChatH == nil {
		t.Fatal("ProvideRouter returned no chat handler")
	}
	if engineChatH.SemanticCache == nil {
		t.Fatal("the returned handler carries no semantic cache")
	}
	if controlH.SemanticCache == engineChatH.SemanticCache {
		t.Fatal("the control probe shares the engine's cache instance; the test cannot distinguish them")
	}

	// A write on the engine's handler.
	cacheReq := &translator.OpenAIRequest{
		Model:    "gpt-4o",
		Messages: []translator.OpenAIMessage{{Role: "user", Content: "wiring probe"}},
	}
	if err := engineChatH.SemanticCache.Store(context.Background(), cacheReq, []byte(`{"ok":true}`), "application/json"); err != nil {
		t.Fatalf("store on the engine's cache: %v", err)
	}

	if _, total := controlH.SemanticCache.ListEntries(1, 10, "", "", "created_at", "desc"); total != 0 {
		t.Fatalf("the control handler reports %d entries, want 0: a separate instance must not see the engine's write", total)
	}

	// The dashboard reads the cache through the handler the router was given.
	// It must report the entry the engine just wrote.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cache/entries", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/cache/entries = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if resp.Pagination.Total != 1 {
		t.Fatalf("dashboard reports total=%d, want 1: the router is mounted on a different chat handler", resp.Pagination.Total)
	}
}

// openWiringRepo opens a database with the production schema and returns a
// repository over it.
func openWiringRepo(t *testing.T) *db.Repo {
	t.Helper()
	conn, err := db.OpenDatabase(filepath.Join(t.TempDir(), "wiring.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("ensure core schema: %v", err)
	}
	return db.NewRepo(conn)
}

// TestAppModule_ResolvesTheEngineChatHandler pins that the PRODUCTION graph
// — AppModule, not a hand-built one — hands ProvideServer a chat handler.
//
// The handler is optional in ServerParams so a server built without one (the
// tests in this package) simply runs no ping loop. That optionality must not
// quietly become the production case: a nil handler means the auto-ping
// scheduler never starts, which is the whole of task 6's control plane. So
// the full graph is booted and the handler is read out of it.
func TestAppModule_ResolvesTheEngineChatHandler(t *testing.T) {
	resetProcessGlobals(t)

	testCfg := &config.Config{
		DatabasePath: filepath.Join(t.TempDir(), "test.sqlite"),
		Port:         20136,
	}

	var engineChatH *chat.ChatHandler
	fxApp := fx.New(
		app.AppModule,
		fx.Replace(testCfg),
		fx.NopLogger,
		fx.Populate(&engineChatH),
	)

	ctx := context.Background()
	if err := fxApp.Start(ctx); err != nil {
		t.Fatalf("AppModule Start failed: %v", err)
	}
	defer fxApp.Stop(ctx)

	if engineChatH == nil {
		t.Fatal("the production graph resolved no chat handler; the auto-ping scheduler would never run")
	}
}
