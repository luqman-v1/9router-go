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
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
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

func seedCloudflareConnWith(t *testing.T, rawDB *sql.DB, id, apiKey, baseURL string, priority int) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"apiKey": apiKey, "baseUrl": baseURL})
	if _, err := rawDB.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		(?, 'cloudflare-ai', 'apikey', ?, ?, 1, ?, '2026-07-18T00:00:00Z', '2026-12-01T00:00:00Z')`, id, "Cloudflare "+id, priority, string(data)); err != nil {
		t.Fatalf("seed cloudflare connection %s: %v", id, err)
	}
}

func blockedUntilForConn(t *testing.T, repo *db.Repo, id string) (time.Time, bool) {
	t.Helper()
	conn, err := repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		t.Fatalf("get connection %s: conn=%v err=%v", id, conn, err)
	}
	return db.ConnectionBlockedUntil(conn.Data)
}

func nextUTCMidnightBounds(before, after time.Time) (time.Time, time.Time) {
	b := before.UTC()
	a := after.UTC()
	return time.Date(b.Year(), b.Month(), b.Day()+1, 0, 0, 0, 0, time.UTC),
		time.Date(a.Year(), a.Month(), a.Day()+1, 0, 0, 0, 0, time.UTC)
}

func TestHandleImages_CloudflareRotationOnQuota(t *testing.T) {
	const quotaBody = `{"errors":[{"code":4006,"message":"you have used up your daily free allocation of 10,000 neurons"}],"success":false}`

	t.Run("rotates on 4006 quota and skips cooled account on next request", func(t *testing.T) {
		var acct1Hits, acct2Hits atomic.Int32
		var gotAuth2, gotPath2, gotPrompt2 string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-1/"):
				acct1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(quotaBody))
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-2/"):
				acct2Hits.Add(1)
				gotAuth2 = r.Header.Get("Authorization")
				gotPath2 = r.URL.Path
				gotPrompt2 = multipartFields(t, r)["prompt"]
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result":{"image":"QUNUMg=="},"success":true,"errors":[]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedCloudflareConnWith(t, database, "conn-cf-1", "tok-1", upstream.URL+"/accounts/acct-1", 1)
		seedCloudflareConnWith(t, database, "conn-cf-2", "tok-2", upstream.URL+"/accounts/acct-2", 2)
		handler := newTestMediaHandler(repo)

		reqBody := `{"model":"cf/@cf/black-forest-labs/flux-2-klein-9b","prompt":"neon skyline"}`
		before := time.Now().UTC()
		req1 := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(reqBody))
		req1.Header.Set("Content-Type", "application/json")
		rec1 := httptest.NewRecorder()
		handler.HandleImages(rec1, req1)
		after := time.Now().UTC()

		if rec1.Code != http.StatusOK {
			t.Fatalf("first request status = %d, want 200: %s", rec1.Code, rec1.Body.String())
		}
		if acct1Hits.Load() != 1 || acct2Hits.Load() != 1 {
			t.Fatalf("hits after req1 = (acct1:%d, acct2:%d), want (1, 1)", acct1Hits.Load(), acct2Hits.Load())
		}
		if gotAuth2 != "Bearer tok-2" {
			t.Errorf("acct-2 Authorization = %q, want Bearer tok-2", gotAuth2)
		}
		if gotPath2 != "/accounts/acct-2/ai/run/@cf/black-forest-labs/flux-2-klein-9b" {
			t.Errorf("acct-2 path = %q", gotPath2)
		}
		if gotPrompt2 != "neon skyline" {
			t.Errorf("acct-2 prompt = %q, want neon skyline", gotPrompt2)
		}

		until1, ok1 := blockedUntilForConn(t, repo, "conn-cf-1")
		if !ok1 {
			t.Fatalf("conn-cf-1 should have persisted cooldown")
		}
		m1, m2 := nextUTCMidnightBounds(before, after)
		if !until1.Equal(m1) && !until1.Equal(m2) {
			t.Errorf("conn-cf-1 blockedUntil = %s, want UTC midnight (%s or %s)", until1.Format(time.RFC3339), m1.Format(time.RFC3339), m2.Format(time.RFC3339))
		}
		if until2, ok2 := blockedUntilForConn(t, repo, "conn-cf-2"); ok2 {
			t.Errorf("conn-cf-2 should not be cooled, got %s", until2)
		}

		req2 := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(reqBody))
		req2.Header.Set("Content-Type", "application/json")
		rec2 := httptest.NewRecorder()
		handler.HandleImages(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("second request status = %d, want 200: %s", rec2.Code, rec2.Body.String())
		}
		if acct1Hits.Load() != 1 || acct2Hits.Load() != 2 {
			t.Errorf("hits after req2 = (acct1:%d, acct2:%d), want (1, 2) so cooled conn-cf-1 is skipped", acct1Hits.Load(), acct2Hits.Load())
		}
	})

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "429_rate_limit", status: http.StatusTooManyRequests, body: `{"errors":[{"code":10000,"message":"rate limited"}],"success":false}`},
		{name: "400_code_7003", status: http.StatusBadRequest, body: `{"errors":[{"code":7003,"message":"Could not route to endpoint"}],"success":false}`},
	} {
		t.Run("rotates_on_"+tc.name+"_with_5m_cooldown", func(t *testing.T) {
			var acct1Hits, acct2Hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/accounts/acct-1/"):
					acct1Hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				case strings.HasPrefix(r.URL.Path, "/accounts/acct-2/"):
					acct2Hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"result":{"image":"T0s="},"success":true,"errors":[]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()

			database, cleanup := setupMultimodalTestDB(t)
			defer cleanup()
			repo := db.NewRepo(database)
			seedCloudflareConnWith(t, database, "conn-cf-1", "tok-1", upstream.URL+"/accounts/acct-1", 1)
			seedCloudflareConnWith(t, database, "conn-cf-2", "tok-2", upstream.URL+"/accounts/acct-2", 2)
			handler := newTestMediaHandler(repo)

			before := time.Now().UTC()
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"cf/@cf/black-forest-labs/flux-1-schnell","prompt":"clouds"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.HandleImages(rec, req)
			after := time.Now().UTC()

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if acct1Hits.Load() != 1 || acct2Hits.Load() != 1 {
				t.Fatalf("hits = (%d, %d), want (1, 1)", acct1Hits.Load(), acct2Hits.Load())
			}
			until, ok := blockedUntilForConn(t, repo, "conn-cf-1")
			if !ok {
				t.Fatalf("conn-cf-1 should have 5m cooldown")
			}
			if !until.After(before.Add(4*time.Minute)) || !until.Before(after.Add(6*time.Minute)) {
				t.Errorf("cooldown = %s, want ~5m from %s", until, before)
			}
		})
	}

	t.Run("non_retryable_400_passes_through_without_rotation_or_cooldown", func(t *testing.T) {
		const clientErr = `{"errors":[{"code":5006,"message":"invalid prompt"}],"success":false}`
		var acct1Hits, acct2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-1/"):
				acct1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(clientErr))
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-2/"):
				acct2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedCloudflareConnWith(t, database, "conn-cf-1", "tok-1", upstream.URL+"/accounts/acct-1", 1)
		seedCloudflareConnWith(t, database, "conn-cf-2", "tok-2", upstream.URL+"/accounts/acct-2", 2)
		handler := newTestMediaHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"cf/@cf/black-forest-labs/flux-1-schnell","prompt":"bad"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.HandleImages(rec, req)

		if rec.Code != http.StatusBadRequest || rec.Body.String() != clientErr {
			t.Fatalf("response = (%d, %s), want (400, %s)", rec.Code, rec.Body.String(), clientErr)
		}
		if acct1Hits.Load() != 1 || acct2Hits.Load() != 0 {
			t.Errorf("hits = (%d, %d), want (1, 0)", acct1Hits.Load(), acct2Hits.Load())
		}
		if until, ok := blockedUntilForConn(t, repo, "conn-cf-1"); ok {
			t.Errorf("non-retryable 400 should not lock connection, got %s", until)
		}
	})

	t.Run("pinned_connection_surfaces_4006_without_rotating", func(t *testing.T) {
		var acct1Hits, acct2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-1/"):
				acct1Hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(quotaBody))
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-2/"):
				acct2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedCloudflareConnWith(t, database, "conn-cf-1", "tok-1", upstream.URL+"/accounts/acct-1", 1)
		seedCloudflareConnWith(t, database, "conn-cf-2", "tok-2", upstream.URL+"/accounts/acct-2", 2)
		handler := newTestMediaHandler(repo)

		before := time.Now().UTC()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"cf/@cf/black-forest-labs/flux-1-schnell","prompt":"pinned"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-connection-id", "conn-cf-1")
		rec := httptest.NewRecorder()
		handler.HandleImages(rec, req)
		after := time.Now().UTC()

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "4006") {
			t.Fatalf("response = (%d, %s), want 400 containing 4006", rec.Code, rec.Body.String())
		}
		if acct1Hits.Load() != 1 || acct2Hits.Load() != 0 {
			t.Errorf("hits = (%d, %d), want (1, 0) for pinned connection", acct1Hits.Load(), acct2Hits.Load())
		}
		until, ok := blockedUntilForConn(t, repo, "conn-cf-1")
		if !ok {
			t.Fatalf("pinned 4006 should still persist cooldown")
		}
		m1, m2 := nextUTCMidnightBounds(before, after)
		if !until.Equal(m1) && !until.Equal(m2) {
			t.Errorf("pinned blockedUntil = %s, want UTC midnight", until)
		}
	})

	t.Run("all_accounts_exhausted_returns_last_quota_refusal", func(t *testing.T) {
		var acct1Hits, acct2Hits atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-1/"):
				acct1Hits.Add(1)
			case strings.HasPrefix(r.URL.Path, "/accounts/acct-2/"):
				acct2Hits.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(quotaBody))
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedCloudflareConnWith(t, database, "conn-cf-1", "tok-1", upstream.URL+"/accounts/acct-1", 1)
		seedCloudflareConnWith(t, database, "conn-cf-2", "tok-2", upstream.URL+"/accounts/acct-2", 2)
		handler := newTestMediaHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"cf/@cf/black-forest-labs/flux-1-schnell","prompt":"exhausted"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.HandleImages(rec, req)

		if rec.Code != http.StatusBadRequest || rec.Body.String() != quotaBody {
			t.Fatalf("response = (%d, %s), want (400, %s)", rec.Code, rec.Body.String(), quotaBody)
		}
		if acct1Hits.Load() != 1 || acct2Hits.Load() != 1 {
			t.Errorf("hits = (%d, %d), want (1, 1)", acct1Hits.Load(), acct2Hits.Load())
		}
		for _, id := range []string{"conn-cf-1", "conn-cf-2"} {
			if _, ok := blockedUntilForConn(t, repo, id); !ok {
				t.Errorf("%s should be cooled when exhausted", id)
			}
		}
	})

	t.Run("probe_context_does_not_persist_cooldown", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasPrefix(r.URL.Path, "/accounts/acct-1/") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(quotaBody))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{"image":"UFJPQkU="},"success":true,"errors":[]}`))
		}))
		defer upstream.Close()

		database, cleanup := setupMultimodalTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)
		seedCloudflareConnWith(t, database, "conn-cf-1", "tok-1", upstream.URL+"/accounts/acct-1", 1)
		seedCloudflareConnWith(t, database, "conn-cf-2", "tok-2", upstream.URL+"/accounts/acct-2", 2)
		handler := newTestMediaHandler(repo)

		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"cf/@cf/black-forest-labs/flux-1-schnell","prompt":"probe"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(handlerutil.WithProbeContext(req.Context()))
		rec := httptest.NewRecorder()
		handler.HandleImages(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("probe status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if until, ok := blockedUntilForConn(t, repo, "conn-cf-1"); ok {
			t.Errorf("probe context must not persist cooldown, got %s", until)
		}
	})
}

