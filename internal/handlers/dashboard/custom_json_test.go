package dashboard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	json "encoding/json/v2"
)

const customJSONNodeBody = `{"type":"openai-compatible","apiType":"custom-json","name":"Custom API","prefix":"custom",` +
	`"baseUrl":"https://api.example.com/generate",` +
	`"customJson":{"requestTemplate":{"project_id":"{{prompt}}"},"responsePath":"data.answer","authType":"none","script":"x"}}`

func sendJSON(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:3456"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func createCustomJSONNode(t *testing.T, router http.Handler, body string) ProviderNodeResponse {
	t.Helper()
	rec := sendJSON(t, router, http.MethodPost, "/api/provider-nodes", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Node ProviderNodeResponse `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	return out.Node
}

func connectionPSD(t *testing.T, raw string) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("decode connection data: %v", err)
	}
	psd, _ := data["providerSpecificData"].(map[string]any)
	return psd
}

// stubProbe counts outbound probe calls so a test can assert that saving
// never reaches a business endpoint.
func stubProbe(t *testing.T, status int, reply string) *int {
	t.Helper()
	calls := 0
	orig := validateProbeDo
	t.Cleanup(func() { validateProbeDo = orig })
	validateProbeDo = func(context.Context, string, string, map[string]string, []byte) (int, []byte, error) {
		calls++
		return status, []byte(reply), nil
	}
	return &calls
}

func TestCustomJSONNode_PersistsTemplateAndCopiesItToConnections(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	node := createCustomJSONNode(t, router, customJSONNodeBody)
	if node.ID[:len("openai-compatible-custom-json-")] != "openai-compatible-custom-json-" {
		t.Errorf("node id = %q", node.ID)
	}
	var stored map[string]any
	if err := json.Unmarshal(node.CustomJSON, &stored); err != nil {
		t.Fatalf("decode customJson: %v", err)
	}
	if stored["responsePath"] != "data.answer" || stored["authType"] != "none" || stored["script"] != nil {
		t.Errorf("stored customJson = %v", stored)
	}

	// A client-supplied configuration is replaced by the node's own.
	rec := sendJSON(t, router, http.MethodPost, "/api/connections",
		`{"provider":"`+node.ID+`","name":"local","providerSpecificData":{"customJson":{"authType":"bearer","responsePath":"$"}}}`)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create connection: %d %s", rec.Code, rec.Body.String())
	}
	conns, err := repo.GetProviderConnections(node.ID, false)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections = %v, %v", conns, err)
	}
	psd := connectionPSD(t, conns[0].Data)
	cj, _ := psd["customJson"].(map[string]any)
	if psd["apiType"] != "custom-json" || cj["responsePath"] != "data.answer" || cj["authType"] != "none" {
		t.Errorf("connection providerSpecificData = %v", psd)
	}

	rec = sendJSON(t, router, http.MethodGet, "/api/provider-nodes", "")
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"customJson"`)) {
		t.Errorf("node list omits customJson: %s", rec.Body.String())
	}
}

func TestCustomJSONNode_RejectsInvalidTemplateWithoutMutating(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	bad := `{"type":"openai-compatible","apiType":"custom-json","name":"C","prefix":"c","baseUrl":"https://a.example.com/x","customJson":{"requestTemplate":[]}}`
	if rec := sendJSON(t, router, http.MethodPost, "/api/provider-nodes", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with array template: %d %s", rec.Code, rec.Body.String())
	}
	node := createCustomJSONNode(t, router, customJSONNodeBody)
	update := `{"name":"Custom API","prefix":"custom","apiType":"custom-json","baseUrl":"https://api.example.com/generate","customJson":{"responsePath":"__proto__","requestTemplate":{}}}`
	if rec := putNode(t, router, node.ID, update); rec.Code != http.StatusBadRequest {
		t.Fatalf("update with unsafe path: %d %s", rec.Code, rec.Body.String())
	}
	_, data, err := repo.GetProviderNodeByID(node.ID)
	if err != nil || data == nil {
		t.Fatalf("reload node: %v", err)
	}
	var stored map[string]any
	_ = json.Unmarshal(data.CustomJSON, &stored)
	if stored["responsePath"] != "data.answer" {
		t.Errorf("stored config changed after rejected update: %v", stored)
	}
}

