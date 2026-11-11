package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

func TestForwardGemini_Headers_Injection(t *testing.T) {
	tests := []struct {
		name       string
		projectID  string
		sessionCtx context.Context
		want       map[string]string // header -> expected value (empty string means just present)
		wantAbsent []string          // headers that must NOT be present
	}{
		{
			name:      "no project, no session",
			projectID: "",
			want: map[string]string{
				"X-Client-Name":     "antigravity",
				"X-Client-Version":  providers.AntigravityCLIVersion,
			},
			wantAbsent: []string{"X-Goog-User-Project", "X-Machine-Session-Id"},
		},
		{
			name:      "project set, no session",
			projectID: "my-proj",
			want: map[string]string{
				"X-Goog-User-Project": "my-proj",
				"X-Client-Name":       "antigravity",
				"X-Client-Version":    providers.AntigravityCLIVersion,
			},
			wantAbsent: []string{"X-Machine-Session-Id"},
		},
		{
			name:       "project set with session",
			projectID:  "my-proj",
			sessionCtx: handlerutil.WithSessionID(context.Background(), "sess-123"),
			want: map[string]string{
				"X-Goog-User-Project": "my-proj",
				"X-Machine-Session-Id": "sess-123",
				"X-Client-Name":       "antigravity",
				"X-Client-Version":    providers.AntigravityCLIVersion,
			},
		},
		{
			name:       "no project with session",
			projectID:  "",
			sessionCtx: handlerutil.WithSessionID(context.Background(), "sess-456"),
			want: map[string]string{
				"X-Machine-Session-Id": "sess-456",
				"X-Client-Name":        "antigravity",
				"X-Client-Version":     providers.AntigravityCLIVersion,
			},
			wantAbsent: []string{"X-Goog-User-Project"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got headerBag
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = captureHeaders(r)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			ctx := tt.sessionCtx
			if ctx == nil {
				ctx = context.Background()
			}
			cfg := &providers.ProviderConfig{BaseURL: srv.URL}
			body := `{"model":"gemini-3.8-flash-medium","messages":[{"role":"user","content":"hi"}]}`
			_, err := ForwardGemini(ctx, srv.Client(), cfg, "ya29.fake", body, false, tt.projectID, "gemini-3.8-flash-medium")
			if err != nil {
				t.Fatalf("ForwardGemini() error = %v", err)
			}

			for k, v := range tt.want {
				actual := got.get(k)
				if actual == "" {
					t.Errorf("header %q missing, want %q", k, v)
					continue
				}
				if v != "" && actual != v {
					t.Errorf("header %q = %q, want %q", k, actual, v)
				}
			}
			for _, k := range tt.wantAbsent {
				if actual := got.get(k); actual != "" {
					t.Errorf("header %q = %q, want absent", k, actual)
				}
			}
		})
	}
}

// headerBag captures request headers case-insensitively.
type headerBag map[string]string

func captureHeaders(r *http.Request) headerBag {
	h := make(headerBag)
	for k, vs := range r.Header {
		if len(vs) > 0 {
			h[strings.ToLower(k)] = vs[0]
		}
	}
	return h
}

func (h headerBag) get(k string) string {
	return h[strings.ToLower(k)]
}
