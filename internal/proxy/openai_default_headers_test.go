package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// TestForwardOpenAI_defaultHeadersMatchNode pins the Node (undici) outbound
// header shape: default User-Agent "node", Accept "*/*", and operator
// StaticHeaders always winning, even when written lowercase.
func TestForwardOpenAI_defaultHeadersMatchNode(t *testing.T) {
	tests := []struct {
		name       string
		static     map[string]string
		isStream   bool
		wantAgent  string
		wantAccept string
	}{
		{
			name:       "defaults when unset",
			wantAgent:  "node",
			wantAccept: "*/*",
		},
		{
			name:       "operator values win",
			static:     map[string]string{"User-Agent": "custom-ua", "Accept": "application/json"},
			wantAgent:  "custom-ua",
			wantAccept: "application/json",
		},
		{
			name:       "lowercase operator values win",
			static:     map[string]string{"user-agent": "custom-ua", "accept": "application/json"},
			wantAgent:  "custom-ua",
			wantAccept: "application/json",
		},
		{
			name:       "streaming asks for event-stream",
			isStream:   true,
			wantAgent:  "node",
			wantAccept: "text/event-stream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAgent, gotAccept string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAgent = r.Header.Get("User-Agent")
				gotAccept = r.Header.Get("Accept")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			cfg := &providers.ProviderConfig{
				BaseURL:       srv.URL,
				AuthHeader:    "Authorization",
				AuthScheme:    "bearer",
				StaticHeaders: tt.static,
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
		})
	}
}
