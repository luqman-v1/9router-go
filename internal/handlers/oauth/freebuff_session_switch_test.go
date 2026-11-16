package oauth

import (
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func seedFreebuffConnection(t *testing.T, database *sql.DB, id, data string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'freebuff', 'oauth', 'Freebuff Test', 1, ?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`,
		id, data,
	)
	if err != nil {
		t.Fatalf("failed to seed freebuff connection: %v", err)
	}
}

func TestHandleFreebuffSessionSwitch_ReleasesHeldSeatAndAdmitsNewModel(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	seedFreebuffConnection(t, database, "fb-conn-1", `{"authToken":"tok-1"}`)

	var releasedInstance, admittedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/freebuff/device-keys" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"keyId":"k-test"}`))
			return
		}
		// The 0.2.36 CLI admits through /session/admission; GET and DELETE keep
		// the bare session path.
		if r.Method == http.MethodPost {
			if r.URL.Path != "/api/v1/freebuff/session/admission" {
				t.Errorf("admission must POST to the admission path, got %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
		} else if r.URL.Path != "/api/v1/freebuff/session" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"status":"active","currentModel":"model-a","instanceId":"i-old","expiresAt":"2030-01-01T00:00:00Z"}`))
		case http.MethodDelete:
			releasedInstance = r.Header.Get("x-freebuff-instance-id")
			_, _ = w.Write([]byte(`{"status":"ended","freebucksRefund":5}`))
		case http.MethodPost:
			admittedModel = r.Header.Get("x-freebuff-model")
			_, _ = w.Write([]byte(`{"status":"active","instanceId":"i-new","currentModel":"model-b","expiresAt":"2030-01-01T01:00:00Z"}`))
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	oldURL := freebuffAPIBaseURL
	freebuffAPIBaseURL = server.URL
	defer func() { freebuffAPIBaseURL = oldURL }()

	handler := NewOAuthHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/session/switch",
		strings.NewReader(`{"connectionId":"fb-conn-1","model":"model-b"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleFreebuffSessionSwitch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if res["switched"] != true {
		t.Errorf("expected switched=true, got %v", res["switched"])
	}
	if res["currentModel"] != "model-b" {
		t.Errorf("expected currentModel model-b, got %v", res["currentModel"])
	}
	if res["instanceId"] != "i-new" {
		t.Errorf("expected instanceId i-new, got %v", res["instanceId"])
	}
	if res["freebucksRefund"] != float64(5) {
		t.Errorf("expected freebucksRefund 5, got %v", res["freebucksRefund"])
	}
	if releasedInstance != "i-old" {
		t.Errorf("expected the held instance i-old to be released, got %q", releasedInstance)
	}
	if admittedModel != "model-b" {
		t.Errorf("expected admission for model-b, got %q", admittedModel)
	}

	updatedConn, err := handler.Repo.GetProviderConnectionByID("fb-conn-1")
	if err != nil {
		t.Fatalf("failed to query updated connection: %v", err)
	}
	if !strings.Contains(updatedConn.Data, `"freebuffModel":"model-b"`) {
		t.Errorf("expected connection data to contain freebuffModel model-b, got %s", updatedConn.Data)
	}
}

func TestHandleFreebuffSessionSwitch_AlreadyOnModelDoesNotBurnASeat(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	seedFreebuffConnection(t, database, "fb-conn-1", `{"authToken":"tok-1"}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected no session mutation, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active","currentModel":"model-a","instanceId":"i-old","expiresAt":"2030-01-01T00:00:00Z"}`))
	}))
	defer server.Close()

	oldURL := freebuffAPIBaseURL
	freebuffAPIBaseURL = server.URL
	defer func() { freebuffAPIBaseURL = oldURL }()

	handler := NewOAuthHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/session/switch",
		strings.NewReader(`{"connectionId":"fb-conn-1","model":"model-a"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleFreebuffSessionSwitch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if res["switched"] != false {
		t.Errorf("expected switched=false, got %v", res["switched"])
	}
	if res["instanceId"] != "i-old" {
		t.Errorf("expected the existing seat i-old, got %v", res["instanceId"])
	}
}

func TestHandleFreebuffSessionSwitch_ValidationErrors(t *testing.T) {
	handler := NewOAuthHandler(nil)

	// Missing model
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/session/switch",
		strings.NewReader(`{"connectionId":"fb-conn-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleFreebuffSessionSwitch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing model, got %d", rec.Code)
	}

	// Invalid JSON
	req2 := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/session/switch", strings.NewReader(`{`))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.HandleFreebuffSessionSwitch(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec2.Code)
	}

	// GET is not allowed
	req3 := httptest.NewRequest(http.MethodGet, "/api/oauth/freebuff/session/switch", nil)
	rec3 := httptest.NewRecorder()
	handler.HandleFreebuffSessionSwitch(rec3, req3)
	if rec3.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", rec3.Code)
	}
}
