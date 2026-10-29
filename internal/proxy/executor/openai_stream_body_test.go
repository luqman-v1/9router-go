package executor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// A streaming request whose upstream answers 200 with a body that is not an
// event stream used to be piped through the SSE scanner: zero frames in, a
// clean [DONE] out, and no error for the fallback layer to act on. The
// upstream's own Content-Type is the only pre-header signal, so it is checked
// first and the body is then classified the same way a non-streaming request
// would be.
func TestForwardOpenAI_StreamRequestWithNonStreamBody(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantErr     bool
	}{
		{
			name:        "html error page behind a 200",
			contentType: "text/html",
			body:        "<html><body>502 Bad Gateway</body></html>",
			wantErr:     true,
		},
		{
			name:        "blank 200",
			contentType: "application/json",
			body:        "",
			wantErr:     true,
		},
		{
			name:        "200 error envelope",
			contentType: "application/json",
			body:        `{"error":{"message":"model overloaded","type":"server_error"}}`,
			wantErr:     true,
		},
		{
			name:        "a completion sent as JSON still serves the client",
			contentType: "application/json",
			body:        `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`,
			wantErr:     false,
		},
		{
			name:        "an event stream mislabelled as JSON still streams",
			contentType: "application/json",
			body:        "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			rec := httptest.NewRecorder()
			err := ForwardOpenAI(rec, &Request{
				Ctx:      t.Context(),
				Client:   srv.Client(),
				Config:   &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"},
				APIKey:   "k",
				Body:     []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`),
				IsStream: true,
			})

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("ForwardOpenAI: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("served a 200 with no answer as a stream: %s", rec.Body.String())
			}
			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("error is %T, want *proxy.UpstreamError", err)
			}
			if ue.StatusCode != http.StatusBadGateway {
				t.Errorf("StatusCode = %d, want 502 so the router fails over", ue.StatusCode)
			}
		})
	}
}

// TestForwardOpenAI_MislabelledSSEStreamsLive pins that a stream arriving
// under a JSON content-type is forwarded frame by frame: the first frame must
// be readable while the upstream is still holding the rest of the body back.
// A whole-body buffer would hang here until the release.
func TestForwardOpenAI_MislabelledSSEStreamsLive(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The mislabel: an SSE body under application/json.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = ForwardOpenAI(w, &Request{
			Ctx:      r.Context(),
			Client:   upstream.Client(),
			Config:   &providers.ProviderConfig{BaseURL: upstream.URL + "/chat/completions"},
			APIKey:   "k",
			Body:     []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			IsStream: true,
		})
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader([]byte(`{"model":"m","stream":true}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("first line before the upstream released the body: %v", err)
	}
	if !strings.Contains(first, `"content":"hi"`) {
		t.Fatalf("first line = %q, want the first mislabelled frame", first)
	}

	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("stream after release lost [DONE]: %q", rest)
	}
}
