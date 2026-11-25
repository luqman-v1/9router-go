package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// TestForwardOpenAI_defaultHeadersMatchNode pins the Node (undici) outbound
// header shape: default User-Agent "node", Accept "*/*", operator
// StaticHeaders/Custom Headers always winning, and streaming requests pinned
// to identity so no edge can answer with a buffered gzip event stream.
func TestForwardOpenAI_defaultHeadersMatchNode(t *testing.T) {
	tests := []struct {
		name         string
		static       map[string]string
		overrides    map[string]string
		isStream     bool
		wantAgent    string
		wantAccept   string
		wantEncoding string
	}{
		{
			name:         "defaults when unset",
			wantAgent:    "node",
			wantAccept:   "*/*",
			wantEncoding: "gzip", // Go's transport default; non-streaming is left alone
		},
		{
			name:         "operator values win",
			static:       map[string]string{"User-Agent": "custom-ua", "Accept": "application/json"},
			wantAgent:    "custom-ua",
			wantAccept:   "application/json",
			wantEncoding: "gzip",
		},
		{
			name:         "lowercase operator values win",
			static:       map[string]string{"user-agent": "custom-ua", "accept": "application/json"},
			wantAgent:    "custom-ua",
			wantAccept:   "application/json",
			wantEncoding: "gzip",
		},
		{
			name:         "streaming asks for event-stream",
			isStream:     true,
			wantAgent:    "node",
			wantAccept:   "text/event-stream",
			wantEncoding: "identity",
		},
		{
			name:         "streaming keeps an operator encoding",
			isStream:     true,
			static:       map[string]string{"Accept-Encoding": "gzip"},
			wantAgent:    "node",
			wantAccept:   "text/event-stream",
			wantEncoding: "gzip",
		},
		{
			name:         "streaming keeps a lowercase operator encoding",
			isStream:     true,
			static:       map[string]string{"accept-encoding": "br"},
			wantAgent:    "node",
			wantAccept:   "text/event-stream",
			wantEncoding: "br",
		},
		{
			name:         "streaming keeps a Custom Header encoding",
			isStream:     true,
			overrides:    map[string]string{"Accept-Encoding": "gzip"},
			wantAgent:    "node",
			wantAccept:   "text/event-stream",
			wantEncoding: "gzip",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAgent, gotAccept, gotEncoding string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAgent = r.Header.Get("User-Agent")
				gotAccept = r.Header.Get("Accept")
				gotEncoding = r.Header.Get("Accept-Encoding")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			cfg := &providers.ProviderConfig{
				BaseURL:         srv.URL,
				AuthHeader:      "Authorization",
				AuthScheme:      "bearer",
				StaticHeaders:   tt.static,
				HeaderOverrides: tt.overrides,
			}
			resp, err := ForwardOpenAI(t.Context(), srv.Client(), cfg, "sk-test", []byte(`{}`), tt.isStream)
			if err != nil {
				t.Fatalf("ForwardOpenAI() error = %v", err)
			}
			resp.Body.Close()

			if gotAgent != tt.wantAgent {
				t.Errorf("User-Agent = %q, want %q", gotAgent, tt.wantAgent)
			}
			if gotAccept != tt.wantAccept {
				t.Errorf("Accept = %q, want %q", gotAccept, tt.wantAccept)
			}
			if gotEncoding != tt.wantEncoding {
				t.Errorf("Accept-Encoding = %q, want %q", gotEncoding, tt.wantEncoding)
			}
		})
	}
}
