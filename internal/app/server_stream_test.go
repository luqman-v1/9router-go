package app_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// TestServer_SlowStreamingRequestSurvivesTheLimits answers the question a
// timeout change always invites: does the gateway still serve a model that
// answers slowly, and does it still stream that answer token by token?
//
// The limits added for Slowloris are ReadHeaderTimeout, IdleTimeout and
// MaxHeaderBytes. None of them is a deadline on the response, so a handler
// may take as long as it likes and may trickle output indefinitely. This test
// runs one that does all three risky things at once: headers uploaded in five
// slow pieces, a silence before the first byte, then a body that keeps
// trickling for seconds.
//
// A WriteTimeout would sever this mid-stream; ReadHeaderTimeout would kill a
// genuinely slow uploader; IdleTimeout would cut the connection if it were
// mistaken for a write deadline. The streaming must survive all three.
func TestServer_SlowStreamingRequestSurvivesTheLimits(t *testing.T) {
	const (
		headerUploadTime = 3 * time.Second // five pieces, 600ms apart
		firstByteDelay   = 400 * time.Millisecond
		chunkGap         = 400 * time.Millisecond
		chunkCount       = 6
	)

	var served int
	r := chi.NewRouter()
	r.Post("/slow/stream", func(w http.ResponseWriter, req *http.Request) {
		served++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, _ := w.(http.Flusher)
		// A slow provider: silence before the first token.
		time.Sleep(firstByteDelay)
		_, _ = fmt.Fprintf(w, "data: {\"delta\":\"hal\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}

		// Then trickle, the way a real SSE stream does — long enough that a
		// WriteTimeout anywhere in this chain would cut it short.
		for i := range chunkCount {
			time.Sleep(chunkGap)
			_, _ = fmt.Fprintf(w, "data: {\"delta\":\"o-%d\"}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})

	srv := newTestServer(t, r)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Upload the header block slowly, in pieces, with pauses. This is the
	// shape of the attack the ReadHeaderTimeout exists for, and the shape a
	// genuinely slow client has. It has to complete.
	go func() {
		for _, part := range []string{
			"POST /slow/stream HTTP/1.1\r\n",
			"Host: localhost\r\n",
			"Content-Type: text/plain\r\n",
			"Content-Length: 2\r\n",
			"\r\n",
		} {
			time.Sleep(headerUploadTime / 5)
			if _, werr := conn.Write([]byte(part)); werr != nil {
				return
			}
		}
		_, _ = conn.Write([]byte("hi"))
	}()

	// The whole exchange — slow headers included — must finish, and finish
	// with every chunk delivered.
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	body := make([]byte, 0, 1024)
	buf := make([]byte, 256)
	for {
		n, rerr := conn.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			t.Fatalf("stream broke after %d bytes (%q): %v", len(body), body, rerr)
		}
		if strings.Contains(string(body), "[DONE]") {
			break
		}
	}

	got := string(body)
	if served != 1 {
		t.Fatalf("handler ran %d times, want 1", served)
	}
	if !strings.Contains(got, "200 OK") {
		t.Errorf("response line missing 200: %q", firstLine(got))
	}
	for i := range chunkCount {
		want := fmt.Sprintf("o-%d", i)
		if !strings.Contains(got, want) {
			t.Errorf("chunk %q never arrived — the stream was cut: %q", want, got)
		}
	}
	if !strings.Contains(got, "[DONE]") {
		t.Errorf("stream ended without [DONE]: %q", got)
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
