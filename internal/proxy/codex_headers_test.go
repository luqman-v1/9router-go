package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

func TestForwardCodex_sendsChatGPTAccountHeader(t *testing.T) {
	// chatgpt-account-id selects the ChatGPT workspace a request bills
	// against. The codex endpoint rejects requests without it, so a
	// connection whose chatgptAccountId never reaches the header is an account
	// that authorizes but cannot complete a single call.
	tests := []struct {
		name        string
		psd         map[string]any
		wantHeader  string
		wantPresent bool
	}{
		{
			name:        "account id from providerSpecificData",
			psd:         map[string]any{"chatgptAccountId": "acct-1"},
			wantHeader:  "acct-1",
			wantPresent: true,
		},
		{
			name:        "account id from workspaceId fallback",
			psd:         map[string]any{"workspaceId": "ws-1"},
			wantHeader:  "ws-1",
			wantPresent: true,
		},
		{
			name:        "account id from accountId fallback",
			psd:         map[string]any{"accountId": "acc-1"},
			wantHeader:  "acc-1",
			wantPresent: true,
		},
		{
			name:        "workspaceId preferred over chatgptAccountId",
			psd:         map[string]any{"workspaceId": "ws-primary", "chatgptAccountId": "acct-secondary"},
			wantHeader:  "ws-primary",
			wantPresent: true,
		},
		{
			name:        "present alongside the plan type",
			psd:         map[string]any{"chatgptAccountId": "acct-2", "chatgptPlanType": "pro"},
			wantHeader:  "acct-2",
			wantPresent: true,
		},
		{
			// An empty header is worse than none: it is sent, and it names no
			// workspace, so the header must be omitted entirely.
			name:        "empty account id is omitted",
			psd:         map[string]any{"chatgptAccountId": ""},
			wantPresent: false,
		},
		{
			name:        "non-string account id is omitted",
			psd:         map[string]any{"chatgptAccountId": 42},
			wantPresent: false,
		},
		{
			name:        "no providerSpecificData",
			psd:         nil,
			wantPresent: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotHeader []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Values("Chatgpt-Account-Id")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
			_, err := ForwardCodex(t.Context(), srv.Client(), cfg, "sk-test", []byte(`{}`), false, tt.psd, false, "")
			if err != nil {
				t.Fatalf("ForwardCodex() error = %v", err)
			}
			if (len(gotHeader) > 0) != tt.wantPresent {
				t.Fatalf("chatgpt-account-id present = %v, want %v (values %q)", len(gotHeader) > 0, tt.wantPresent, gotHeader)
			}
			if tt.wantPresent && (len(gotHeader) == 0 || gotHeader[0] != tt.wantHeader) {
				t.Errorf("chatgpt-account-id = %q, want %q", gotHeader, tt.wantHeader)
			}
		})
	}
}

func TestForwardCodex_sendsSessionIDHeader(t *testing.T) {
	// session_id is required by the Codex backend to pin requests to the same
	// server-side prompt cache worker. Matching upstream:
	// headers["session_id"] = this._currentSessionId || credentials?.connectionId || "default"
	tests := []struct {
		name        string
		sessionID   string
		psd         map[string]any
		wantSession string
	}{
		{
			name:        "explicit session ID is forwarded",
			sessionID:   "sess-alpha",
			psd:         nil,
			wantSession: "sess-alpha",
		},
		{
			name:        "connectionId fallback when session ID is empty",
			sessionID:   "",
			psd:         map[string]any{"connectionId": "cx-conn-99"},
			wantSession: "cx-conn-99",
		},
		{
			name:        "default fallback when both are empty",
			sessionID:   "",
			psd:         nil,
			wantSession: "default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotSession string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotSession = r.Header.Get("session_id")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			cfg := &providers.ProviderConfig{BaseURL: srv.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
			_, err := ForwardCodex(t.Context(), srv.Client(), cfg, "sk-test", []byte(`{}`), false, tt.psd, false, tt.sessionID)
			if err != nil {
				t.Fatalf("ForwardCodex() error = %v", err)
			}
			if gotSession != tt.wantSession {
				t.Errorf("session_id = %q, want %q", gotSession, tt.wantSession)
			}
		})
	}
}
