package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func setupProxyPoolValidationRouter(t *testing.T) (*db.Repo, http.Handler, func()) {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	if _, err := repo.RawDB().Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		cleanup()
		t.Fatalf("create proxyPools table: %v", err)
	}
	return repo, setupTestRouter(repo), cleanup
}

func sendProxyPoolRawJSON(t *testing.T, router http.Handler, method, path, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeValidationErrorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body %q: %v", rec.Body.String(), err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object in response %s", rec.Body.String())
	}
	return errObj
}

func TestHandleCreateProxyPool_ValidationErrors(t *testing.T) {
	_, router, cleanup := setupProxyPoolValidationRouter(t)
	defer cleanup()

	tests := []struct {
		name               string
		body               string
		wantField          string
		wantReasonContains []string
	}{
		{
			name:               "empty body",
			body:               "   ",
			wantField:          "",
			wantReasonContains: []string{"empty", "JSON object"},
		},
		{
			name:               "malformed JSON",
			body:               `{"name":`,
			wantField:          "name",
			wantReasonContains: []string{"malformed JSON", "byte"},
		},
		{
			name:               "malformed JSON root syntax",
			body:               `{invalid`,
			wantField:          "",
			wantReasonContains: []string{"malformed JSON", "byte"},
		},
		{
			name:               "non-object JSON array",
			body:               `["socks5://127.0.0.1:40000"]`,
			wantField:          "",
			wantReasonContains: []string{"JSON object", "array"},
		},
		{
			name:               "non-object JSON string",
			body:               `"hello"`,
			wantField:          "",
			wantReasonContains: []string{"JSON object", "string"},
		},
		{
			name:               "non-object JSON null",
			body:               `null`,
			wantField:          "",
			wantReasonContains: []string{"JSON object", "null"},
		},
		{
			name:               "wrong type isActive number (#1024)",
			body:               `{"name":"warp","proxyUrl":"socks5://127.0.0.1:40000","isActive":1}`,
			wantField:          "isActive",
			wantReasonContains: []string{"boolean", "number"},
		},
		{
			name:               "wrong type strictProxy string",
			body:               `{"name":"warp","proxyUrl":"socks5://127.0.0.1:40000","strictProxy":"true"}`,
			wantField:          "strictProxy",
			wantReasonContains: []string{"boolean", "string"},
		},
		{
			name:               "wrong type name number",
			body:               `{"name":42,"proxyUrl":"http://127.0.0.1:8080"}`,
			wantField:          "name",
			wantReasonContains: []string{"string", "number"},
		},
		{
			name:               "wrong type noProxy array",
			body:               `{"proxyUrl":"http://127.0.0.1:8080","noProxy":["localhost"]}`,
			wantField:          "noProxy",
			wantReasonContains: []string{"string", "array"},
		},
		{
			name:               "wrong type urls string",
			body:               `{"urls":"socks5://127.0.0.1:40000"}`,
			wantField:          "urls",
			wantReasonContains: []string{"array", "string"},
		},
		{
			name:               "wrong type urls element number",
			body:               `{"urls":["socks5://127.0.0.1:40000",123]}`,
			wantField:          "urls[1]",
			wantReasonContains: []string{"string", "number"},
		},
		{
			name:               "missing proxyUrl",
			body:               `{"name":"warp"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"is required"},
		},
		{
			name:               "invalid URL missing scheme",
			body:               `{"name":"warp","proxyUrl":"127.0.0.1:40000"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"scheme and host"},
		},
		{
			name:               "invalid URL missing host",
			body:               `{"name":"warp","proxyUrl":"http://:8080"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"scheme and host"},
		},
		{
			name:               "unsupported scheme socks4",
			body:               `{"proxyUrl":"socks4://127.0.0.1:1080"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"unsupported proxy scheme", "socks4"},
		},
		{
			name:               "unsupported scheme quic",
			body:               `{"proxyUrl":"quic://127.0.0.1:40000"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"unsupported proxy scheme", "quic"},
		},
		{
			name:               "invalid pool type masque",
			body:               `{"proxyUrl":"socks5://127.0.0.1:40000","type":"masque"}`,
			wantField:          "type",
			wantReasonContains: []string{"http, socks5, vercel, cloudflare, deno, netlify"},
		},
		{
			name:               "scheme/type mismatch socks5 with http url",
			body:               `{"proxyUrl":"http://127.0.0.1:8080","type":"socks5"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"socks5", "http"},
		},
		{
			name:               "scheme/type mismatch vercel with socks5 url",
			body:               `{"proxyUrl":"socks5://127.0.0.1:40000","type":"vercel"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"relay type", "http or https", "socks5"},
		},
		{
			name:               "multiple urls not supported",
			body:               `{"urls":["http://127.0.0.1:8001","http://127.0.0.1:8002"]}`,
			wantField:          "urls",
			wantReasonContains: []string{"single proxy URL"},
		},
		{
			name:               "duplicate JSON keys",
			body:               `{"proxyUrl":"http://127.0.0.1:8001","proxyUrl":"http://127.0.0.1:8002"}`,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"duplicate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := sendProxyPoolRawJSON(t, router, http.MethodPost, "/api/proxy-pools", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
			errObj := decodeValidationErrorBody(t, rec)
			if errObj["code"] != "bad_request" || errObj["type"] != "invalid_request_error" {
				t.Errorf("unexpected error envelope code/type: %v", errObj)
			}

			gotField, hasField := errObj["field"].(string)
			if tt.wantField == "" {
				if hasField && gotField != "" {
					t.Errorf("expected no field key, got %q (body: %s)", gotField, rec.Body.String())
				}
			} else if gotField != tt.wantField {
				t.Errorf("field = %q, want %q (body: %s)", gotField, tt.wantField, rec.Body.String())
			}

			reason, _ := errObj["reason"].(string)
			if reason == "" {
				t.Fatalf("expected non-empty reason in %v", errObj)
			}
			for _, sub := range tt.wantReasonContains {
				if !strings.Contains(reason, sub) {
					t.Errorf("reason %q does not contain %q", reason, sub)
				}
			}
			msg, _ := errObj["message"].(string)
			if !strings.Contains(msg, reason) || (tt.wantField != "" && !strings.Contains(msg, tt.wantField)) {
				t.Errorf("message %q missing field=%q or reason=%q", msg, tt.wantField, reason)
			}
		})
	}
}

func TestHandleCreateProxyPool_ValidPayloads(t *testing.T) {
	repo, router, cleanup := setupProxyPoolValidationRouter(t)
	defer cleanup()

	t.Run("MASQUE SOCKS5 infers socks5 type", func(t *testing.T) {
		rec := sendProxyPoolRawJSON(t, router, http.MethodPost, "/api/proxy-pools",
			`{"name":"  warp-masque  ","proxyUrl":"  socks5://127.0.0.1:40000  ","strictProxy":true,"isActive":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var pool map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &pool); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if pool["name"] != "warp-masque" || pool["proxyUrl"] != "socks5://127.0.0.1:40000" {
			t.Errorf("unexpected name/proxyUrl: %v", pool)
		}
		if pool["type"] != "socks5" || pool["strictProxy"] != true || pool["isActive"] != true {
			t.Errorf("unexpected type/strictProxy/isActive: %v", pool)
		}
	})

	t.Run("socks5h IPv6 with omitted name defaults to Proxy Pool and socks5", func(t *testing.T) {
		rec := sendProxyPoolRawJSON(t, router, http.MethodPost, "/api/proxy-pools",
			`{"proxyUrl":"socks5h://[::1]:40000"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var pool map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &pool); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if pool["name"] != "Proxy Pool" || pool["type"] != "socks5" || pool["isActive"] != true {
			t.Errorf("unexpected defaults: %v", pool)
		}
	})

	t.Run("explicit isActive false persists in DB", func(t *testing.T) {
		rec := sendProxyPoolRawJSON(t, router, http.MethodPost, "/api/proxy-pools",
			`{"name":"disabled-pool","proxyUrl":"http://127.0.0.1:8080","isActive":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var pool map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &pool); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if pool["isActive"] != false {
			t.Fatalf("response isActive = %v, want false", pool["isActive"])
		}
		stored, err := repo.GetProxyPool(pool["id"].(string))
		if err != nil {
			t.Fatalf("GetProxyPool: %v", err)
		}
		if stored.IsActive {
			t.Errorf("stored IsActive = true, want false")
		}
	})

	t.Run("single-element urls promotes to proxyUrl", func(t *testing.T) {
		rec := sendProxyPoolRawJSON(t, router, http.MethodPost, "/api/proxy-pools",
			`{"name":"from-urls","urls":["socks5://127.0.0.1:40000"]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var pool map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &pool); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if pool["proxyUrl"] != "socks5://127.0.0.1:40000" || pool["type"] != "socks5" {
			t.Errorf("unexpected pool: %v", pool)
		}
	})

	t.Run("extra unknown fields tolerated for round-tripping", func(t *testing.T) {
		rec := sendProxyPoolRawJSON(t, router, http.MethodPost, "/api/proxy-pools",
			`{"name":"round-trip","proxyUrl":"http://127.0.0.1:8888","createdAt":"2026-01-01T00:00:00Z","boundConnectionCount":3}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("relay pool vercel accepted", func(t *testing.T) {
		rec := sendProxyPoolRawJSON(t, router, http.MethodPost, "/api/proxy-pools",
			`{"name":"Vercel Edge","proxyUrl":"https://relay.example.com","type":"vercel"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleUpdateProxyPool_ValidationAndNotFound(t *testing.T) {
	repo, router, cleanup := setupProxyPoolValidationRouter(t)
	defer cleanup()

	created, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "Original",
		ProxyURL: "http://127.0.0.1:8080",
		Type:     "http",
	})
	if err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	poolID := created["id"].(string)

	tests := []struct {
		name               string
		id                 string
		body               string
		wantCode           int
		wantField          string
		wantReasonContains []string
	}{
		{
			name:               "wrong type isActive number",
			id:                 poolID,
			body:               `{"isActive":1}`,
			wantCode:           http.StatusBadRequest,
			wantField:          "isActive",
			wantReasonContains: []string{"boolean", "number"},
		},
		{
			name:               "empty name",
			id:                 poolID,
			body:               `{"name":"   "}`,
			wantCode:           http.StatusBadRequest,
			wantField:          "name",
			wantReasonContains: []string{"must not be empty"},
		},
		{
			name:               "empty proxyUrl",
			id:                 poolID,
			body:               `{"proxyUrl":""}`,
			wantCode:           http.StatusBadRequest,
			wantField:          "proxyUrl",
			wantReasonContains: []string{"must not be empty"},
		},
		{
			name:               "invalid type",
			id:                 poolID,
			body:               `{"type":"invalid"}`,
			wantCode:           http.StatusBadRequest,
			wantField:          "type",
			wantReasonContains: []string{"http, socks5, vercel, cloudflare, deno, netlify"},
		},
		{
			name:               "incompatible type switch",
			id:                 poolID,
			body:               `{"type":"socks5"}`,
			wantCode:           http.StatusBadRequest,
			wantField:          "type",
			wantReasonContains: []string{"socks5", "http"},
		},
		{
			name:     "non-existent pool id returns 404",
			id:       "does-not-exist",
			body:     `{"isActive":false}`,
			wantCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := sendProxyPoolRawJSON(t, router, http.MethodPut, "/api/proxy-pools/"+tt.id, tt.body)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantCode != http.StatusBadRequest {
				return
			}
			errObj := decodeValidationErrorBody(t, rec)
			if got, _ := errObj["field"].(string); got != tt.wantField {
				t.Errorf("field = %q, want %q (body: %s)", got, tt.wantField, rec.Body.String())
			}
			reason, _ := errObj["reason"].(string)
			for _, sub := range tt.wantReasonContains {
				if !strings.Contains(reason, sub) {
					t.Errorf("reason %q missing %q", reason, sub)
				}
			}
		})
	}

	t.Run("valid partial update persists", func(t *testing.T) {
		rec := sendProxyPoolRawJSON(t, router, http.MethodPut, "/api/proxy-pools/"+poolID, `{"isActive":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		got, err := repo.GetProxyPool(poolID)
		if err != nil {
			t.Fatalf("GetProxyPool: %v", err)
		}
		if got.IsActive {
			t.Errorf("expected IsActive=false after PUT, got true")
		}
		if got.Name != "Original" || len(got.URLs) != 1 || got.URLs[0] != "http://127.0.0.1:8080" {
			t.Errorf("partial update unexpectedly altered existing fields: %+v", got)
		}
	})
}
