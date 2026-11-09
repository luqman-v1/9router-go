package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uploadLogoRequest builds a multipart POST carrying one file under the field
// name the handler reads. filename is used verbatim so a traversal attempt can
// be posted as-is.
func uploadLogoRequest(t *testing.T, filename, content string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(logoUploadField, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/settings/logo", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// TestUploadLogoStoresFileAndRewritesSetting pins the happy path: the file
// lands under DATA_DIR/uploads under a fixed name, and appLogoUrl is rewritten
// to the path this gateway serves it from.
func TestUploadLogoStoresFileAndRewritesSetting(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := uploadLogoRequest(t, "my-brand.png", "\x89PNG\r\n\x1a\nfake-bytes")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := payload["appLogoUrl"]; got != "/uploads/logo.png" {
		t.Errorf("appLogoUrl = %v, want /uploads/logo.png", got)
	}

	stored, err := os.ReadFile(filepath.Join(os.Getenv("DATA_DIR"), "uploads", "logo.png"))
	if err != nil {
		t.Fatalf("read stored logo: %v", err)
	}
	if !bytes.Contains(stored, []byte("fake-bytes")) {
		t.Error("the stored file does not carry the uploaded bytes")
	}

	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if raw["appLogoUrl"] != "/uploads/logo.png" {
		t.Errorf("stored appLogoUrl = %v, want /uploads/logo.png", raw["appLogoUrl"])
	}
}

// TestUploadLogoRejectsUnsupportedType pins the extension allowlist: a format
// a browser will not render in an <img> must not be stored.
func TestUploadLogoRejectsUnsupportedType(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := uploadLogoRequest(t, "payload.php", "<?php echo 1; ?>")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("DATA_DIR"), "uploads", "logo.php")); !os.IsNotExist(err) {
		t.Error("an unsupported upload was written to disk")
	}
}

// TestUploadLogoRejectsOversizeFile pins the size cap. The cap is enforced on
// the bytes read, not on the client's declared Content-Length, so the request
// here carries a body larger than logoMaxBytes.
func TestUploadLogoRejectsOversizeFile(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	oversize := strings.Repeat("A", logoMaxBytes+1024)
	req := uploadLogoRequest(t, "huge.png", oversize)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("DATA_DIR"), "uploads", "logo.png")); !os.IsNotExist(err) {
		t.Error("an oversize upload was left on disk")
	}
}

// TestUploadLogoReplacesPreviousExtension pins the single-slot behaviour: a
// second upload with a different extension removes the first, so a stale asset
// is never served by a URL the operator no longer chose.
func TestUploadLogoReplacesPreviousExtension(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, uploadLogoRequest(t, "first.svg", "<svg xmlns='http://www.w3.org/2000/svg'/>"))
	if rec.Code != http.StatusOK {
		t.Fatalf("first upload status = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, uploadLogoRequest(t, "second.png", "\x89PNG\r\n\x1a\nsecond"))
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload status = %d, want 200", rec.Code)
	}

	dir := filepath.Join(os.Getenv("DATA_DIR"), "uploads")
	if _, err := os.Stat(filepath.Join(dir, "logo.svg")); !os.IsNotExist(err) {
		t.Error("the previous logo.svg survived a PNG upload")
	}
	if _, err := os.Stat(filepath.Join(dir, "logo.png")); err != nil {
		t.Errorf("the new logo.png is missing: %v", err)
	}
}

// TestServeUploadsRejectsTraversal pins that the client's filename never
// becomes a path component: a request for a path outside the uploads directory
// is answered 404, and nothing outside it is read.
func TestServeUploadsRejectsTraversal(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// A file that exists outside the uploads directory, which a traversal
	// attempt would try to reach.
	secret := filepath.Join(os.Getenv("DATA_DIR"), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not serve"), 0o600); err != nil {
		t.Fatalf("write the decoy: %v", err)
	}

	for _, path := range []string{
		"/uploads/../secret.txt",
		"/uploads/..%2fsecret.txt",
		"/uploads/logo.php",
		"/uploads/other.png",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
		if bytes.Contains(rec.Body.Bytes(), []byte("do not serve")) {
			t.Errorf("GET %s served a file outside the uploads directory", path)
		}
	}
}

