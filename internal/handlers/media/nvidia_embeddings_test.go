package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestHandleEmbeddings_NvidiaInputType(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		body    string
		wantHas bool
		wantVal string
	}{
		{
			name:    "query preserved",
			model:   "nvidia/nemotron-3.5-embed-8b",
			body:    `{"model":"MODEL","input":"hello","input_type":"query"}`,
			wantHas: true,
			wantVal: "query",
		},
		{
			name:    "passage preserved",
			model:   "nvidia/nemotron-3.5-embed-8b",
			body:    `{"model":"MODEL","input":"hello","input_type":"passage"}`,
			wantHas: true,
			wantVal: "passage",
		},
		{
			name:    "missing defaults to query",
			model:   "nvidia/nemotron-3.5-embed-8b",
			body:    `{"model":"MODEL","input":"hello"}`,
			wantHas: true,
			wantVal: "query",
		},
		{
			name:    "empty defaults to query",
			model:   "nvidia/nemotron-3.5-embed-8b",
			body:    `{"model":"MODEL","input":"hello","input_type":""}`,
			wantHas: true,
			wantVal: "query",
		},
		{
			name:    "invalid defaults to query",
			model:   "nvidia/nemotron-3.5-embed-8b",
			body:    `{"model":"MODEL","input":"hello","input_type":"bogus"}`,
			wantHas: true,
			wantVal: "query",
		},
		{
			name:    "symmetric nvclip no input_type",
			model:   "nvidia/nvclip",
			body:    `{"model":"MODEL","input":"hello"}`,
			wantHas: false,
		},
		{
			name:    "non-embedding parakeet no input_type",
			model:   "nvidia/parakeet-ctc-1.1b-asr",
			body:    `{"model":"MODEL","input":"hello"}`,
			wantHas: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var captured map[string]any

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				_ = json.Unmarshal(b, &captured)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`))
			}))
			defer upstream.Close()

			origCfg := providers.KnownProviders["nvidia"]
			providers.KnownProviders["nvidia"] = providers.ProviderConfig{
				BaseURL:    upstream.URL + "/v1/chat/completions",
				AuthHeader: "Authorization",
				AuthScheme: "bearer",
			}
			t.Cleanup(func() { providers.KnownProviders["nvidia"] = origCfg })

			database, cleanup := setupEmbeddingsTestDB(t)
			defer cleanup()

			connData, _ := json.Marshal(map[string]any{"apiKey": "nv-test-key", "baseUrl": upstream.URL})
			if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-nv-emb', 'nvidia', 'apikey', 'Nvidia Emb Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
				t.Fatalf("seed connection: %v", err)
			}

			repo := db.NewRepo(database)
			handler := newTestMediaHandler(repo)

			body := strings.Replace(tc.body, "MODEL", tc.model, 1)
			req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.HandleEmbeddings(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			mu.Lock()
			defer mu.Unlock()
			_, has := captured["input_type"]
			if has != tc.wantHas {
				t.Fatalf("input_type presence = %v, want %v (body=%v)", has, tc.wantHas, captured)
			}
			if tc.wantHas {
				got, _ := captured["input_type"].(string)
				if got != tc.wantVal {
					t.Errorf("input_type = %q, want %q", got, tc.wantVal)
				}
			}
		})
	}
}

// Direct unit-level pin of forwardNvidiaEmbeddingInputType.
func TestForwardNvidiaEmbeddingInputType_Unit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"query", `{"input":"x","input_type":"query"}`, "query"},
		{"passage", `{"input":"x","input_type":"passage"}`, "passage"},
		{"missing", `{"input":"x"}`, "query"},
		{"empty", `{"input":"x","input_type":""}`, "query"},
		{"invalid", `{"input":"x","input_type":"bogus"}`, "query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := forwardNvidiaEmbeddingInputType([]byte(tc.in))
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, _ := m["input_type"].(string)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
