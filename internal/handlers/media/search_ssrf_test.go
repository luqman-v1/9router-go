package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestResolveSearchURL_RejectsPrivateOverride(t *testing.T) {
	tests := []struct {
		name    string
		psd     map[string]any
		want    string
		wantErr string
	}{
		{
			name:    "loopback ipv4",
			psd:     map[string]any{"baseUrl": "http://127.0.0.1:8080/search"},
			wantErr: "search base URL is not allowed",
		},
		{
			name:    "loopback hostname",
			psd:     map[string]any{"baseUrl": "http://localhost/search"},
			wantErr: "search base URL is not allowed",
		},
		{
			name:    "private rfc1918",
			psd:     map[string]any{"baseUrl": "http://192.168.1.10/search"},
			wantErr: "search base URL is not allowed",
		},
		{
			name:    "link-local",
			psd:     map[string]any{"baseUrl": "http://169.254.169.254/search"},
			wantErr: "search base URL is not allowed",
		},
		{
			name:    "cloud metadata",
			psd:     map[string]any{"baseUrl": "http://169.254.169.254/latest/meta-data/"},
			wantErr: "search base URL is not allowed",
		},
		{
			name:    "invalid scheme",
			psd:     map[string]any{"baseUrl": "ftp://example.com/search"},
			wantErr: "invalid search base URL",
		},
		{
			name: "empty baseUrl falls back to registry default",
			psd:  map[string]any{"baseUrl": ""},
			want: "http://localhost:4000/search",
		},
		{
			name: "public https allowed",
			psd:  map[string]any{"baseUrl": "https://searx.example.com/search"},
			want: "https://searx.example.com/search",
		},
		{
			name: "public http allowed",
			psd:  map[string]any{"baseUrl": "http://searx.example.com/search"},
			want: "http://searx.example.com/search",
		},
	}

	cfg := &providers.ProviderConfig{
		BaseURL:   "http://localhost:4000",
		SearchURL: "http://localhost:4000/search",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSearchURL(cfg, tt.psd, cfg.SearchURL)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveSearchURL() expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("resolveSearchURL() error = %q, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSearchURL() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveSearchURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveSearchURL_NilPsd_FallsBackToRegistry(t *testing.T) {
	cfg := &providers.ProviderConfig{
		BaseURL:   "http://localhost:4000",
		SearchURL: "http://localhost:4000/search",
	}
	got, err := resolveSearchURL(cfg, nil, cfg.SearchURL)
	if err != nil {
		t.Fatalf("resolveSearchURL(nil) unexpected error: %v", err)
	}
	if got != cfg.SearchURL {
		t.Errorf("resolveSearchURL(nil) = %q, want %q", got, cfg.SearchURL)
	}
}

func TestResolveSearchURL_PublicHostnameAllowed(t *testing.T) {
	cfg := &providers.ProviderConfig{
		SearchURL: "http://localhost:4000/search",
	}
	got, err := resolveSearchURL(cfg, map[string]any{"baseUrl": "https://public.searxng.example/search"}, cfg.SearchURL)
	if err != nil {
		t.Fatalf("public hostname unexpectedly rejected: %v", err)
	}
	if got != "https://public.searxng.example/search" {
		t.Errorf("got %q, want %q", got, "https://public.searxng.example/search")
	}
}

// A search endpoint that answers 302 with a Location on a private address must
// not be followed: that hop is the bypass the resolveSearchURL pre-flight
// cannot see, because the host it validated is public and only the redirect
// lands inside. The target is a second loopback server this test owns, so a
// refused request leaves it with zero hits — a dial error would otherwise read
// the same as a refusal.
func TestResolveSearchURL_RedirectToPrivateRefused(t *testing.T) {
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHits.Add(1)
		w.Write([]byte(`{"results":[{"title":"internal admin"}]}`))
	}))
	defer private.Close()

	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL+"/admin", http.StatusFound)
	}))
	defer search.Close()

	orig := providers.KnownProviders["tavily"]
	tavily := orig
	tavily.SearchURL = search.URL
	providers.KnownProviders["tavily"] = tavily
	defer func() { providers.KnownProviders["tavily"] = orig }()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	connData, _ := json.Marshal(map[string]any{"apiKey": "tvly-test-key"})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-tavily', 'tavily', 'apikey', 'Tavily Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := newTestMediaHandler(db.NewRepo(database))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(`{"model":"tavily/search","query":"internal admin"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleSearch(rec, req)

	if hits := privateHits.Load(); hits != 0 {
		t.Fatalf("redirect to private address was followed %d time(s); body: %s", hits, rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("expected the redirect to be refused, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The combo branch of forwardMediaRequest dials each member through the shared
// chat client, so the redirect guard must ride on a copy of it there too. This
// test drives a search-combo whose tavily/search member answers 302 with a
// Location on a loopback server this test owns: without the guard the private
// server would receive the request, so zero hits is the only acceptable result.
func TestResolveSearchURL_ComboRedirectToPrivateRefused(t *testing.T) {
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHits.Add(1)
		w.Write([]byte(`{"results":[{"title":"internal admin"}]}`))
	}))
	defer private.Close()

	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL+"/admin", http.StatusFound)
	}))
	defer search.Close()

	orig := providers.KnownProviders["tavily"]
	tavily := orig
	tavily.SearchURL = search.URL
	providers.KnownProviders["tavily"] = tavily
	defer func() { providers.KnownProviders["tavily"] = orig }()

	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	connData, _ := json.Marshal(map[string]any{"apiKey": "tvly-test-key"})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-tavily', 'tavily', 'apikey', 'Tavily Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS combos (
		id TEXT PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		kind TEXT,
		models TEXT NOT NULL,
		strategy TEXT DEFAULT 'fallback',
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL,
		matchRules TEXT DEFAULT '',
		customPrompt TEXT DEFAULT ''
	)`); err != nil {
		t.Fatalf("create combos table: %v", err)
	}

	repo := db.NewRepo(database)
	if err := repo.CreateCombo("search-combo", "search-combo", "webSearch", `["tavily/search"]`, "fallback", ""); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	handler := newTestMediaHandler(repo)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(`{"model":"search-combo","query":"internal admin"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.HandleSearch(rec, req)

	if hits := privateHits.Load(); hits != 0 {
		t.Fatalf("combo redirect to private address was followed %d time(s); body: %s", hits, rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway from combo search, got %d: %s", rec.Code, rec.Body.String())
	}
}