// TestServeUploadsServesStoredLogo pins the read path: a stored logo is served
// with its media type, and the upload that wrote it is the one that answers.
func TestServeUploadsServesStoredLogo(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, uploadLogoRequest(t, "brand.svg", "<svg xmlns='http://www.w3.org/2000/svg'></svg>"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/logo.svg", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("serve status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("<svg")) {
		t.Error("the served body is not the stored logo")
	}
}

// TestServeUploadsMissingLogo404 pins that an absent logo is a 404 rather than
// an empty 200, so the sidebar's <img> falls back instead of rendering a blank
// box from a successful response.
func TestServeUploadsMissingLogo404(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/uploads/logo.png", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestClearingAppLogoUrlRemovesUploadedFile pins the gap this closes: clearing
// the setting used to leave the uploaded file on disk forever, served by
// nothing. The file must go with the setting.
func TestClearingAppLogoUrlRemovesUploadedFile(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, uploadLogoRequest(t, "brand.png", "\x89PNG\r\n\x1a\nbytes"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", rec.Code)
	}
	stored := filepath.Join(os.Getenv("DATA_DIR"), "uploads", "logo.png")
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("the uploaded logo is missing before the clear: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"appLogoUrl":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Error("clearing appLogoUrl left the uploaded file on disk")
	}

	// The served path must 404 now, so the sidebar falls back to the default.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/logo.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("serve after clear = %d, want 404", rec.Code)
	}
}

// TestClearingAppLogoUrlKeepsOperatorOwnedFile pins the boundary of the
// cleanup: only an asset this gateway uploaded is removed. An operator who
// pointed the setting at their own path keeps that file, because clearing a
// setting must never delete a file the gateway does not own.
func TestClearingAppLogoUrlKeepsOperatorOwnedFile(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	own := filepath.Join(os.Getenv("DATA_DIR"), "my-logo.png")
	if err := os.WriteFile(own, []byte("operator asset"), 0o600); err != nil {
		t.Fatalf("write the operator's file: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"appLogoUrl":"/my-logo.png"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"appLogoUrl":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200", rec.Code)
	}
	if _, err := os.Stat(own); err != nil {
		t.Error("clearing appLogoUrl deleted a file the gateway does not own")
	}
}

// TestUploadAfterClearRestoresTheLogo pins that the slot is reusable: a clear
// removes the file, and a later upload writes it again under the same name.
func TestUploadAfterClearRestoresTheLogo(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, uploadLogoRequest(t, "brand.svg", "<svg xmlns='http://www.w3.org/2000/svg'/>"))
	if rec.Code != http.StatusOK {
		t.Fatalf("first upload status = %d, want 200", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"appLogoUrl":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, uploadLogoRequest(t, "brand.svg", "<svg xmlns='http://www.w3.org/2000/svg' width='9'/>"))
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload status = %d, want 200", rec.Code)
	}
	stored, err := os.ReadFile(filepath.Join(os.Getenv("DATA_DIR"), "uploads", "logo.svg"))
	if err != nil {
		t.Fatalf("read the restored logo: %v", err)
	}
	if !bytes.Contains(stored, []byte("width='9'")) {
		t.Error("the restored logo does not carry the second upload's bytes")
	}
}

// TestSettingAnExternalLogoDoesNotRemoveTheFile pins that pointing the setting
// at a remote URL leaves the uploaded asset alone: the operator may switch
// between a hosted logo and an uploaded one, and the switch is not a delete.
func TestSettingAnExternalLogoDoesNotRemoveTheFile(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, uploadLogoRequest(t, "brand.png", "\x89PNG\r\n\x1a\nbytes"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"appLogoUrl":"https://example.com/logo.png"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set external status = %d, want 200", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("DATA_DIR"), "uploads", "logo.png")); err != nil {
		t.Error("switching to an external URL deleted the uploaded file")
	}
}
