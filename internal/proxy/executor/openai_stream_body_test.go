package executor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	// Released on every path, including a failed assertion. The gate is
	// registered after upstream.Close so it runs first: an early return that
	// left the handler blocked on <-release would hang upstream.Close, and a
	// hang reports the package timeout instead of the assertion that failed.
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
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
	defer releaseUpstream()

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

	releaseUpstream()
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("stream after release lost [DONE]: %q", rest)
	}
}

// TestForwardOpenAI_MislabelledSSEPreambleStreamsLive pins that a stream whose
// first flushed line is blank is still a stream. The sniff takes its verdict
// on the first line that carries something, so a keepalive blank line before
// the first field cannot fold the turn into a single document.
func TestForwardOpenAI_MislabelledSSEPreambleStreamsLive(t *testing.T) {
	for name, preamble := range map[string]string{
		"blank line": "\n",
		"crlf":       "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(preamble))
				w.(http.Flusher).Flush()
				_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
			}))
			defer upstream.Close()

			rec := httptest.NewRecorder()
			err := ForwardOpenAI(rec, &Request{
				Ctx:      t.Context(),
				Client:   upstream.Client(),
				Config:   &providers.ProviderConfig{BaseURL: upstream.URL + "/chat/completions"},
				APIKey:   "k",
				Body:     []byte(`{"model":"m","stream":true}`),
				IsStream: true,
			})
			if err != nil {
				t.Fatalf("ForwardOpenAI: %v", err)
			}
			if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
				t.Fatalf("Content-Type = %q, want text/event-stream: a blank preamble folded the stream into a document", got)
			}
			if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
				t.Fatalf("stream lost its frame: %q", rec.Body.String())
			}
		})
	}
}

// TestForwardOpenAI_MislabelledSSEClosesUpstreamBody pins that the wrapper
// replaying the sniffed head keeps the upstream body's Close. The stall
// watchdog ends a stuck stream by closing the reader, so a wrapper that
// swallows Close leaves both the watchdog and the deferred release unable to
// reach the socket.
func TestForwardOpenAI_MislabelledSSEClosesUpstreamBody(t *testing.T) {
	var closed atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
		w.(http.Flusher).Flush()
		// Hold the turn open for a bounded moment: only a Close ends it
		// earlier, which is what the deferred release relies on. The bound
		// matters — an unbounded wait turns a failed assertion into a hung
		// package instead of one reported line.
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer upstream.Close()

	client := &http.Client{Transport: recordingTransport{base: upstream.Client().Transport, closed: &closed}}
	rec := httptest.NewRecorder()
	if err := ForwardOpenAI(rec, &Request{
		Ctx:      t.Context(),
		Client:   client,
		Config:   &providers.ProviderConfig{BaseURL: upstream.URL + "/chat/completions"},
		APIKey:   "k",
		Body:     []byte(`{"model":"m","stream":true}`),
		IsStream: true,
	}); err != nil {
		t.Fatalf("ForwardOpenAI: %v", err)
	}
	if !closed.Load() {
		t.Fatal("upstream body was never closed: the replay wrapper dropped its Close")
	}
}

// recordingTransport marks the response body when it is closed.
type recordingTransport struct {
	base   http.RoundTripper
	closed *atomic.Bool
}

func (t recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &recordingBody{ReadCloser: resp.Body, closed: t.closed}
	return resp, nil
}

type recordingBody struct {
	io.ReadCloser
	closed *atomic.Bool
}

func (b *recordingBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

// TestReplayBodyCloseReachesUpstream pins the property the wrapper exists for:
// closing the replay body closes the upstream body underneath it, which is how
// the stall watchdog interrupts a read that is already blocked.
func TestReplayBodyCloseReachesUpstream(t *testing.T) {
	var closed atomic.Bool
	underlying := &recordingBody{ReadCloser: io.NopCloser(strings.NewReader("data: {}\n\n")), closed: &closed}
	body := replayBody{Reader: io.MultiReader(strings.NewReader(""), underlying), Closer: underlying}

	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read replay body: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("close replay body: %v", err)
	}
	if !closed.Load() {
		t.Fatal("closing the replay body did not close the upstream body")
	}
}
