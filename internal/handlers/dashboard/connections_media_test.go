package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleUpdateConnection_MediaPoolSeparation(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	connID := "conn-media-toggle-test"
	if err := repo.CreateProviderConnection(connID, "codex", "oauth", "Codex Pro", "token-pro"); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	initialData := `{
		"accessToken": "token-pro",
		"providerSpecificData": {
			"chatgptPlanType": "pro"
		}
	}`
	if err := repo.UpdateConnectionData(connID, initialData); err != nil {
		t.Fatalf("failed to initialize connection data: %v", err)
	}

	t.Run("updating mediaEnabled false does not disable connection for chat", func(t *testing.T) {
		body := `{"mediaEnabled": false}`
		req := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		conn, err := repo.GetProviderConnectionByID(connID)
		if err != nil {
			t.Fatalf("failed to fetch connection: %v", err)
		}
		if conn.IsActive != 1 {
			t.Errorf("expected connection to remain isActive=1 for chat, got %d", conn.IsActive)
		}
		if conn.IsMediaActive("image") {
			t.Errorf("expected connection to be inactive for media requests")
		}

		// Check GET /api/connections response
		getReq := httptest.NewRequest(http.MethodGet, "/api/connections", nil)
		getW := httptest.NewRecorder()
		router.ServeHTTP(getW, getReq)
		if getW.Code != http.StatusOK {
			t.Fatalf("GET /api/connections returned %d", getW.Code)
		}
		var list []map[string]any
		if err := json.Unmarshal(getW.Body.Bytes(), &list); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		var target map[string]any
		for _, item := range list {
			if item["id"] == connID {
				target = item
				break
			}
		}
		if target == nil {
			t.Fatalf("target connection %s not found in GET /api/connections", connID)
		}
		if target["isActive"] != float64(1) && target["isActive"] != 1 {
			t.Errorf("expected isActive=1 in GET /api/connections, got %v", target["isActive"])
		}
		if target["mediaEnabled"] != false {
			t.Errorf("expected mediaEnabled=false in GET /api/connections, got %v", target["mediaEnabled"])
		}
	})

	t.Run("updating disabledKinds preserves chat active status", func(t *testing.T) {
		connKindID := "conn-disabled-kinds-test"
		if err := repo.CreateProviderConnection(connKindID, "codex", "oauth", "Codex Kinds", "token-kinds"); err != nil {
			t.Fatalf("failed to create connection: %v", err)
		}
		kindData := `{"accessToken":"token-kinds","mediaEnabled":true}`
		if err := repo.UpdateConnectionData(connKindID, kindData); err != nil {
			t.Fatalf("failed to init connection data: %v", err)
		}

		body := `{"providerSpecificData": {"disabledKinds": ["image", "video"]}}`
		req := httptest.NewRequest(http.MethodPut, "/api/providers/"+connKindID, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		conn, err := repo.GetProviderConnectionByID(connKindID)
		if err != nil {
			t.Fatalf("failed to fetch connection: %v", err)
		}
		if conn.IsActive != 1 {
			t.Errorf("expected connection to remain active for chat, got %d", conn.IsActive)
		}
		if conn.IsMediaActive("image") {
			t.Errorf("expected image to be disabled")
		}
		if conn.IsMediaActive("video") {
			t.Errorf("expected video to be disabled")
		}
		if !conn.IsMediaActive("tts") {
			t.Errorf("expected tts to remain enabled")
		}
	})

	t.Run("updating isActive false disables connection for both chat and media", func(t *testing.T) {
		body := `{"isActive": false}`
		req := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		conn, err := repo.GetProviderConnectionByID(connID)
		if err != nil {
			t.Fatalf("failed to fetch connection: %v", err)
		}
		if conn.IsActive != 0 {
			t.Errorf("expected isActive=0, got %d", conn.IsActive)
		}
		if conn.IsMediaActive() {
			t.Errorf("expected media to be inactive when isActive=0")
		}
	})
}
