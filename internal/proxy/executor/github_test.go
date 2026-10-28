package executor

import (
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

func TestForwardGithubContextOverflowNormalization(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		wantCode     string
		wantModified bool
	}{
		{
			name:   "copilot context window overflow normalized",
			status: http.StatusBadRequest,
			body: `{
				"error": {
					"code": "invalid_request_body",
					"message": "Your input exceeds the context window of this model. Please adjust your input and try again.",
					"type": "invalid_request_error"
				}
			}`,
			wantCode:     "context_length_exceeded",
			wantModified: true,
		},
		{
			name:   "other invalid_request_body untouched",
			status: http.StatusBadRequest,
			body: `{
				"error": {
					"code": "invalid_request_body",
					"message": "Unknown parameter 'foo'.",
					"type": "invalid_request_error"
				}
			}`,
			wantCode:     "invalid_request_body",
			wantModified: false,
		},
		{
			name:   "500 internal error untouched",
			status: http.StatusInternalServerError,
			body: `{
				"error": {
					"code": "server_error",
					"message": "Internal error"
				}
			}`,
			wantCode:     "server_error",
			wantModified: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			rec := httptest.NewRecorder()
			cfg := &providers.ProviderConfig{
				BaseURL: server.URL,
			}
			req := &Request{
				Ctx:       t.Context(),
				Client:    server.Client(),
				Provider:  "github",
				Config:    cfg,
				Endpoint:  server.URL,
				ModelName: "gpt-4o",
				Body:      []byte(`{"model":"gpt-4o","messages":[]}`),
			}

			err := ForwardGithub(rec, req)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}

			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected UpstreamError, got %T: %v", err, err)
			}

			var parsed struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(ue.Body, &parsed); err != nil {
				t.Fatalf("unmarshal error body: %v", err)
			}

			if parsed.Error.Code != tc.wantCode {
				t.Errorf("expected code %q, got %q", tc.wantCode, parsed.Error.Code)
			}
		})
	}
}
