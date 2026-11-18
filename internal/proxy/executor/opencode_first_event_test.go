package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// bodyFunc adapts a function to io.ReadCloser so a test can script reads.
type bodyFunc struct {
	read  func([]byte) (int, error)
	close func() error
}

func (b bodyFunc) Read(p []byte) (int, error) { return b.read(p) }
func (b bodyFunc) Close() error {
	if b.close != nil {
		return b.close()
	}
	return nil
}

func readerBody(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// sseResponse wraps body in a 200 response carrying the SSE content type, which
// is what the gate keys on.
func sseResponse(body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
}

func TestAwaitOpenCodeFirstEvent(t *testing.T) {
	const healthy = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
	const usageLimit = "data: {\"type\":\"error\",\"error\":{\"type\":\"FreeUsageLimitError\",\"message\":\"Free usage limit reached\"}}\n\n"
	const tierError = "data: {\"type\":\"error\",\"error\":{\"type\":\"FreeTierError\",\"message\":\"nope\"}}\n\n"

	tests := []struct {
		name       string
		body       io.ReadCloser
		wantStatus int
		wantErr    bool
		wantPrefix string
	}{
		{
			name:       "healthy stream is replayed in full",
			body:       readerBody(healthy),
			wantPrefix: healthy,
		},
		{
			name: "in-band usage limit becomes a 429 on the error path",
			body: readerBody(usageLimit),
			// The whole point: the HTTP response said 200, and the refusal is
			// still reported as the status it implies.
			wantStatus: http.StatusTooManyRequests,
			wantErr:    true,
		},
		{
			name:       "in-band tier refusal becomes a 403",
			body:       readerBody(tierError),
			wantStatus: http.StatusForbidden,
			wantErr:    true,
		},
		{
			name:    "stream that ends before any frame fails",
			body:    readerBody(""),
			wantErr: true,
		},
		{
			name:    "stream that ends mid-frame fails",
			body:    readerBody("data: {\"partial\":true}"),
			wantErr: true,
		},
		{
			name:       "leading keepalive blank lines are tolerated",
			body:       readerBody("\n\ndata: {\"choices\":[]}\n\n"),
			wantPrefix: "\n\ndata: {\"choices\":[]}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := awaitOpenCodeFirstEvent(context.Background(), sseResponse(tt.body), 2*time.Second)
			if tt.wantErr {
				if err == nil {
					if got != nil {
						got.Close()
					}
					t.Fatal("expected an error, got none")
				}
				if tt.wantStatus != 0 {
					var ue *proxy.UpstreamError
					if !errors.As(err, &ue) {
						t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
					}
					if ue.StatusCode != tt.wantStatus {
						t.Errorf("status = %d, want %d", ue.StatusCode, tt.wantStatus)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer got.Close()
			rest, rerr := io.ReadAll(got)
			if rerr != nil {
				t.Fatalf("read replayed stream: %v", rerr)
			}
			if string(rest) != tt.wantPrefix {
				t.Errorf("replayed body = %q, want %q", rest, tt.wantPrefix)
			}
		})
	}
}

// A stalled upstream must fail the attempt rather than block until the client
// gives up. The stall reader closes the body, which unblocks the pending read.
func TestAwaitOpenCodeFirstEvent_StalledUpstreamFails(t *testing.T) {
	closed := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(closed) }) }
	t.Cleanup(unblock)

	// A real http body unblocks its pending Read when closed — that is how the
	// stall reader ends a stuck stream. A fake that ignored Close would hang
	// this test rather than the router, so close is part of the contract here.
	body := bodyFunc{
		read: func([]byte) (int, error) {
			<-closed
			return 0, errors.New("body closed")
		},
		close: func() error {
			unblock()
			return nil
		},
	}

	start := time.Now()
	_, err := awaitOpenCodeFirstEvent(context.Background(), sseResponse(body), 150*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a stalled upstream must report an error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("stall was not bounded: waited %s", elapsed)
	}
}

// A client that disconnects mid-wait must release the attempt promptly, so a
// cancelled context cannot leave the gate holding a socket.
func TestAwaitOpenCodeFirstEvent_ContextCancelReleases(t *testing.T) {
	closed := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(closed) }) }
	t.Cleanup(unblock)

	body := bodyFunc{
		read: func([]byte) (int, error) {
			<-closed
			return 0, errors.New("body closed")
		},
		close: func() error {
			unblock()
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := awaitOpenCodeFirstEvent(ctx, sseResponse(body), 30*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled context must report an error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation was not honoured: waited %s", elapsed)
	}
}

// A prelude larger than the cap is a refusal in its own right: holding it would
// only delay the failover.
func TestReadFirstOpenCodeFrame_RejectsOversizedPrelude(t *testing.T) {
	// A never-terminating prelude: bytes with no `data:` frame in sight.
	huge := strings.Repeat("x", 1<<20)
	_, err := readFirstOpenCodeFrame(strings.NewReader(huge))
	if !errors.Is(err, errOpenCodePreludeTooLarge) {
		t.Fatalf("expected errOpenCodePreludeTooLarge, got %v", err)
	}
}

// The gate must hand back every byte it consumed. A replayed stream that lost
// its prelude would truncate the client's answer.
func TestAwaitOpenCodeFirstEvent_PreservesPrelude(t *testing.T) {
	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n" +
		"data: [DONE]\n\n"

	got, err := awaitOpenCodeFirstEvent(context.Background(), sseResponse(readerBody(payload)), 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer got.Close()

	all, rerr := io.ReadAll(got)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if string(all) != payload {
		t.Errorf("replay lost bytes:\n got %q\nwant %q", all, payload)
	}
}

// A refusal that arrives inside a 200 SSE frame must be reported as the status it
// implies, not streamed to the client as a successful turn. This is the failure
// the gate exists for: the response head says 200, so nothing downstream can
// tell it apart from a healthy stream.
func TestForwardOpencode_InBandRefusalIsNotServedAsSuccess(t *testing.T) {
	tests := []struct {
		name       string
		frame      string
		wantStatus int
	}{
		{
			name:       "usage limit inside a 200",
			frame:      "data: {\"type\":\"error\",\"error\":{\"type\":\"FreeUsageLimitError\",\"message\":\"Free usage limit reached\"}}\n\n",
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "tier error inside a 200",
			frame:      "data: {\"type\":\"error\",\"error\":{\"type\":\"FreeTierError\",\"message\":\"nope\"}}\n\n",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// A 200 with the SSE content type: exactly what the gate sends
				// when it refuses in-band.
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.frame))
			}))
			defer srv.Close()

			rec := httptest.NewRecorder()
			req := &Request{
				Client:   srv.Client(),
				Config:   &providers.ProviderConfig{BaseURL: srv.URL},
				Body:     []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}],"stream":true}`),
				IsStream: true,
			}
			err := ForwardOpencode(rec, req)
			if err == nil {
				t.Fatalf("an in-band refusal must be reported, not served as success; body=%q", rec.Body.String())
			}
			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
			}
			if ue.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", ue.StatusCode, tt.wantStatus)
			}
		})
	}
}

// A healthy stream must still reach the client unchanged, with every byte the
// gate read replayed ahead of it.
func TestForwardOpencode_HealthyStreamSurvivesTheGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL},
		Body:     []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("healthy stream must not be rejected: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("stream content was lost: %q", rec.Body.String())
	}
}
