package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"
)

func TestDashboard_HandleTestRTK(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	body := []byte(`{"text":"diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tokensaver/rtk/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleTestRTK(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got: %d (%s)", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if cat, _ := res["detectedCategory"].(string); cat != "git" {
		t.Errorf("expected detectedCategory = 'git', got: %v", cat)
	}
	if _, ok := res["originalTokens"]; !ok {
		t.Errorf("expected originalTokens in response")
	}
	if _, ok := res["compressedTokens"]; !ok {
		t.Errorf("expected compressedTokens in response")
	}
}

func TestDashboard_HandleGetRTKFilters(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/tokensaver/rtk/filters", nil)
	rec := httptest.NewRecorder()

	h.HandleGetRTKFilters(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got: %d (%s)", rec.Code, rec.Body.String())
	}

	var res struct {
		Filters []struct {
			ID       string `json:"id"`
			Label    string `json:"label"`
			Category string `json:"category"`
			Enabled  bool   `json:"enabled"`
		} `json:"filters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(res.Filters) != 55 {
		t.Fatalf("expected 55 filters, got: %d", len(res.Filters))
	}
}

func TestDashboard_HandleTestCaveman_PromptPreview(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	// Mode preview / empty text
	body := []byte(`{"level":"full","language":"id"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tokensaver/caveman/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleTestCaveman(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got: %d (%s)", rec.Code, rec.Body.String())
	}

	var res TestCavemanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !strings.Contains(res.Text, "manusia purba") {
		t.Errorf("expected Indonesian caveman prompt, got: %s", res.Text)
	}
}

func TestDashboard_HandleTestCaveman_InputCompression(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	body := []byte(`{"text":"Hello, can you please help me to fix the database bug? Thank you!"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tokensaver/caveman/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleTestCaveman(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got: %d (%s)", rec.Code, rec.Body.String())
	}

	var res TestCavemanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !strings.Contains(res.Text, "fix the database bug?") {
		t.Errorf("expected compressed substance, got: %s", res.Text)
	}
	if res.OriginalTokens <= res.CompressedTokens {
		t.Errorf("expected originalTokens (%d) > compressedTokens (%d)", res.OriginalTokens, res.CompressedTokens)
	}
	if res.SavedPct <= 0 {
		t.Errorf("expected savedPct > 0, got: %f", res.SavedPct)
	}
}
