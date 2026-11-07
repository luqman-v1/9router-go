package media

import (
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
)

func TestIsCloudflareImageModel(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     bool
	}{
		{"cloudflare-ai", "@cf/black-forest-labs/flux-1-schnell", true},
		{"cloudflare-ai", "@cf/black-forest-labs/flux-2-dev", true},
		{"cloudflare-ai", "@cf/bytedance/stable-diffusion-xl-lightning", true},
		{"cf", "@cf/black-forest-labs/flux-2-klein-9b", true},
		{"openai", "@cf/black-forest-labs/flux-2-dev", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			got := isCloudflareImageModel(&chat.ModelInfo{Provider: tc.provider, Model: tc.model})
			if got != tc.want {
				t.Errorf("isCloudflareImageModel(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

func TestCloudflareDimensions(t *testing.T) {
	tests := []struct {
		name         string
		size         string
		w, h         any
		wantW, wantH int
	}{
		{"from size", "1024x768", nil, nil, 1024, 768},
		{"from fields", "", float64(512), float64(512), 512, 512},
		{"string fields", "", "640", "480", 640, 480},
		{"empty", "", nil, nil, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, h := cloudflareDimensions(tc.size, tc.w, tc.h)
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("cloudflareDimensions(%q) = (%d,%d), want (%d,%d)", tc.size, w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestNormalizeCloudflareImageResponse(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantData []map[string]string
	}{
		{
			name:     "result image b64",
			in:       `{"result":{"image":"aGVsbG8="},"success":true,"errors":[]}`,
			wantData: []map[string]string{{"b64_json": "aGVsbG8="}},
		},
		{
			name:     "result url string",
			in:       `{"result":"https://cdn.example/a.png"}`,
			wantData: []map[string]string{{"url": "https://cdn.example/a.png"}},
		},
		{
			name:     "already openai shape",
			in:       `{"created":1,"data":[{"url":"https://x/y.png"}]}`,
			wantData: []map[string]string{{"url": "https://x/y.png"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := normalizeCloudflareImageResponse([]byte(tc.in))
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			var parsed struct {
				Created int                 `json:"created"`
				Data    []map[string]string `json:"data"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("unmarshal normalized: %v (%s)", err, out)
			}
			if parsed.Created == 0 {
				t.Errorf("created = 0")
			}
			if len(parsed.Data) != len(tc.wantData) {
				t.Fatalf("data = %v, want %v", parsed.Data, tc.wantData)
			}
			for i := range tc.wantData {
				if parsed.Data[i]["b64_json"] != tc.wantData[i]["b64_json"] ||
					parsed.Data[i]["url"] != tc.wantData[i]["url"] {
					t.Errorf("data[%d] = %v, want %v", i, parsed.Data[i], tc.wantData[i])
				}
			}
		})
	}
}

func seedCloudflareConn(t *testing.T, rawDB *sql.DB, baseURL string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": "cf-token", "baseUrl": baseURL})
	if _, err := rawDB.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cf', 'cloudflare-ai', 'apikey', 'Cloudflare AI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-12-01T00:00:00Z')`, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection: %v", err)
	}
}

func TestHandleImages_CloudflareFluxSchnellRuns(t *testing.T) {
	var captured map[string]any
	var gotPath, gotCT string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"image":"aGVsbG8="},"success":true,"errors":[]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	seedCloudflareConn(t, database, upstream.URL)
	handler := newTestMediaHandler(repo)

	body := `{"model":"cf/@cf/black-forest-labs/flux-1-schnell","prompt":"a red fox","size":"1024x1024","width":1024,"height":1024,"steps":4}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if want := "/ai/run/@cf/black-forest-labs/flux-1-schnell"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("content-type = %q, want application/json", gotCT)
	}
	if _, has := captured["width"]; has {
		t.Errorf("width must be stripped for flux-1-schnell")
	}
	if _, has := captured["height"]; has {
		t.Errorf("height must be stripped for flux-1-schnell")
	}
	if p, _ := captured["prompt"].(string); p != "a red fox" {
		t.Errorf("prompt = %v, want a red fox", captured["prompt"])
	}
	if s, _ := captured["steps"].(float64); s != 4 {
		t.Errorf("steps = %v, want 4", captured["steps"])
	}
	var out struct {
		Created int `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("client body not json: %v (%s)", err, rec.Body.String())
	}
	if len(out.Data) != 1 || out.Data[0].B64JSON != "aGVsbG8=" {
		t.Errorf("normalized response = %+v, want b64 aGVsbG8=", out.Data)
	}
}

func TestHandleImages_CloudflareFlux2KleinMultipart(t *testing.T) {
	// flux-2-klein-9b is the model from the issue #864 report.
	var gotPath, gotCT string
	var parts map[string]string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		parts = multipartFields(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"image":"VGVzdEltYWdl"},"success":true,"errors":[]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	seedCloudflareConn(t, database, upstream.URL)
	handler := newTestMediaHandler(repo)

	body := `{"model":"cf/@cf/black-forest-labs/flux-2-klein-9b","prompt":"cyberpunk city","size":"1024x1024","width":1024,"height":1024}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/ai/run/@cf/black-forest-labs/flux-2-klein-9b" {
		t.Errorf("path = %q, want /ai/run/@cf/black-forest-labs/flux-2-klein-9b", gotPath)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", gotCT)
	}
	if parts["prompt"] != "cyberpunk city" {
		t.Errorf("prompt = %q, want cyberpunk city", parts["prompt"])
	}
	if parts["width"] != "1024" {
		t.Errorf("width = %q, want 1024", parts["width"])
	}
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not normalized json: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0].B64JSON == "" {
		t.Errorf("normalized response missing image: %s", rec.Body.String())
	}
}

func TestHandleImages_CloudflareFlux2WithReferenceImage(t *testing.T) {
	var gotPath string
	var imageB64 string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		parts := multipartFields(t, r)
		imageB64 = parts["image_b64"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"image":"REF"},"success":true,"errors":[]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	seedCloudflareConn(t, database, upstream.URL)
	handler := newTestMediaHandler(repo)

	// Reference image as base64 data URI — not an HTTP URL, so not fetched.
	body := `{"model":"cf/@cf/black-forest-labs/flux-2-dev","prompt":"colorize","image":"data:image/png;base64,REFIMG"}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasSuffix(gotPath, "/ai/run/@cf/black-forest-labs/flux-2-dev") {
		t.Errorf("path = %q", gotPath)
	}
	if imageB64 != "data:image/png;base64,REFIMG" {
		t.Errorf("image_b64 = %q, want data:image/png;base64,REFIMG", imageB64)
	}
}

// multipartFields reads a multipart body from a mock request and returns its
// named parts.
func multipartFields(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse multipart content-type: %v", err)
	}
	fields := map[string]string{}
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(p)
		fields[p.FormName()] = string(b)
	}
	return fields
}


func seedCloudflareConnNamed(t *testing.T, rawDB *sql.DB, id, name string, priority int, baseURL string, apiKey string) {
	t.Helper()
	if apiKey == "" {
		apiKey = "cf-token"
	}
	data, _ := json.Marshal(map[string]any{"apiKey": apiKey, "baseUrl": baseURL})
	if _, err := rawDB.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		(?, 'cloudflare-ai', 'apikey', ?, ?, 1, ?, '2026-07-18T00:00:00Z', '2026-12-01T00:00:00Z')`, id, name, priority, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection %s: %v", name, err)
	}
}

func TestHandleImages_CloudflareRotationOnQuota(t *testing.T) {
	var quotaHits, okHits int32

	// Account A: daily free allocation exhausted (Cloudflare 4006, surfaced as 429).
	quotaUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quotaHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"message":"you have used up your daily free allocation of 10,000 neurons","code":4006}],"success":false,"result":{}}`))
	}))
	defer quotaUpstream.Close()

	// Account B: healthy, returns a real image.
	okUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"image":"QUOTA_OK_IMAGE"},"success":true,"errors":[]}`))
	}))
	defer okUpstream.Close()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	seedCloudflareConnNamed(t, database, "conn-cf-a", "account A (exhausted)", 1, quotaUpstream.URL, "token-a")
	seedCloudflareConnNamed(t, database, "conn-cf-b", "account B (healthy)", 2, okUpstream.URL, "token-b")
	handler := newTestMediaHandler(repo)

	body := `{"model":"cf/@cf/black-forest-labs/flux-2-klein-9b","prompt":"a fox","width":512,"height":512}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after rotating to account B, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "QUOTA_OK_IMAGE") {
		t.Errorf("response missing account B image: %s", rec.Body.String())
	}
	if quotaHits == 0 {
		t.Errorf("account A (quota) was never tried")
	}
	if okHits == 0 {
		t.Errorf("account B (healthy) was never tried")
	}

	// Account A must be parked: rateLimitedUntil set in the future.
	var data string
	if err := database.QueryRow(`SELECT data FROM providerConnections WHERE id = 'conn-cf-a'`).Scan(&data); err != nil {
		t.Fatalf("read conn-cf-a data: %v", err)
	}
	if until, ok := db.ConnectionCooldownUntil(data); !ok || until.Before(time.Now()) {
		t.Errorf("account A should be in quota cooldown, got ok=%v until=%v", ok, until)
	}

	// Second request: account A already parked, only account B may be hit.
	quotaHits, okHits = 0, 0
	rec2 := httptest.NewRecorder()
	handler.HandleImages(rec2, httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body)))
	if quotaHits != 0 {
		t.Errorf("second request re-hit the parked account (quotaHits=%d)", quotaHits)
	}
	if okHits == 0 {
		t.Errorf("second request should still reach account B")
	}
}
