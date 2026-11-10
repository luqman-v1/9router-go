package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/constants"
)

// The catalogue fetch and the connection test both leave User-Agent unset, and
// an absent one goes out as Go's default identity — which Cloudflare-fronted
// providers (Nous Research) refuse with HTTP 403 "Attention Required!". These
// tests exercise the real request construction, so they fail if the default is
// dropped from either outbound path.
func TestProbePathsSendNonDefaultUserAgent(t *testing.T) {
	tests := []struct {
		name string
		do   func(url string) (int, []byte, error)
	}{
		{
			name: "validateProbeDo (catalogue + provider probes)",
			do: func(url string) (int, []byte, error) {
				return validateProbeDo(context.Background(), http.MethodGet, url,
					map[string]string{"Authorization": "Bearer k"}, nil)
			},
		},
		{
			name: "connectionProbeDo (connection test)",
			do: func(url string) (int, []byte, error) {
				return connectionProbeDo(context.Background(), nil, http.MethodGet, url,
					map[string]string{"Authorization": "Bearer k"}, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUA = r.Header.Get("User-Agent")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer srv.Close()

			status, _, err := tt.do(srv.URL)
			if err != nil {
				t.Fatalf("probe failed: %v", err)
			}
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if gotUA != constants.DefaultClientUserAgent {
				t.Errorf("outbound User-Agent = %q, want %q", gotUA, constants.DefaultClientUserAgent)
			}
		})
	}
}

// A provider-specific identity must survive the default, otherwise the probes
// that Google and xAI validate by User-Agent would start failing.
func TestValidateProbeDoKeepsCallerUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const want = "antigravity/cli/1.5.1"
	if _, _, err := validateProbeDo(context.Background(), http.MethodGet, srv.URL,
		map[string]string{"User-Agent": want}, nil); err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if gotUA != want {
		t.Errorf("outbound User-Agent = %q, want the caller's %q", gotUA, want)
	}
}