func TestCustomJSONNode_UpdateSyncsConnectionsAndSwitchBackDropsTemplate(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	node := createCustomJSONNode(t, router, customJSONNodeBody)
	for _, name := range []string{"one", "two"} {
		if rec := sendJSON(t, router, http.MethodPost, "/api/connections", `{"provider":"`+node.ID+`","name":"`+name+`"}`); rec.Code >= 300 {
			t.Fatalf("create connection %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	changed := `{"name":"Custom API","prefix":"custom","apiType":"custom-json","baseUrl":"https://api.example.com/v2/run",` +
		`"customJson":{"requestTemplate":{"question":"{{prompt}}"},"responsePath":"result","authType":"none"}}`
	if rec := putNode(t, router, node.ID, changed); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	conns, _ := repo.GetProviderConnections(node.ID, false)
	for _, c := range conns {
		psd := connectionPSD(t, c.Data)
		cj, _ := psd["customJson"].(map[string]any)
		if psd["baseUrl"] != "https://api.example.com/v2/run" || cj["responsePath"] != "result" {
			t.Errorf("connection %s not synced: %v", c.ID, psd)
		}
	}

	back := `{"name":"Custom API","prefix":"custom","apiType":"chat","baseUrl":"https://api.example.com/v1"}`
	if rec := putNode(t, router, node.ID, back); rec.Code != http.StatusOK {
		t.Fatalf("switch back: %d %s", rec.Code, rec.Body.String())
	}
	conns, _ = repo.GetProviderConnections(node.ID, false)
	for _, c := range conns {
		psd := connectionPSD(t, c.Data)
		if psd["apiType"] != "chat" || psd["customJson"] != nil {
			t.Errorf("connection %s kept the business template: %v", c.ID, psd)
		}
	}
}

func TestCustomJSONConnection_ValidateOnlyNeverCallsEndpoint(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	calls := stubProbe(t, http.StatusOK, `{"data":{"answer":"OK"}}`)

	node := createCustomJSONNode(t, router, customJSONNodeBody)
	rec := sendJSON(t, router, http.MethodPost, "/api/providers/validate", `{"provider":"`+node.ID+`","validateOnly":true}`)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"valid":true`)) {
		t.Fatalf("validateOnly: %s", rec.Body.String())
	}
	if *calls != 0 {
		t.Errorf("validateOnly called the endpoint %d times", *calls)
	}

	rec = sendJSON(t, router, http.MethodPost, "/api/providers/validate", `{"provider":"`+node.ID+`"}`)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"valid":true`)) || *calls != 1 {
		t.Errorf("explicit test: %s (calls=%d)", rec.Body.String(), *calls)
	}
}

func TestValidateProviderNode_CustomJSONProbe(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		reply     string
		wantValid bool
	}{
		{"mapped reply", http.StatusOK, `{"data":{"answer":"pong"}}`, true},
		{"unmappable reply", http.StatusOK, `{"other":1}`, false},
		{"business error", http.StatusBadGateway, `{"error":"down"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubProbe(t, tt.status, tt.reply)
			// No API key: the node uses no authentication, so the test may run without one.
			_, out := postValidateNode(t, `{"apiType":"custom-json","baseUrl":"https://api.example.com/generate","apiKey":"",`+
				`"customJson":{"authType":"none","responsePath":"data.answer"}}`, true)
			if nodeValid(t, out) != tt.wantValid || *calls != 1 {
				t.Errorf("out = %v, calls = %d", out, *calls)
			}
		})
	}
}