func TestClassifyCloudflareFailure(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantRetryable bool
		wantQuota     bool
		wantMsg       string
	}{
		{
			name:          "400 with code 4006 is retryable quota",
			status:        http.StatusBadRequest,
			body:          `{"errors":[{"code":4006,"message":"you have used up your daily free allocation"}],"success":false}`,
			wantRetryable: true,
			wantQuota:     true,
			wantMsg:       "you have used up your daily free allocation",
		},
		{
			name:          "400 with code 7003 is retryable non-quota",
			status:        http.StatusBadRequest,
			body:          `{"errors":[{"code":7003,"message":"Could not route to endpoint"}]}`,
			wantRetryable: true,
			wantQuota:     false,
			wantMsg:       "Could not route to endpoint",
		},
		{
			name:          "429/401/403 are retryable non-quota",
			status:        http.StatusTooManyRequests,
			body:          `{"errors":[{"code":10000,"message":"rate limit"}]}`,
			wantRetryable: true,
			wantQuota:     false,
			wantMsg:       "rate limit",
		},
		{
			name:          "401 is retryable",
			status:        http.StatusUnauthorized,
			body:          `unauthorized`,
			wantRetryable: true,
			wantQuota:     false,
			wantMsg:       "unauthorized",
		},
		{
			name:          "403 is retryable",
			status:        http.StatusForbidden,
			body:          `forbidden`,
			wantRetryable: true,
			wantQuota:     false,
			wantMsg:       "forbidden",
		},
		{
			name:          "400 unknown code is not retryable",
			status:        http.StatusBadRequest,
			body:          `{"errors":[{"code":5006,"message":"invalid prompt"}]}`,
			wantRetryable: false,
			wantQuota:     false,
			wantMsg:       "invalid prompt",
		},
		{
			name:          "404 is not retryable",
			status:        http.StatusNotFound,
			body:          `not found`,
			wantRetryable: false,
			wantQuota:     false,
			wantMsg:       "not found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCloudflareFailure(tc.status, []byte(tc.body))
			if got.retryable != tc.wantRetryable || got.quota != tc.wantQuota || got.message != tc.wantMsg {
				t.Errorf("got %+v, want retryable=%v quota=%v msg=%q", got, tc.wantRetryable, tc.wantQuota, tc.wantMsg)
			}
		})
	}
}

func TestCloudflareCooldownUntil(t *testing.T) {
	now := time.Date(2026, 11, 22, 15, 30, 45, 0, time.FixedZone("WIB", 7*3600))
	if got, want := cloudflareCooldownUntil(now, true), time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("quota cooldown = %s, want %s", got, want)
	}
	if got, want := cloudflareCooldownUntil(now, false), time.Date(2026, 11, 22, 8, 35, 45, 0, time.UTC); !got.Equal(want) {
		t.Errorf("non-quota cooldown = %s, want %s", got, want)
	}
}
