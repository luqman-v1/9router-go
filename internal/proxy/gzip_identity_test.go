package proxy

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDoRequest_UnwrapsGzipDespiteIdentity pins the guard that keeps a
// non-compliant upstream from breaking a stream: a request that pinned
// Accept-Encoding: identity is not auto-decompressed by the transport, so a
// gzip answer has to be unwrapped by the gateway.
func TestDoRequest_UnwrapsGzipDespiteIdentity(t *testing.T) {
	const payload = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"

	tests := []struct {
		name         string
		headers      map[string]string
		compressed   bool
		wantBody     string
		wantEncoding string
	}{
		{
			name:         "identity request, gzip response is unwrapped",
			headers:      map[string]string{"Accept-Encoding": "identity"},
			compressed:   true,
			wantBody:     payload,
			wantEncoding: "",
		},
		{
			name:         "identity request, plain response untouched",
			headers:      map[string]string{"Accept-Encoding": "identity"},
			compressed:   false,
			wantBody:     payload,
			wantEncoding: "",
		},
		{
			name:         "transport-negotiated gzip is unwrapped by Go itself",
			headers:      map[string]string{},
			compressed:   true,
			wantBody:     payload,
			wantEncoding: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !tt.compressed {
					_, _ = io.WriteString(w, payload)
					return
				}
				w.Header().Set("Content-Encoding", "gzip")
				gz := gzip.NewWriter(w)
				defer gz.Close()
				_, _ = io.WriteString(gz, payload)
			}))
			defer srv.Close()

			resp, err := DoRequest(t.Context(), srv.Client(), http.MethodPost, srv.URL, tt.headers, []byte(`{}`))
			if err != nil {
				t.Fatalf("DoRequest() error = %v", err)
			}
			defer resp.Body.Close()

			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if enc := resp.Header.Get("Content-Encoding"); enc != tt.wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", enc, tt.wantEncoding)
			}
		})
	}
}

// TestNewJSONRequest_PinsIdentityForStreams pins the shared request
// constructor's heuristic: a lane that asks for an event stream gets identity
// unless it already chose an encoding.
func TestNewJSONRequest_PinsIdentityForStreams(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "event stream gets identity",
			headers: map[string]string{"Accept": "text/event-stream"},
			want:    "identity",
		},
		{
			name:    "lowercase accept gets identity",
			headers: map[string]string{"accept": "text/event-stream"},
			want:    "identity",
		},
		{
			name:    "json keeps the transport default",
			headers: map[string]string{"Accept": "application/json"},
			want:    "",
		},
		{
			name:    "explicit encoding wins",
			headers: map[string]string{"Accept": "text/event-stream", "Accept-Encoding": "br"},
			want:    "br",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := newJSONRequest(t.Context(), http.MethodPost, "http://upstream.test/v1/chat/completions", tt.headers, []byte(`{}`))
			if err != nil {
				t.Fatalf("newJSONRequest() error = %v", err)
			}
			if got := req.Header.Get("Accept-Encoding"); got != tt.want {
				t.Errorf("Accept-Encoding = %q, want %q", got, tt.want)
			}
		})
	}
}
