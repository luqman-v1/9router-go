package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

// The provider id of a custom node is the address every model, connection and
// usage row points at. These cases pin what a hand-written urlSuffix produces,
// what it refuses, and that renaming a node carries the node's whole footprint
// to the new id instead of orphaning it.

func postNode(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/provider-nodes", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeNode(t *testing.T, rec *httptest.ResponseRecorder) ProviderNodeResponse {
	t.Helper()
	var out struct {
		Node ProviderNodeResponse `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode node response %s: %v", rec.Body.String(), err)
	}
	return out.Node
}

// A custom suffix replaces the uuid in the composed id, for every node shape,
// and the response reports back the suffix so the dashboard can render the field.
func TestHandleCreateProviderNode_CustomSuffixReplacesUUID(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantID     string
		wantSuffix string
	}{
		{
			name:       "openai chat",
			body:       `{"name":"B.AI","prefix":"acme","urlSuffix":"acme","apiType":"chat","baseUrl":"https://api.b-ai.example/v1"}`,
			wantID:     "openai-compatible-chat-acme",
			wantSuffix: "acme",
		},
		{
			name:       "openai responses pins the api type in the id",
			body:       `{"name":"B.AI","prefix":"acme","urlSuffix":"acme","apiType":"responses","baseUrl":"https://api.b-ai.example/v1"}`,
			wantID:     "openai-compatible-responses-acme",
			wantSuffix: "acme",
		},
		{
			name:       "anthropic has no api type segment",
			body:       `{"name":"AC","prefix":"acme","urlSuffix":"ac-prod","type":"anthropic-compatible","baseUrl":"https://ac.example/v1"}`,
			wantID:     "anthropic-compatible-ac-prod",
			wantSuffix: "ac-prod",
		},
		{
			name:       "custom embedding",
			body:       `{"name":"Voyage","prefix":"voy","urlSuffix":"voy","type":"custom-embedding","baseUrl":"https://voyage.example/v1"}`,
			wantID:     "custom-embedding-voy",
			wantSuffix: "voy",
		},
		{
			name:       "dotted and underscored suffixes stay writable",
			body:       `{"name":"N","prefix":"n","urlSuffix":"prod_v2.1"}`,
			wantID:     "openai-compatible-chat-prod_v2.1",
			wantSuffix: "prod_v2.1",
		},
		{
			name:       "surrounding whitespace is trimmed",
			body:       `{"name":"N","prefix":"n","urlSuffix":"  acme  "}`,
			wantID:     "openai-compatible-chat-acme",
			wantSuffix: "acme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			rec := postNode(t, router, tt.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
			}
			node := decodeNode(t, rec)
			if node.ID != tt.wantID {
				t.Errorf("id = %q, want %q", node.ID, tt.wantID)
			}
			if node.URLSuffix != tt.wantSuffix {
				t.Errorf("urlSuffix = %q, want %q", node.URLSuffix, tt.wantSuffix)
			}
			if node.URLSuffixGenerated {
				t.Error("urlSuffixGenerated must be false for a suffix the caller chose")
			}

			stored, _, err := repo.GetProviderNodeByID(tt.wantID)
			if err != nil || stored == nil {
				t.Fatalf("node %s not persisted: %v", tt.wantID, err)
			}
		})
	}
}

// Omitting the suffix keeps upstream's random id, and the response marks the
// tail as generated so the dashboard does not present a uuid as editable text.
func TestHandleCreateProviderNode_EmptySuffixStaysGenerated(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := postNode(t, router, `{"name":"N","prefix":"n","baseUrl":"https://a.example/v1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	node := decodeNode(t, rec)
	if !strings.HasPrefix(node.ID, "openai-compatible-chat-") {
		t.Fatalf("id = %q, want the openai-compatible-chat- literal", node.ID)
	}
	if !node.URLSuffixGenerated {
		t.Error("urlSuffixGenerated must be true when the id tail was generated")
	}
	if node.URLSuffix != "" {
		t.Errorf("urlSuffix = %q, want empty for a generated id", node.URLSuffix)
	}
	if !isGeneratedNodeSuffix(strings.TrimPrefix(node.ID, "openai-compatible-chat-")) {
		t.Errorf("id tail %q is not a uuid", node.ID)
	}
}

// The list endpoint derives the same suffix the create endpoint composed, so the
// dashboard shows a node's value unchanged on a later page load.
func TestHandleGetProviderNodes_ReportsURLSuffix(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	if _, err := repo.CreateProviderNode("openai-compatible-chat-bai", "openai-compatible", "B.AI",
		`{"prefix":"acme","apiType":"chat","baseUrl":"https://api.b-ai.example/v1"}`); err != nil {
		t.Fatalf("seed named node: %v", err)
	}
	generated := "openai-compatible-chat-2eb28394-dbf3-4c93-8477-2690aeea7041"
	if _, err := repo.CreateProviderNode(generated, "openai-compatible", "Generated",
		`{"prefix":"gen","apiType":"chat","baseUrl":"https://gen.example/v1"}`); err != nil {
		t.Fatalf("seed generated node: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/provider-nodes", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Nodes []ProviderNodeResponse `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := make(map[string]ProviderNodeResponse, len(out.Nodes))
	for _, n := range out.Nodes {
		byID[n.ID] = n
	}
	if got := byID["openai-compatible-chat-bai"]; got.URLSuffix != "bai" || got.URLSuffixGenerated {
		t.Errorf("named node suffix = %q (generated=%v), want \"bai\" (generated=false)", got.URLSuffix, got.URLSuffixGenerated)
	}
	if got := byID[generated]; got.URLSuffixGenerated != true {
		t.Errorf("uuid node urlSuffixGenerated = %v, want true", got.URLSuffixGenerated)
	}
}

// A suffix that produces an id a reader would mis-parse is refused: "/" splits
// the "provider/model" address resolution builds, and a suffix naming a
// provider literal would be matched by every strings.HasPrefix reader.
func TestHandleCreateProviderNode_RejectsUnusableSuffix(t *testing.T) {
	tests := []struct {
		name   string
		suffix string
		want   string
	}{
		{name: "slash breaks the provider/model address", suffix: "team/nara", want: "may only contain"},
		{name: "space", suffix: "my node", want: "may only contain"},
		{name: "leading dot", suffix: ".hidden", want: "may only contain"},
		{name: "leading hyphen", suffix: "-node", want: "may only contain"},
		{name: "provider literal", suffix: "openai-compatible", want: "must not start with"},
		{name: "anthropic literal", suffix: "anthropic-compatible-x", want: "must not start with"},
		{name: "embedding literal", suffix: "custom-embedding", want: "must not start with"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			body, _ := json.Marshal(map[string]any{
				"name": "N", "prefix": "n", "baseUrl": "https://a.example/v1", "urlSuffix": tt.suffix,
			})
			rec := postNode(t, router, string(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("suffix %q: expected 400, got %d: %s", tt.suffix, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("suffix %q: message %s should mention %q", tt.suffix, rec.Body.String(), tt.want)
			}
			nodes, err := repo.GetProviderNodes()
			if err != nil {
				t.Fatalf("list nodes: %v", err)
			}
			if len(nodes) != 0 {
				t.Errorf("a refused suffix must persist nothing, got %d nodes", len(nodes))
			}
		})
	}
}

// Two endpoints cannot share a provider id: the second create is a conflict the
// user resolves by choosing another suffix, not a 500 from the primary key.
func TestHandleCreateProviderNode_SuffixCollisionIsConflict(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	body := `{"name":"First","prefix":"acme","urlSuffix":"acme","baseUrl":"https://one.example/v1"}`
	if rec := postNode(t, router, body); rec.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	rec := postNode(t, router, `{"name":"Second","prefix":"acme2","urlSuffix":"acme","baseUrl":"https://two.example/v1"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second create: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PROVIDER_NODE_ID_CONFLICT") {
		t.Errorf("conflict body %s should carry the typed code", rec.Body.String())
	}
}

// The rename is the risky part of the feature: a node id is the storage key for
// its connections, models, aliases, disabled list, combos, settings and history.
// Moving only the row would leave all of those pointing at a provider that no
// longer exists.
func TestHandleUpdateProviderNode_RenameCarriesEverything(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	const oldID = "openai-compatible-chat-bai-old"
	const newID = "openai-compatible-chat-acme"
	if _, err := repo.CreateProviderNode(oldID, "openai-compatible", "B.AI",
		`{"prefix":"acme","apiType":"chat","baseUrl":"https://api.b-ai.example/v1"}`); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := repo.RawDB().Exec(query, args...); err != nil {
			t.Fatalf("seed %s: %v", query, err)
		}
	}
	exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-1', ?, 'apikey', 'Key', 1, 1, '{"apiKey":"sk-x"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, oldID)
	exec(`INSERT INTO kv (scope, key, value) VALUES
		('customModels', ?, '{"providerAlias":"' || ? || '","id":"glm-5.3","type":"llm"}'),
		('disabledModels', ?, '["glm-4.7"]'),
		('modelAliases', 'fast-bai', '"' || ? || '/glm-5.3"'),
		('pricing', 'deepseek', '{"input":1}')`,
		oldID+"|glm-5.3|llm", oldID, oldID, oldID)
	exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt)
		VALUES ('combo-bai', 'combo-bai', 'fallback', '["' || ? || '/glm-5.3"]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, oldID)
	exec(`INSERT INTO usageHistory (timestamp, provider, model, connectionId, status)
		VALUES ('2026-01-01T00:00:00Z', ?, 'glm-5.3', 'conn-1', 'success')`, oldID)
	exec(`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		VALUES ('rd-1', '2026-01-01T00:00:00Z', ?, 'glm-5.3', 'conn-1', 'success', '{}')`, oldID)
	exec(`INSERT INTO settings (id, data) VALUES (1, ?)`, `{"providerStrategies":{"`+oldID+`":{"proxyPoolId":"pool-1","proxyRotateStrategy":"none"},"deepseek":{"proxyPoolId":"pool-2"}},"providerOverrides":{"`+oldID+`":{"headers":{"x-team":"bai"}}}}`)

	rec := putNode(t, router, oldID,
		`{"name":"B.AI","prefix":"acme","apiType":"chat","baseUrl":"https://api.b-ai.example/v1","urlSuffix":"acme"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeNode(t, rec); got.ID != newID || got.URLSuffix != "acme" {
		t.Fatalf("renamed node = %+v, want id %q", got, newID)
	}

	if node, _, _ := repo.GetProviderNodeByID(oldID); node != nil {
		t.Error("the old provider id must no longer resolve")
	}

	// Every row that named the old id must name the new one.
	var provider string
	if err := repo.RawDB().QueryRow(`SELECT provider FROM providerConnections WHERE id = 'conn-1'`).Scan(&provider); err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if provider != newID {
		t.Errorf("connection provider = %q, want %q", provider, newID)
	}
	assertKVValue(t, repo, "customModels", newID+"|glm-5.3|llm", `"providerAlias":"`+newID+`"`)
	assertKVMissing(t, repo, "customModels", oldID+"|glm-5.3|llm")
	assertKVValue(t, repo, "disabledModels", newID, `["glm-4.7"]`)
	assertKVMissing(t, repo, "disabledModels", oldID)
	assertKVValue(t, repo, "modelAliases", "fast-bai", `"`+newID+`/glm-5.3"`)

	var models string
	if err := repo.RawDB().QueryRow(`SELECT models FROM combos WHERE id = 'combo-bai'`).Scan(&models); err != nil {
		t.Fatalf("read combo: %v", err)
	}
	if !strings.Contains(models, newID+"/glm-5.3") || strings.Contains(models, oldID) {
		t.Errorf("combo members = %s, want the renamed model id", models)
	}

	var usageProvider, detailProvider string
	if err := repo.RawDB().QueryRow(`SELECT provider FROM usageHistory WHERE connectionId = 'conn-1'`).Scan(&usageProvider); err != nil {
		t.Fatalf("read usageHistory: %v", err)
	}
	if err := repo.RawDB().QueryRow(`SELECT provider FROM requestDetails WHERE id = 'rd-1'`).Scan(&detailProvider); err != nil {
		t.Fatalf("read requestDetails: %v", err)
	}
	if usageProvider != newID || detailProvider != newID {
		t.Errorf("history providers = %q / %q, want %q", usageProvider, detailProvider, newID)
	}

	settings, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	strategies, _ := settings["providerStrategies"].(map[string]any)
	if _, gone := strategies[oldID]; gone {
		t.Errorf("providerStrategies still keys the retired id: %v", strategies)
	}
	entry, ok := strategies[newID].(map[string]any)
	if !ok || entry["proxyPoolId"] != "pool-1" {
		t.Errorf("providerStrategies did not move the proxy binding: %v", strategies)
	}
	if _, kept := strategies["deepseek"]; !kept {
		t.Errorf("an unrelated provider binding was dropped: %v", strategies)
	}
	overrides, _ := settings["providerOverrides"].(map[string]any)
	if _, ok := overrides[newID]; !ok {
		t.Errorf("providerOverrides did not move with the node: %v", overrides)
	}
}

// An edit that leaves the suffix alone must not touch the id — including on a
// node whose tail is a uuid, which the edit form reports as an empty field.
func TestHandleUpdateProviderNode_OmittedSuffixKeepsID(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	const id = "openai-compatible-chat-2eb28394-dbf3-4c93-8477-2690aeea7041"
	if _, err := repo.CreateProviderNode(id, "openai-compatible", "Generated",
		`{"prefix":"gen","apiType":"chat","baseUrl":"https://gen.example/v1"}`); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	rec := putNode(t, router, id,
		`{"name":"Renamed","prefix":"gen","apiType":"chat","baseUrl":"https://gen2.example/v1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	node := decodeNode(t, rec)
	if node.ID != id {
		t.Errorf("id = %q, want the unchanged %q", node.ID, id)
	}
	if !node.URLSuffixGenerated {
		t.Error("a uuid tail must still be reported as generated after an unrelated edit")
	}
}

// Renaming onto an id another node already owns must not touch either node.
func TestHandleUpdateProviderNode_RenameCollisionIsConflict(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	for id, prefix := range map[string]string{"openai-compatible-chat-bai": "one", "openai-compatible-chat-two": "two"} {
		if _, err := repo.CreateProviderNode(id, "openai-compatible", id,
			`{"prefix":"`+prefix+`","apiType":"chat","baseUrl":"https://`+prefix+`.example/v1"}`); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	rec := putNode(t, router, "openai-compatible-chat-two",
		`{"name":"Two","prefix":"two","apiType":"chat","baseUrl":"https://two.example/v1","urlSuffix":"bai"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	for id := range map[string]string{"openai-compatible-chat-bai": "one", "openai-compatible-chat-two": "two"} {
		node, _, err := repo.GetProviderNodeByID(id)
		if err != nil || node == nil {
			t.Errorf("node %s must survive a refused rename: %v", id, err)
		}
	}
}

// A node id is also the key of the custom-embedding validation branch, so the
// composed id for that shape has to stay recognisable to it.
func TestComposeProviderNodeID_KeepsReaderLiterals(t *testing.T) {
	tests := []struct {
		nodeType string
		apiType  string
		suffix   string
		want     string
	}{
		{nodeType: "openai-compatible", apiType: "chat", suffix: "x", want: "openai-compatible-chat-x"},
		{nodeType: "openai-compatible", apiType: "responses", suffix: "x", want: "openai-compatible-responses-x"},
		{nodeType: "anthropic-compatible", suffix: "x", want: "anthropic-compatible-x"},
		{nodeType: "custom-embedding", suffix: "x", want: "custom-embedding-x"},
		{nodeType: "", suffix: "x", want: "openai-compatible-chat-x"},
	}
	for _, tt := range tests {
		if got := composeProviderNodeID(tt.nodeType, tt.apiType, tt.suffix); got != tt.want {
			t.Errorf("composeProviderNodeID(%q, %q, %q) = %q, want %q", tt.nodeType, tt.apiType, tt.suffix, got, tt.want)
		}
	}
}

func assertKVValue(t *testing.T, repo *db.Repo, scope, key, wantContains string) {
	t.Helper()
	var value string
	if err := repo.RawDB().QueryRow(`SELECT value FROM kv WHERE scope = ? AND key = ?`, scope, key).Scan(&value); err != nil {
		t.Fatalf("read kv [%s %s]: %v", scope, key, err)
	}
	if !strings.Contains(value, wantContains) {
		t.Errorf("kv [%s %s] = %s, want it to contain %s", scope, key, value, wantContains)
	}
}

func assertKVMissing(t *testing.T, repo *db.Repo, scope, key string) {
	t.Helper()
	var count int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(1) FROM kv WHERE scope = ? AND key = ?`, scope, key).Scan(&count); err != nil {
		t.Fatalf("count kv [%s %s]: %v", scope, key, err)
	}
	if count != 0 {
		t.Errorf("kv [%s %s] must not survive the rename", scope, key)
	}
}
