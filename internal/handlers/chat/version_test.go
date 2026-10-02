package chat

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/updater"
)

func TestHandleVersion(t *testing.T) {
	handler := NewChatHandler(nil, nil)
	req := httptest.NewRequest("GET", "/api/version", nil)
	rec := httptest.NewRecorder()

	handler.HandleVersion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var info updater.UpdateInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if info.CurrentVersion == "" {
		t.Errorf("expected non-empty CurrentVersion")
	}
}

func TestHandleVersionStatus(t *testing.T) {
	handler := NewChatHandler(nil, nil)
	req := httptest.NewRequest("GET", "/api/version/status", nil)
	rec := httptest.NewRecorder()

	handler.HandleVersionStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var status updater.UpdaterStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if status.CurrentVersion == "" {
		t.Errorf("expected currentVersion")
	}
}

func TestHandleToggleAutoUpdate(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo, nil)

	body := `{"enabled":true}`
	req := httptest.NewRequest("POST", "/api/version/auto-update", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleToggleAutoUpdate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if !updater.IsAutoUpdateEnabled() {
		t.Errorf("expected updater.IsAutoUpdateEnabled to be true")
	}

	settings, err := repo.GetSettings()
	if err != nil || !settings.AutoUpdate {
		t.Errorf("expected settings.AutoUpdate to be true in DB, err=%v", err)
	}
}

// A digest-less manifest advertising a newer version is a lead, not an answer:
// since the checksum became mandatory, a manifest that cannot describe its
// asset with a sha256 cannot produce an installable update, so CheckUpdate
// falls through to the release API that can. This manifest carries the digest
// precisely so it still answers on its own — the fallthrough is covered in
// internal/updater. Without it this test would be asserting the old
// manifest-always-wins behaviour, where `9router-go update` always failed
// with "release carries no checksum".
func TestHandleCheckUpdate_Mock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest := map[string]any{
			"latestVersion": "9.9.9",
			"downloadUrl":   "https://example.com/download",
			"releaseNotes":  "Test release notes",
			"sha256":        strings.Repeat("ab", 32),
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, manifest)
	}))
	defer server.Close()

	os.Setenv("UPDATE_URL", server.URL)
	defer os.Unsetenv("UPDATE_URL")

	handler := NewChatHandler(nil, nil)
	req := httptest.NewRequest("GET", "/api/version/check", nil)
	rec := httptest.NewRecorder()

	handler.HandleCheckUpdate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var info updater.UpdateInfo
	json.Unmarshal(rec.Body.Bytes(), &info)
	if info.LatestVersion != "9.9.9" || !info.HasUpdate {
		t.Errorf("expected latestVersion 9.9.9 and hasUpdate=true, got %v", info)
	}
}

func TestHandleTriggerUpdate_UpToDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest := map[string]any{
			"latestVersion": updater.CurrentVersion,
			"downloadUrl":   "https://example.com/download",
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, manifest)
	}))
	defer server.Close()

	os.Setenv("UPDATE_URL", server.URL)
	defer os.Unsetenv("UPDATE_URL")

	handler := NewChatHandler(nil, nil)
	req := httptest.NewRequest("POST", "/api/version/update", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()

	handler.HandleTriggerUpdate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res map[string]any
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res["status"] != "up_to_date" {
		t.Errorf("expected status 'up_to_date', got %v", res["status"])
	}
}

func TestHandleChangelog(t *testing.T) {
	handler := NewChatHandler(nil, nil)
	req := httptest.NewRequest("GET", "/api/changelog", nil)
	rec := httptest.NewRecorder()

	handler.HandleChangelog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Changelog") {
		t.Errorf("expected response to contain 'Changelog', got: %s", rec.Body.String())
	}
}
