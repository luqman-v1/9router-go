package media

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleImages_OutputFormatPreserved(t *testing.T) {
	for _, format := range []string{"png", "webp", "jpeg", "jpg"} {
		t.Run(format, func(t *testing.T) {
			var captured map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &captured)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"created":1,"data":[{"b64_json":"AAAA"}]}`))
			}))
			defer upstream.Close()

			database, cleanup := setupMultimodalTestDB(t)
			defer cleanup()
			seedOpenAIConn(t, database, upstream.URL)
			repo := db.NewRepo(database)
			handler := newTestMediaHandler(repo)

			body := `{"model":"openai/dall-e-3","prompt":"cat","output_format":"` + format + `"}`
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.HandleImages(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if got, _ := captured["output_format"].(string); got != format {
				t.Fatalf("forwarded output_format = %q, want %q", got, format)
			}
		})
	}
}

func TestHandleImages_OutputFormatInvalidReturns400(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/dall-e-3","prompt":"cat","output_format":"bmp"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImages(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream hit %d times, want 0", upstreamHits)
	}
}

func TestHandleImages_OutputFormatNonStringReturns400(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/dall-e-3","prompt":"cat","output_format":42}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImages(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
