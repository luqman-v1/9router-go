package media

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestForwardTTS_DoesNotHijackElevenPrefixedNonElevenModel(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"custom-provider/eleven_test","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioSpeech(rec, req)

	if strings.Contains(rec.Body.String(), "elevenlabs") {
		t.Fatalf("eleven_ prefixed model was hijacked to elevenlabs: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "custom-provider") {
		t.Fatalf("expected custom-provider in response, got: %s", rec.Body.String())
	}
	_ = http.StatusOK
}
