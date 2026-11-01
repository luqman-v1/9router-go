package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Issue #154: the edit-connection modal rotates a connection's credential.
// A blank apiKey means "keep the stored one", a non-blank one replaces it, and
// a listing only ever shows a non-reversible mask.
func TestHandleUpdateConnection_APIKeyReplacement(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	const connID = "conn-key-rotate"
	const storedKey = "sk-live-original"
	const stored = `{"apiKey":"` + storedKey + `","authToken":"bearer-token","testStatus":"error",` +
		`"lastError":{"message":"401 invalid key"},"lastErrorAt":"2026-01-01T00:00:00Z",` +
		`"providerSpecificData":{"baseUrl":"https://api.example.com/v1"}}`
	if err := repo.CreateProviderConnectionFull(connID, "openai-compatible-chat-abc", "apikey", "Node", nil, stored); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	tests := []struct {
		name    string
		body    string
		wantKey string
	}{
		{
			name:    "omitted key keeps the stored credential",
			body:    `{"name":"Renamed"}`,
			wantKey: storedKey,
		},
		{
			name:    "blank key keeps the stored credential",
			body:    `{"apiKey":"   "}`,
			wantKey: storedKey,
		},
		{
			name:    "a new key replaces the credential",
			body:    `{"apiKey":"sk-live-replacement"}`,
			wantKey: "sk-live-replacement",
		},
		{
			name:    "a rotation can be combined with a rename and a priority change",
			body:    `{"name":"Rotated","priority":3,"apiKey":"sk-live-second"}`,
			wantKey: "sk-live-second",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := put(tt.body); rec.Code != http.StatusOK {
				t.Fatalf("PUT %s = %d: %s", tt.body, rec.Code, rec.Body.String())
			}
			data := readConnectionData(t, repo, connID)
			if data["apiKey"] != tt.wantKey {
				t.Errorf("apiKey = %v, want %v", data["apiKey"], tt.wantKey)
			}
			if data["authToken"] != "bearer-token" {
				t.Errorf("authToken must survive a rotation, got %v", data["authToken"])
			}
			psd, _ := data["providerSpecificData"].(map[string]any)
			if psd == nil || psd["baseUrl"] != "https://api.example.com/v1" {
				t.Errorf("providerSpecificData must survive a rotation, got %v", data["providerSpecificData"])
			}
			// Rotating a key is not evidence that the connection works again:
			// the row stays as the last probe left it, and only an explicit
			// testStatus write (which the modal sends after validating) clears it.
			if data["testStatus"] != "error" {
				t.Errorf("testStatus = %v, want the row's own probe state", data["testStatus"])
			}
			if _, ok := data["lastError"]; !ok {
				t.Error("lastError must survive a rotation that carries no testStatus")
			}
			if _, ok := data["lastErrorAt"]; !ok {
				t.Error("lastErrorAt must survive a rotation that carries no testStatus")
			}
		})
	}

	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil || conn == nil {
		t.Fatalf("read connection: %v", err)
	}
	if conn.Name == nil || *conn.Name != "Rotated" {
		t.Errorf("name = %v, want Rotated", conn.Name)
	}
	if conn.Priority == nil || *conn.Priority != 3 {
		t.Errorf("priority = %v, want 3", conn.Priority)
	}
}

// The listing feeds the modal's placeholder, so it must carry a hint for every
// credential kind the dashboard can rotate, and never the raw value.
func TestSanitizeProviderConnection_ExposesOnlyMaskedCredentials(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	const (
		apiKeyConn = "conn-mask-apikey"
		oauthConn  = "conn-mask-oauth"
		keyless    = "conn-mask-keyless"
	)
	if err := repo.CreateProviderConnectionFull(apiKeyConn, "openai", "apikey", "Key", nil,
		`{"apiKey":"sk-abcdefghijklmnop"}`); err != nil {
		t.Fatalf("create api-key connection: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(oauthConn, "gemini-cli", "oauth", "OAuth", nil,
		`{"apiKey":"oauth-token-value-1234","accessToken":"oauth-access-token-9999"}`); err != nil {
		t.Fatalf("create oauth connection: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(keyless, "ollama", "none", "Local", nil,
		`{"providerSpecificData":{"baseUrl":"http://127.0.0.1:11434"}}`); err != nil {
		t.Fatalf("create credential-less connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/connections", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list connections = %d: %s", rec.Code, rec.Body.String())
	}

	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal connections: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, c := range listed {
		id, _ := c["id"].(string)
		byID[id] = c
	}

	tests := []struct {
		id     string
		secret string
		want   any // nil = no hint at all
	}{
		{id: apiKeyConn, secret: "sk-abcdefghijklmnop", want: "sk-abc•••••••••mnop"},
		// An OAuth connection sends its access token; apiKey only mirrors it.
		{id: oauthConn, secret: "oauth-access-token-9999", want: "oauth-•••••••••••••9999"},
		{id: keyless, want: nil},
	}
	for _, tt := range tests {
		conn, ok := byID[tt.id]
		if !ok {
			t.Fatalf("connection %s missing from listing: %v", tt.id, listed)
		}
		if conn["apiKeyMasked"] != tt.want {
			t.Errorf("%s apiKeyMasked = %v, want %v", tt.id, conn["apiKeyMasked"], tt.want)
		}
		if tt.secret == "" {
			continue
		}
		for _, leak := range []string{tt.secret, "apiKey", "accessToken"} {
			if _, present := conn[leak]; present {
				t.Errorf("%s listing exposes %q", tt.id, leak)
			}
		}
	}
}

func TestMaskConnectionSecret(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "empty", secret: "", want: "***"},
		{name: "too short to reveal any prefix", secret: "short-key", want: "***"},
		{name: "exactly the reveal threshold", secret: "123456789012", want: "***"},
		{name: "long key keeps only a hint", secret: "sk-proj-1234567890abcdef", want: "sk-pro••••••••••••••cdef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskConnectionSecret(tt.secret); got != tt.want {
				t.Errorf("maskConnectionSecret(%q) = %q, want %q", tt.secret, got, tt.want)
			}
		})
	}
}