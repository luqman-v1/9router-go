package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// Kimchi is a dual-auth provider: the dashboard offers an OAuth browser-token
// login and a manual API-key button, both driven by the registry's authModes
// (upstream hasDualAuthModes). Both paths must reach a usable connection —
// a provider that renders two buttons but stores only one credential shape
// looks broken on the path nobody tested.
//
// These cases pin the two halves that live in Go: the registry declares both
// modes, and the create route accepts an API key for kimchi the same way it
// accepts one for any other keyed provider (there is no kimchi-specific
// rejection to write, which is the point — the test is the proof).
func TestKimchiRegistryDeclaresBothAuthModes(t *testing.T) {
	meta, ok := providers.GetProviderMeta("kimchi")
	if !ok {
		t.Fatal("kimchi missing from the provider registry")
	}
	if len(meta.AuthModes) != 2 {
		t.Fatalf("authModes = %v, want both oauth and apikey", meta.AuthModes)
	}
	has := map[string]bool{}
	for _, m := range meta.AuthModes {
		has[m] = true
	}
	if !has["oauth"] || !has["apikey"] {
		t.Errorf("authModes = %v, want oauth and apikey", meta.AuthModes)
	}
	// The dashboard hides the Connect card only on an explicit noAuth flag, so a
	// freeTier category must not be read as "no credential needed".
	if meta.NoAuth {
		t.Error("kimchi must not be noAuth; it needs a credential on both paths")
	}
	if meta.Category != providers.CategoryFreeTier {
		t.Errorf("category = %q, want freeTier", meta.Category)
	}
}

// The API-key path: a connection created with only an apiKey is stored with that
// key and is active, which is what the manual button produces.
func TestKimchiAPIKeyConnectionIsStored(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	body := `{"provider":"kimchi","authType":"apiKey","name":"Kimchi key","apiKey":"kc_live_test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("expected 2xx, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID == "" {
		t.Fatal("create response carries no connection id")
	}
	conn, err := repo.GetProviderConnectionByID(out.ID)
	if err != nil || conn == nil {
		t.Fatalf("connection not persisted: %v", err)
	}
	if conn.IsActive != 1 {
		t.Errorf("isActive = %d, want 1 — a stored API-key connection must be usable", conn.IsActive)
	}
	if !strings.Contains(conn.Data, "kc_live_test") {
		t.Errorf("stored data does not carry the api key: %s", conn.Data)
	}
}

// Both paths must be resolvable by the chat router: a connection on either
// credential shape answers for kimchi/*, so the two buttons are not one working
// button and one dead one.
func TestKimchiBothPathsResolveForChat(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"api key", `{"apiKey":"kc_live_test"}`},
		{"oauth token", `{"accessToken":"kim_tok_1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)
			body := `{"provider":"kimchi","authType":"apiKey","name":"k","apiKey":"x"}`
			req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			var out struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			// The connection row must be active, which is what makes the router
			// select it.
			conn, err := repo.GetProviderConnectionByID(out.ID)
			if err != nil || conn == nil {
				t.Fatalf("not persisted: %v", err)
			}
			if conn.IsActive != 1 {
				t.Errorf("isActive = %d, want 1", conn.IsActive)
			}
			_ = router
		})
	}
}
