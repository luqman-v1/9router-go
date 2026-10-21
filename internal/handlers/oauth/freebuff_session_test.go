package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
)

func TestHandleFreebuffSessionStatus_MethodNotAllowed(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	req := httptest.NewRequest(http.MethodPost, "/api/oauth/freebuff/session", nil)
	rec := httptest.NewRecorder()

	handler.HandleFreebuffSessionStatus(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleFreebuffSessionStatus_NoConnection(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/freebuff/session", nil)
	rec := httptest.NewRecorder()

	handler.HandleFreebuffSessionStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if res["status"] != "none" {
		t.Fatalf("expected status=none, got %v", res["status"])
	}
}

func TestHandleFreebuffSessionStatus_ActiveSession(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	var authHeader, uaHeader, acceptHeader string
	var upstreamCalls int64

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamCalls, 1)
		authHeader = r.Header.Get("Authorization")
		uaHeader = r.Header.Get("User-Agent")
		acceptHeader = r.Header.Get("Accept")

		if r.URL.Path != "/api/v1/freebuff/session" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "active",
			"model": "z-ai/glm-5.3-flash",
			"instanceId": "inst-12345",
			"expiresAt": "2026-09-17T01:00:00Z",
			"freebucks": {
				"balance": 20
			}
		}`))
	}))
	defer mockUpstream.Close()

	origBaseURL := freebuffAPIBaseURL
	freebuffAPIBaseURL = mockUpstream.URL
	defer func() { freebuffAPIBaseURL = origBaseURL }()

	// Seed active freebuff connection
	connData, _ := json.Marshal(map[string]any{"authToken": "fb-secret-token-123"})
	_, err := database.Exec(`
		INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
		VALUES ('fb-conn-1', 'freebuff', 'oauth', 'Test FB', 1, ?, '2026-09-17T00:00:00Z', '2026-09-17T00:00:00Z')
	`, string(connData))
	if err != nil {
		t.Fatalf("failed to seed connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/freebuff/session?connectionId=fb-conn-1", nil)
	rec := httptest.NewRecorder()

	handler.HandleFreebuffSessionStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if authHeader != "Bearer fb-secret-token-123" {
		t.Errorf("expected Authorization Bearer fb-secret-token-123, got %q", authHeader)
	}
	if uaHeader != constants.FreebuffClientUserAgent {
		t.Errorf("expected User-Agent %s, got %q", constants.FreebuffClientUserAgent, uaHeader)
	}
	if acceptHeader != "application/json" {
		t.Errorf("expected Accept application/json, got %q", acceptHeader)
	}

	var res struct {
		Status       string         `json:"status"`
		CurrentModel string         `json:"currentModel"`
		InstanceID   string         `json:"instanceId"`
		ExpiresAt    string         `json:"expiresAt"`
		Freebucks    map[string]any `json:"freebucks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if res.Status != "active" {
		t.Errorf("expected status active, got %q", res.Status)
	}
	if res.CurrentModel != "z-ai/glm-5.3-flash" {
		t.Errorf("expected currentModel z-ai/glm-5.3-flash, got %q", res.CurrentModel)
	}
	if res.InstanceID != "inst-12345" {
		t.Errorf("expected instanceId inst-12345, got %q", res.InstanceID)
	}
	if res.ExpiresAt != "2026-09-17T01:00:00Z" {
		t.Errorf("expected expiresAt 2026-09-17T01:00:00Z, got %q", res.ExpiresAt)
	}
	if res.Freebucks == nil || res.Freebucks["balance"] != float64(20) {
		t.Errorf("expected freebucks.balance=20, got %v", res.Freebucks)
	}

	updatedConn, err := handler.Repo.GetProviderConnectionByID("fb-conn-1")
	if err != nil {
		t.Fatalf("failed to query updated connection: %v", err)
	}
	if !strings.Contains(updatedConn.Data, `"freebuffModel":"z-ai/glm-5.3-flash"`) {
		t.Errorf("expected connection data to contain freebuffModel, got %s", updatedConn.Data)
	}
}
func TestHandleFreebuffSessionStatus_Unauthorized(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
	}))
	defer mockUpstream.Close()

	origBaseURL := freebuffAPIBaseURL
	freebuffAPIBaseURL = mockUpstream.URL
	defer func() { freebuffAPIBaseURL = origBaseURL }()

	connData, _ := json.Marshal(map[string]any{"accessToken": "expired-token"})
	_, err := database.Exec(`
		INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
		VALUES ('fb-conn-2', 'freebuff', 'oauth', 'Test FB 2', 1, ?, '2026-09-17T00:00:00Z', '2026-09-17T00:00:00Z')
	`, string(connData))
	if err != nil {
		t.Fatalf("failed to seed connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/freebuff/session", nil)
	rec := httptest.NewRecorder()

	handler.HandleFreebuffSessionStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if res["status"] != "unauthorized" {
		t.Errorf("expected status=unauthorized, got %v", res["status"])
	}
	if res["connectionId"] != "fb-conn-2" {
		t.Errorf("expected the report to name its connection, got %v", res["connectionId"])
	}
}

