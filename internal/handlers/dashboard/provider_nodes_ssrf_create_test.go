package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The write path still refuses cloud metadata and non-http(s) URLs. A private
// or LAN base URL is a normal upstream for a self-hosted gateway, so it is
// accepted while private networking is on (the default) and refused only when
// the operator sets ALLOW_PRIVATE_NETWORKING=false.
//
// The socket-level guard in proxy.NewSafeTransport still backs the write path
// up (chat/resolution.go builds its client from it).

func createProviderNodeStatus(t *testing.T, baseURL string) int {
	t.Helper()
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	handler := NewDashboardHandler(repo, nil)
	r := chi.NewRouter()
	r.Post("/api/provider-nodes", handler.HandleCreateProviderNode)

	body := `{"name":"N","prefix":"n","apiType":"chat","type":"openai-compatible","baseUrl":"` + baseURL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider-nodes", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func TestHandleCreateProviderNode_RejectsInternalBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    int
	}{
		{name: "cloud metadata", baseURL: "http://169.254.169.254/latest/meta-data/", want: http.StatusBadRequest},
		{name: "alibaba metadata", baseURL: "http://100.100.100.200/latest/meta-data/", want: http.StatusBadRequest},
		{name: "gcp metadata name", baseURL: "http://metadata.google.internal/v1", want: http.StatusBadRequest},
		{name: "decimal metadata", baseURL: "http://2852039166/latest/meta-data/", want: http.StatusBadRequest},
		{name: "not a url", baseURL: "not-a-url", want: http.StatusBadRequest},
		{name: "rfc1918 accepted", baseURL: "http://192.168.1.50/v1", want: http.StatusCreated},
		{name: "loopback accepted", baseURL: "http://127.0.0.1:11434/v1", want: http.StatusCreated},
		{name: "public accepted", baseURL: "https://api.example.com/v1", want: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := createProviderNodeStatus(t, tt.baseURL); got != tt.want {
				t.Fatalf("baseUrl %q: status = %d, want %d", tt.baseURL, got, tt.want)
			}
		})
	}
}

// Strict mode is the opt-out: ALLOW_PRIVATE_NETWORKING=false restores the old
// refusal of private/LAN base URLs for a non-loopback caller.
func TestHandleCreateProviderNode_StrictModeRejectsInternalBaseURL(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")
	for _, baseURL := range []string{
		"http://192.168.1.50/v1",
		"http://127.0.0.1:11434/v1",
	} {
		if got := createProviderNodeStatus(t, baseURL); got != http.StatusBadRequest {
			t.Errorf("strict mode: baseUrl %q: status = %d, want %d", baseURL, got, http.StatusBadRequest)
		}
	}
}
