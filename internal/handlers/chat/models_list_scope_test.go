package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// modelsListResponse is the wire shape of GET /v1/models including the new
// mode/connections metadata.
type modelsListResponse struct {
	Object      string `json:"object"`
	Mode        string `json:"mode"`
	Connections int    `json:"connections"`
	Data        []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
	Models []struct {
		ID string `json:"id"`
	} `json:"models"`
}

func fetchModels(t *testing.T, h *ChatHandler, query string) modelsListResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models"+query, nil)
	w := httptest.NewRecorder()
	h.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for %q, got %d: %s", query, w.Code, w.Body.String())
	}
	var resp modelsListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", query, err)
	}
	return resp
}

func (r modelsListResponse) ids() []string {
	out := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		out = append(out, m.ID)
	}
	return out
}

func (r modelsListResponse) idSet() map[string]bool {
	out := make(map[string]bool, len(r.Data))
	for _, m := range r.Data {
		out[m.ID] = true
	}
	return out
}

// TestHandleModels_DefaultModePreservesUpstreamCatalog proves the default
// request is untouched: with no connection rows the full static catalog is
// still dumped, so existing clients keep seeing every candidate model.
func TestHandleModels_DefaultModePreservesUpstreamCatalog(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	resp := fetchModels(t, h, "")

	if resp.Object != "list" {
		t.Errorf("object = %q, want %q", resp.Object, "list")
	}
	if resp.Mode != "all" {
		t.Errorf("mode = %q, want %q", resp.Mode, "all")
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected the default mode to dump the static catalog")
	}
	// The whole point of the catalog dump: a provider with no connection and no
	// credentials still appears by id so clients can discover the prefix.
	joined := strings.Join(resp.ids(), "\n")
	if !strings.Contains(joined, "kiro/") {
		t.Errorf("default mode should still list unconnected kiro models, got:\n%s", firstLines(joined, 40))
	}
	// data and models stay the same slice — clients rely on the legacy key.
	if len(resp.Models) != len(resp.Data) {
		t.Errorf("models(%d) must mirror data(%d)", len(resp.Models), len(resp.Data))
	}
}

// TestHandleModels_ConnectedModeFreshInstall covers the reported confusion: a
// fresh install with zero connections. ?connected=1 must return only the
// noAuth subset, never the 1300-entry catalog.
func TestHandleModels_ConnectedModeFreshInstall(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	all := fetchModels(t, h, "")
	connected := fetchModels(t, h, "?connected=1")

	if connected.Mode != "connected" {
		t.Errorf("mode = %q, want %q", connected.Mode, "connected")
	}
	if connected.Connections != 0 {
		t.Errorf("connections = %d, want 0 on a fresh install", connected.Connections)
	}
	if len(connected.Data) >= len(all.Data) {
		t.Fatalf("connected mode (%d) must be a strict subset of the catalog (%d)",
			len(connected.Data), len(all.Data))
	}

	// Every returned model must belong to a noAuth provider.
	for _, m := range connected.Data {
		prefix := m.ID
		if idx := strings.Index(m.ID, "/"); idx >= 0 {
			prefix = m.ID[:idx]
		}
		if !providers.IsNoAuthProvider(prefix) {
			t.Errorf("connected mode listed %q whose provider is not noAuth", m.ID)
		}
	}
	// A known noAuth provider must survive, otherwise the filter is too tight.
	joined := strings.Join(connected.ids(), "\n")
	if !strings.Contains(joined, "oc/") {
		t.Errorf("expected noAuth opencode models in connected mode, got:\n%s", firstLines(joined, 20))
	}
}

