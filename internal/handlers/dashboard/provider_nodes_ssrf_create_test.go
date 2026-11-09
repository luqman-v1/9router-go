package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestHandleCreateProviderNode_RejectsInternalBaseURL pins the URL gate on the
// write path. A share link can carry an arbitrary url=, so this is the one place
// an untrusted string reaches the node table: without the gate the row is
// stored and the operator only learns it is unusable when the first request
// fails with a dial error naming an address they never typed.
//
// The socket-level guard in proxy.NewSafeTransport still blocks the dial
// (chat/resolution.go builds its client from it), so this is a fail-fast with a
// clear message, not the only defence.
func TestHandleCreateProviderNode_RejectsInternalBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    int
	}{
		{name: "cloud metadata", baseURL: "http://169.254.169.254/latest/meta-data/", want: http.StatusBadRequest},
		{name: "alibaba metadata", baseURL: "http://100.100.100.200/latest/meta-data/", want: http.StatusBadRequest},
		{name: "gcp metadata name", baseURL: "http://metadata.google.internal/v1", want: http.StatusBadRequest},
		{name: "rfc1918", baseURL: "http://192.168.1.50/v1", want: http.StatusBadRequest},
		{name: "loopback", baseURL: "http://127.0.0.1:11434/v1", want: http.StatusBadRequest},
		{name: "decimal metadata", baseURL: "http://2852039166/latest/meta-data/", want: http.StatusBadRequest},
		{name: "not a url", baseURL: "not-a-url", want: http.StatusBadRequest},
		{name: "public is accepted", baseURL: "https://api.example.com/v1", want: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			handler := NewDashboardHandler(repo, nil)
			r := chi.NewRouter()
			r.Post("/api/provider-nodes", handler.HandleCreateProviderNode)

			body := `{"name":"N","prefix":"n","apiType":"chat","type":"openai-compatible","baseUrl":"` + tt.baseURL + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/provider-nodes", bytes.NewReader([]byte(body)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("baseUrl %q: status = %d, want %d (body %.200s)", tt.baseURL, rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}
