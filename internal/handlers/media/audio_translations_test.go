package media

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleAudioTranslations_ForwardToUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/translations") {
			t.Errorf("expected path .../audio/translations, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "whisper-1") {
			t.Errorf("expected model forwarded, got: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"hello world"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/whisper-1"}`
	req := httptest.NewRequest("POST", "/v1/audio/translations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioTranslations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello world") {
		t.Errorf("expected translation text in response, got: %s", rec.Body.String())
	}
}

func TestHandleAudioTranslations_EmptyBodyRefused(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	req := httptest.NewRequest("POST", "/v1/audio/translations", nil)
	rec := httptest.NewRecorder()

	handler.HandleAudioTranslations(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("expected non-200 for empty body")
	}
}
