package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postConnection(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHandleCreateConnection_StoresModalFields(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	body := `{
		"provider": "clinepass",
		"authType": "apikey",
		"name": "ClinePass Production",
		"apiKey": "sk-cline-secret",
		"priority": 7,
		"testStatus": "active",
		"proxyPoolId": "pool-42",
		"providerSpecificData": {"accountId": "acc-1"}
	}`
	rec := postConnection(t, router, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("expected created connection id")
	}

	conn, err := repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		t.Fatalf("failed to reload connection: %v", err)
	}
	if conn.Name == nil || *conn.Name != "ClinePass Production" {
		t.Errorf("unexpected name %v", conn.Name)
	}
	if conn.Priority == nil || *conn.Priority != 7 {
		t.Errorf("expected priority 7, got %v", conn.Priority)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("decode connection data: %v", err)
	}
	if data["apiKey"] != "sk-cline-secret" {
		t.Errorf("expected apiKey stored, got %v", data["apiKey"])
	}
	if data["testStatus"] != "active" {
		t.Errorf("expected testStatus stored, got %v", data["testStatus"])
	}
	if data["proxyPoolId"] != "pool-42" {
		t.Errorf("expected proxyPoolId stored, got %v", data["proxyPoolId"])
	}
	psd, ok := data["providerSpecificData"].(map[string]any)
	if !ok || psd["accountId"] != "acc-1" {
		t.Errorf("expected providerSpecificData stored, got %v", data["providerSpecificData"])
	}
}

func TestHandleCreateConnection_DefaultsPriorityToNextFreeSlot(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	first := postConnection(t, router, `{"provider":"deepseek","name":"First","apiKey":"k1"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", first.Code, first.Body.String())
	}
	var firstBody map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &firstBody)
	firstConn, _ := repo.GetProviderConnectionByID(firstBody["id"].(string))
	if firstConn.Priority == nil || *firstConn.Priority != 1 {
		t.Fatalf("expected first connection priority 1, got %v", firstConn.Priority)
	}

	second := postConnection(t, router, `{"provider":"deepseek","name":"Second","apiKey":"k2"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", second.Code, second.Body.String())
	}
	var secondBody map[string]any
	_ = json.Unmarshal(second.Body.Bytes(), &secondBody)
	secondConn, _ := repo.GetProviderConnectionByID(secondBody["id"].(string))
	if secondConn.Priority == nil || *secondConn.Priority != 2 {
		t.Errorf("expected fallback priority max+1 = 2, got %v", secondConn.Priority)
	}
}

func TestHandleCreateConnection_KeepsLegacyDataString(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	body := `{"provider":"cline","authType":"oauth","name":"OAuth Conn","data":"{\"refreshToken\":\"rt-1\",\"accessToken\":\"at-1\"}"}`
	rec := postConnection(t, router, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	conn, _ := repo.GetProviderConnectionByID(created["id"].(string))

	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		t.Fatalf("decode connection data: %v", err)
	}
	if data["refreshToken"] != "rt-1" || data["accessToken"] != "at-1" {
		t.Errorf("legacy data payload not preserved: %v", data)
	}
}

func TestHandleCreateConnection_RequiresProvider(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := postConnection(t, router, `{"name":"No provider","apiKey":"k"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleCreateConnection_HydratesCompatibleNodeData(t *testing.T) {
	tests := []struct {
		name        string
		nodeID      string
		nodeType    string
		nodeData    string
		wantPrefix  string
		wantBaseURL string
		wantAPIType string
	}{
		{
			name:        "openai compatible",
			nodeID:      "openai-compatible-chat-wus",
			nodeType:    "openai-compatible",
			nodeData:    `{"prefix":"wus","apiType":"responses","baseUrl":"https://wus.example.com/v1"}`,
			wantPrefix:  "wus",
			wantBaseURL: "https://wus.example.com/v1",
			wantAPIType: "responses",
		},
		{
			name:        "anthropic compatible",
			nodeID:      "anthropic-compatible-acme",
			nodeType:    "anthropic-compatible",
			nodeData:    `{"prefix":"acme","baseUrl":"https://acme.example.com/v1"}`,
			wantPrefix:  "acme",
			wantBaseURL: "https://acme.example.com/v1",
		},
		{
			name:        "custom embedding",
			nodeID:      "custom-embedding-voyage",
			nodeType:    "custom-embedding",
			nodeData:    `{"prefix":"voyage","baseUrl":"https://voyage.example.com/v1"}`,
			wantPrefix:  "voyage",
			wantBaseURL: "https://voyage.example.com/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			if _, err := repo.CreateProviderNode(tt.nodeID, tt.nodeType, "Node Name", tt.nodeData); err != nil {
				t.Fatalf("seed node: %v", err)
			}

			body := fmt.Sprintf(`{
				"provider": %q,
				"authType": "apikey",
				"name": "Node key",
				"apiKey": "sk-node-secret",
				"providerSpecificData": {
					"prefix": "stale",
					"apiType": "stale",
					"baseUrl": "https://stale.example.com/v1",
					"nodeName": "Stale",
					"assignedModel": "pinned-model"
				}
			}`, tt.nodeID)
			rec := postConnection(t, router, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
			}

			var created map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatalf("decode create response: %v", err)
			}
			id, _ := created["id"].(string)
			conn, err := repo.GetProviderConnectionByID(id)
			if err != nil || conn == nil {
				t.Fatalf("reload connection: %v", err)
			}

			var data map[string]any
			if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
				t.Fatalf("decode connection data: %v", err)
			}
			psd, ok := data["providerSpecificData"].(map[string]any)
			if !ok {
				t.Fatalf("providerSpecificData missing: %v", data)
			}
			if psd["prefix"] != tt.wantPrefix {
				t.Errorf("expected node prefix %q, got %v", tt.wantPrefix, psd["prefix"])
			}
			if psd["baseUrl"] != tt.wantBaseURL {
				t.Errorf("expected node base URL %q, got %v", tt.wantBaseURL, psd["baseUrl"])
			}
			if psd["nodeName"] != "Node Name" {
				t.Errorf("expected node name, got %v", psd["nodeName"])
			}
			if psd["assignedModel"] != "pinned-model" {
				t.Errorf("expected unrelated providerSpecificData preserved, got %v", psd["assignedModel"])
			}
			if tt.wantAPIType == "" {
				if _, exists := psd["apiType"]; exists {
					t.Errorf("apiType must be absent for %s, got %v", tt.nodeType, psd["apiType"])
				}
			} else if psd["apiType"] != tt.wantAPIType {
				t.Errorf("expected apiType %q, got %v", tt.wantAPIType, psd["apiType"])
			}
			if data["apiKey"] != "sk-node-secret" {
				t.Errorf("expected apiKey stored, got %v", data["apiKey"])
			}
		})
	}
}

func TestHandleCreateConnection_CompatibleNodeMissing(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := postConnection(t, router, `{
		"provider": "openai-compatible-chat-missing",
		"authType": "apikey",
		"name": "Missing node",
		"apiKey": "sk-missing"
	}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
	conns, err := repo.GetProviderConnections("", false)
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("missing node must not create a connection, got %d", len(conns))
	}
}
