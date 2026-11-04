package media

import (
	"bytes"
	"database/sql"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/providers"
)

func setupMultimodalTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	return setupEmbeddingsTestDB(t)
}

func seedOpenAIConn(t *testing.T, database *sql.DB, baseURL string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": baseURL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'openai', 'apikey', 'OpenAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func seedMiMoConn(t *testing.T, database *sql.DB, baseURL string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-mimo", "baseUrl": baseURL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-mimo', 'xiaomi-mimo', 'apikey', 'MiMo Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed mimo connection: %v", err)
	}
}

func TestHandleImages_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/images/generations") {
			t.Errorf("expected path /images/generations, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Bearer sk-test, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/img.png"}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/dall-e-3","prompt":"a cat"}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 1 {
		t.Errorf("expected 1 image, got %d", len(data))
	}
}

func TestHandleImages_MissingModel(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleImages_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid prompt"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/dall-e-3"}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 from upstream, got %d", rec.Code)
	}
}

func TestHandleAudioSpeech_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			t.Errorf("expected path /audio/speech, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ID3fakeaudio"))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/tts-1","input":"hello","voice":"alloy"}`
	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioSpeech(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("expected audio/mpeg passthrough, got %q", ct)
	}
	if rec.Body.String() != "ID3fakeaudio" {
		t.Errorf("expected raw audio body, got %q", rec.Body.String())
	}
}

func TestHandleAudioSpeech_MissingModel(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"input":"hi"}`
	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioSpeech(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAudioSpeech_MiMo(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected path /v1/chat/completions, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-mimo" {
			t.Errorf("expected Bearer sk-mimo, got %q", auth)
		}
		var reqBody struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Audio struct {
				Format string `json:"format"`
				Voice  string `json:"voice"`
			} `json:"audio"`
		}
		if err := json.UnmarshalRead(r.Body, &reqBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		if reqBody.Model != "mimo-v2.5-tts" {
			t.Errorf("expected model mimo-v2.5-tts, got %q", reqBody.Model)
		}
		if reqBody.Stream {
			t.Error("expected stream=false")
		}
		if len(reqBody.Messages) != 2 || reqBody.Messages[0].Role != "user" || reqBody.Messages[1].Role != "assistant" {
			t.Fatalf("unexpected messages: %+v", reqBody.Messages)
		}
		if !strings.Contains(reqBody.Messages[0].Content, "Speak in English") {
			t.Errorf("expected language hint, got %q", reqBody.Messages[0].Content)
		}
		if reqBody.Audio.Format != "wav" || reqBody.Audio.Voice != "冰糖" {
			t.Errorf("unexpected audio cfg: %+v", reqBody.Audio)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"aGVsbG8=","format":"wav"}}}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedMiMoConn(t, database, upstream.URL+"/v1/chat/completions")

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"mimo/mimo-v2.5-tts/冰糖","input":"hello world","language":"English","style":"cheerful"}`
	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleAudioSpeech(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("expected audio/wav, got %q", ct)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("expected decoded audio 'hello', got %q", rec.Body.String())
	}
}

func TestHandleAudioTranscriptions_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/transcriptions") {
			t.Errorf("expected path /audio/transcriptions, got %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart content-type, got %q", r.Header.Get("Content-Type"))
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Bearer sk-test, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"text":"hello world"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	buf.WriteString("--boundary\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"model\"\r\n\r\nopenai/whisper-1\r\n")
	buf.WriteString("--boundary--\r\n")
	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	rec := httptest.NewRecorder()

	handler.HandleAudioTranscriptions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["text"] != "hello world" {
		t.Errorf("expected text 'hello world', got %v", resp["text"])
	}
}

func TestHandleAudioTranscriptions_MissingModel(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	var buf bytes.Buffer
	buf.WriteString("--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.mp3\"\r\n\r\nbinary\r\n--boundary--\r\n")
	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	rec := httptest.NewRecorder()

	handler.HandleAudioTranscriptions(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing model, got %d", rec.Code)
	}
}

// ---- Video endpoint tests ----

func TestHandleVideoGenerations_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"vid-abc123","status":"pending"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/video-test","prompt":"a cat jumping"}`
	req := httptest.NewRequest("POST", "/v1/videos/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleVideoGenerations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp["id"] != "vid-abc123" {
		t.Errorf("expected id, got %v", resp["id"])
	}
}

func TestHandleVideoGenerations_MissingModel(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"prompt":"test"}`
	req := httptest.NewRequest("POST", "/v1/videos/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleVideoGenerations(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleVideoEdits_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"vid-edt456","status":"pending"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/video-test-edits","video":"vid-abc123","prompt":"make it faster"}`
	req := httptest.NewRequest("POST", "/v1/videos/edits", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleVideoEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVideoExtensions_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"vid-ext789","status":"pending"}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedOpenAIConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/video-test-ext","video":"vid-abc123","duration":10}`
	req := httptest.NewRequest("POST", "/v1/videos/extensions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleVideoExtensions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVideoGet_missingID(t *testing.T) {
	handler := chat.NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/v1/videos/", nil)
	rec := httptest.NewRecorder()

	handler.HandleVideoGet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleVideoGet_xai(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/vid-abc123") {
			t.Errorf("expected path ending with /vid-abc123, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"vid-abc123","status":"completed","video":{"url":"https://example.com/video.mp4"}}`))
	}))
	defer upstream.Close()

	cfg := providers.KnownProviders["xai"]
	cfg.VideoURL = upstream.URL + "/v1/videos"
	providers.KnownProviders["xai"] = cfg

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedXaiConn(t, database, upstream.URL)

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	req := httptest.NewRequest("GET", "/v1/videos/vid-abc123", nil)
	req.SetPathValue("id", "vid-abc123")
	rec := httptest.NewRecorder()

	handler.HandleVideoGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp["status"] != "completed" {
		t.Errorf("expected status completed, got %v", resp["status"])
	}
}

