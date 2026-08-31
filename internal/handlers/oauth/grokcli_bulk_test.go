package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleOAuthGrokCliBulkImport_Array(t *testing.T) {
	database, cleanup := setupOAuthTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewOAuthHandler(repo)

	body := `[
		{
			"access_token": "token-1",
			"refresh_token": "refresh-1",
			"email": "user1@x.ai"
		},
		{
			"accessToken": "token-2",
			"refreshToken": "refresh-2",
			"displayName": "User 2 Account"
		}
	]`

	req := httptest.NewRequest("POST", "/api/oauth/grok-cli/bulk-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleOAuthGrokCliBulkImport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Total   int `json:"total"`
		Success int `json:"success"`
		Failed  int `json:"failed"`
		Results []struct {
			Index int    `json:"index"`
			Ok    bool   `json:"ok"`
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Total != 2 || resp.Success != 2 || resp.Failed != 0 {
		t.Errorf("expected 2 total, 2 success, 0 failed, got total=%d success=%d failed=%d", resp.Total, resp.Success, resp.Failed)
	}
	if resp.Results[0].Email != "user1@x.ai" {
		t.Errorf("expected user1@x.ai, got %s", resp.Results[0].Email)
	}
}

func TestHandleOAuthGrokCliBulkImport_WrappedAndConcatenated(t *testing.T) {
	database, cleanup := setupOAuthTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewOAuthHandler(repo)

	// Test wrapped in {accounts: [...]}
	bodyWrapped := `{"accounts":[{"access_token":"tok-a","email":"a@x.ai"}]}`
	req := httptest.NewRequest("POST", "/api/oauth/grok-cli/bulk-import", strings.NewReader(bodyWrapped))
	rec := httptest.NewRecorder()
	handler.HandleOAuthGrokCliBulkImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Test concatenated JSON objects `}{`
	bodyConcat := `{"access_token":"tok-b","email":"b@x.ai"}{"access_token":"tok-c","email":"c@x.ai"}`
	req2 := httptest.NewRequest("POST", "/api/oauth/grok-cli/bulk-import", strings.NewReader(bodyConcat))
	rec2 := httptest.NewRecorder()
	handler.HandleOAuthGrokCliBulkImport(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleOAuthGrokCliBulkImport_Invalid(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/grok-cli/bulk-import", strings.NewReader(`invalid json`))
	rec := httptest.NewRecorder()
	handler.HandleOAuthGrokCliBulkImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}
