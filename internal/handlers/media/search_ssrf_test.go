package media

import (
	"strings"
	"testing"

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