func seedXaiConn(t *testing.T, database *sql.DB, baseURL string) {
	t.Helper()
	url := strings.TrimSuffix(baseURL, "/v1/videos")
	data, _ := json.Marshal(map[string]any{"apiKey": "sk-xai-test", "baseUrl": url})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-xai', 'xai', 'apikey', 'xAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed xai connection: %v", err)
	}
}

func TestHandleImages_CloudflareFluxSchnellStripsDimensions(t *testing.T) {
	var mu sync.Mutex
	var captured map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &captured)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/flux.png"}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	data, _ := json.Marshal(map[string]any{"apiKey": "cf-token", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cf', 'cloudflare-ai', 'apikey', 'Cloudflare AI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"cf/@cf/black-forest-labs/flux-1-schnell","prompt":"a red fox","size":"1024x1024","width":1024,"height":1024}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if _, hasWidth := captured["width"]; hasWidth {
		t.Errorf("expected width stripped for flux-1-schnell, but found %v", captured["width"])
	}
	if _, hasHeight := captured["height"]; hasHeight {
		t.Errorf("expected height stripped for flux-1-schnell, but found %v", captured["height"])
	}
	if _, hasSize := captured["size"]; hasSize {
		t.Errorf("expected size stripped for flux-1-schnell, but found %v", captured["size"])
	}
	if prompt, _ := captured["prompt"].(string); prompt != "a red fox" {
		t.Errorf("expected prompt preserved, got %q", prompt)
	}
}

func TestHandleImages_CloudflareFlux2DevKeepsDimensions(t *testing.T) {
	// Negative check: flux-2-dev must NOT have width/height/size stripped,
	// only flux-1-schnell is subject to Cloudflare's closed schema (upstream #5373).
	var mu sync.Mutex
	var captured map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &captured)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/flux.png"}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	data, _ := json.Marshal(map[string]any{"apiKey": "cf-token", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cf', 'cloudflare-ai', 'apikey', 'Cloudflare AI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"cf/@cf/black-forest-labs/flux-2-dev","prompt":"a red fox","size":"1024x1024","width":1024,"height":1024}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if _, hasWidth := captured["width"]; !hasWidth {
		t.Errorf("expected width preserved for flux-2-dev, but it was stripped")
	}
	if _, hasHeight := captured["height"]; !hasHeight {
		t.Errorf("expected height preserved for flux-2-dev, but it was stripped")
	}
	if _, hasSize := captured["size"]; !hasSize {
		t.Errorf("expected size preserved for flux-2-dev, but it was stripped")
	}
	if prompt, _ := captured["prompt"].(string); prompt != "a red fox" {
		t.Errorf("expected prompt preserved, got %q", prompt)
	}
}

func TestHandleImages_CloudflareFluxSchnellComboStripsDimensions(t *testing.T) {
	// Combo path: a combo whose cloudflare flux-1-schnell member also has
	// width/height/size stripped (forwardMediaRequest combo branch).
	var mu sync.Mutex
	var captured map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &captured)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/flux.png"}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS combos (
		id TEXT PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		kind TEXT,
		models TEXT NOT NULL,
		strategy TEXT DEFAULT 'fallback',
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL,
		matchRules TEXT DEFAULT ''
	)`); err != nil {
		t.Fatalf("create combos table: %v", err)
	}

	repo := db.NewRepo(database)

	data, _ := json.Marshal(map[string]any{"apiKey": "cf-token", "baseUrl": upstream.URL})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cf', 'cloudflare-ai', 'apikey', 'Cloudflare AI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection: %v", err)
	}

	if err := repo.CreateCombo("flux-combo", "flux-combo", "image", `["cf/@cf/black-forest-labs/flux-1-schnell"]`, "fallback"); err != nil {
		t.Fatalf("seed flux combo: %v", err)
	}

	handler := newTestMediaHandler(repo)

	body := `{"model":"flux-combo","prompt":"a red fox","size":"1024x1024","width":1024,"height":1024}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if _, hasWidth := captured["width"]; hasWidth {
		t.Errorf("expected width stripped for flux-1-schnell combo member, but found %v", captured["width"])
	}
	if _, hasHeight := captured["height"]; hasHeight {
		t.Errorf("expected height stripped for flux-1-schnell combo member, but found %v", captured["height"])
	}
	if _, hasSize := captured["size"]; hasSize {
		t.Errorf("expected size stripped for flux-1-schnell combo member, but found %v", captured["size"])
	}
	if prompt, _ := captured["prompt"].(string); prompt != "a red fox" {
		t.Errorf("expected prompt preserved, got %q", prompt)
	}
}