// TestHandleModels_ConnectedModeIncludesActiveConnections verifies that a
// connected provider's models are listed even though it is not noAuth.
func TestHandleModels_ConnectedModeIncludesActiveConnections(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-kiro-1', 'kiro', 'oauth', 'Kiro Account', 1, 1, '{"apiKey":"tok"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed kiro: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	connected := fetchModels(t, h, "?connected=1")

	if connected.Connections < 1 {
		t.Errorf("connections = %d, want at least 1 with an active kiro row", connected.Connections)
	}
	joined := strings.Join(connected.ids(), "\n")
	if !strings.Contains(joined, "kr/") {
		t.Errorf("expected connected kiro models, got:\n%s", firstLines(joined, 20))
	}
	if strings.Contains(joined, "nvidia/") || strings.Contains(joined, "nv/") {
		t.Errorf("connected mode leaked an unconnected provider:\n%s", firstLines(joined, 20))
	}
}

// TestHandleModels_ConnectedModeExcludesInactiveConnections confirms an
// isActive=0 row is treated as unusable.
func TestHandleModels_ConnectedModeExcludesInactiveConnections(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-kiro-off', 'kiro', 'oauth', 'Kiro Paused', 1, 0, '{"apiKey":"tok"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed inactive kiro: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	connected := fetchModels(t, h, "?connected=1")
	joined := strings.Join(connected.ids(), "\n")
	if strings.Contains(joined, "kr/") {
		t.Errorf("inactive connection must not surface its provider:\n%s", firstLines(joined, 20))
	}
}

// TestHandleModels_AllModeForcesCatalogWithConnections proves ?all=1 dumps the
// catalog even when connections exist, so discovery stays available.
func TestHandleModels_AllModeForcesCatalogWithConnections(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-1', 'codex', 'oauth', 'Codex', 1, 1, '{"apiKey":"tok"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed codex: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	connected := fetchModels(t, h, "?connected=1")
	all := fetchModels(t, h, "?all=1")

	if all.Mode != "catalog" {
		t.Errorf("mode = %q, want %q", all.Mode, "catalog")
	}
	if len(all.Data) <= len(connected.Data) {
		t.Errorf("?all=1 (%d models) must exceed ?connected=1 (%d models)",
			len(all.Data), len(connected.Data))
	}
	joined := strings.Join(all.ids(), "\n")
	if !strings.Contains(joined, "kiro/") {
		t.Errorf("?all=1 should include unconnected kiro models, got:\n%s", firstLines(joined, 40))
	}
}

// TestHandleModels_ConnectedModeKeepsConnectedCustomModels makes sure the
// custom-model path honours the connected filter.
func TestHandleModels_ConnectedModeKeepsConnectedCustomModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-1', 'codex', 'oauth', 'Codex', 1, 1, '{"prefix":"cx","apiKey":"tok"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed codex: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'cx|my-custom|llm', ?)`,
		`{"providerAlias":"cx","id":"my-custom","type":"llm","name":"my-custom"}`); err != nil {
		t.Fatalf("seed custom cx: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'nvidia|ghost-model|llm', ?)`,
		`{"providerAlias":"nvidia","id":"ghost-model","type":"llm","name":"ghost-model"}`); err != nil {
		t.Fatalf("seed custom ghost: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	joined := strings.Join(fetchModels(t, h, "?connected=1").ids(), "\n")
	if !strings.Contains(joined, "cx/my-custom") {
		t.Errorf("expected cx/my-custom in connected mode, got:\n%s", firstLines(joined, 20))
	}
	if strings.Contains(joined, "ghost-model") {
		t.Errorf("connected mode leaked a custom model for an unconnected provider:\n%s",
			firstLines(joined, 20))
	}
}

// TestHandleModels_DisabledModelsStillWin makes sure the pre-existing
// disabledModels KV scope keeps filtering in every mode.
func TestHandleModels_DisabledModelsStillWin(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='disabledModels'`); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('disabledModels', 'oc', ?)`,
		`["space-bunny-free"]`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	for _, query := range []string{"", "?connected=1", "?all=1"} {
		ids := fetchModels(t, h, query).idSet()
		if ids["oc/space-bunny-free"] {
			t.Errorf("query %q listed a disabled model", query)
		}
	}
}

func TestModelsListModeFromQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  ModelsListMode
	}{
		{"absent params keep upstream default", "", modeListAll},
		{"explicit connected narrows", "?connected=1", modeListConnected},
		{"connected accepts true", "?connected=true", modeListConnected},
		{"connected accepts yes", "?connected=yes", modeListConnected},
		{"explicit all forces catalog", "?all=1", modeListCatalog},
		{"all accepts true", "?all=true", modeListCatalog},
		{"connected wins over all", "?all=1&connected=1", modeListConnected},
		{"valueless all is ignored", "?all", modeListAll},
		{"unknown value is ignored", "?connected=maybe", modeListAll},
		{"unrelated params ignored", "?limit=10", modeListAll},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/models"+tt.query, nil)
			if got := modelsListModeFromQuery(req); got != tt.want {
				t.Errorf("modelsListModeFromQuery(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestIsUsableProvider(t *testing.T) {
	usable := map[string]bool{"codex": true, "opencode": true}
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"directly connected", "codex", true},
		{"noAuth provider", "opencode", true},
		{"known noAuth by alias", "oc", true},
		{"absent provider", "kiro", false},
		{"empty id", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUsableProvider(tt.id, usable); got != tt.want {
				t.Errorf("isUsableProvider(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// firstLines trims a long joined id dump so failure output stays readable.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n... (truncated)"
}