// A banned account is refused with its own status upstream. Reporting it as a
// plain unauthorized would send the user back through the login flow for a
// credential that can never work.
func TestHandleFreebuffSessionStatus_Banned(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"banned"}`))
	}))
	defer mockUpstream.Close()

	origBaseURL := freebuffAPIBaseURL
	freebuffAPIBaseURL = mockUpstream.URL
	defer func() { freebuffAPIBaseURL = origBaseURL }()

	_, err := database.Exec(`
		INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
		VALUES ('fb-banned', 'freebuff', 'oauth', 'Banned Account', 1, '{"accessToken":"dead-token"}', '2026-09-17T00:00:00Z', '2026-09-17T00:00:00Z')
	`)
	if err != nil {
		t.Fatalf("failed to seed connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/freebuff/session?connectionId=fb-banned", nil)
	rec := httptest.NewRecorder()
	handler.HandleFreebuffSessionStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if res["status"] != "banned" {
		t.Errorf("expected status=banned, got %v", res["status"])
	}
	if res["connectionName"] != "Banned Account" {
		t.Errorf("expected connectionName to be reported, got %v", res["connectionName"])
	}
}

func TestHandleFreebuffSessionStatus_ReportsQuotaAndCountry(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	handler := NewOAuthHandler(db.NewRepo(database))

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "active",
			"accessTier": "limited",
			"model": "deepseek/deepseek-v4-flash",
			"instanceId": "inst-quota",
			"expiresAt": "2026-09-21T17:10:47.306Z",
			"countryCode": "ID",
			"countryBlockReason": "country_not_allowed",
			"rateLimit": {
				"model": "deepseek/deepseek-v4-flash",
				"limit": 6,
				"recentCount": 1,
				"poolLabel": "Daily",
				"resetAt": "2026-09-22T07:00:00.000Z",
				"resetTimeZone": "America/Los_Angeles"
			}
		}`))
	}))
	defer mockUpstream.Close()

	origBaseURL := freebuffAPIBaseURL
	freebuffAPIBaseURL = mockUpstream.URL
	defer func() { freebuffAPIBaseURL = origBaseURL }()

	_, err := database.Exec(`
		INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
		VALUES ('fb-quota', 'freebuff', 'oauth', 'Quota Account', 1, '{"accessToken":"tok"}', '2026-09-17T00:00:00Z', '2026-09-17T00:00:00Z')
	`)
	if err != nil {
		t.Fatalf("failed to seed connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/freebuff/session?connectionId=fb-quota", nil)
	rec := httptest.NewRecorder()
	handler.HandleFreebuffSessionStatus(rec, req)

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if res["status"] != "active" {
		t.Fatalf("expected status=active, got %v", res["status"])
	}
	if res["accessTier"] != "limited" {
		t.Errorf("expected accessTier=limited, got %v", res["accessTier"])
	}
	if res["countryCode"] != "ID" {
		t.Errorf("expected countryCode=ID, got %v", res["countryCode"])
	}
	if res["countryBlockReason"] != "country_not_allowed" {
		t.Errorf("expected countryBlockReason to be reported, got %v", res["countryBlockReason"])
	}

	rateLimit, ok := res["rateLimit"].(map[string]any)
	if !ok {
		t.Fatalf("expected a rateLimit block, got %v", res["rateLimit"])
	}
	if rateLimit["limit"] != float64(6) || rateLimit["recentCount"] != float64(1) {
		t.Errorf("unexpected rateLimit: %v", rateLimit)
	}
	if rateLimit["poolLabel"] != "Daily" {
		t.Errorf("expected poolLabel=Daily, got %v", rateLimit["poolLabel"])
	}
}
