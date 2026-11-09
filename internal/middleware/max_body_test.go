package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxBodyMiddleware(t *testing.T) {
	middleware := MaxBody(100)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Case 1: Body within limit
	reqSmall := httptest.NewRequest("POST", "/test", bytes.NewReader(make([]byte, 50)))
	recSmall := httptest.NewRecorder()
	handler.ServeHTTP(recSmall, reqSmall)
	if recSmall.Code != http.StatusOK {
		t.Errorf("expected 200 for small body, got %d", recSmall.Code)
	}

	// Case 2: Body exceeds limit
	reqLarge := httptest.NewRequest("POST", "/test", bytes.NewReader(make([]byte, 200)))
	recLarge := httptest.NewRecorder()
	handler.ServeHTTP(recLarge, reqLarge)
	if recLarge.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized body, got %d", recLarge.Code)
	}
}

// TestMaxBodyByPathCeilings pins the boundary contract of the path-aware limit:
// a media upload clears the JSON ceiling, the JSON routes keep theirs, and the
// byte that crosses a ceiling is a 413 naming that ceiling rather than a 400
// that reads like a malformed body (#125).
func TestMaxBodyByPathCeilings(t *testing.T) {
	const (
		jsonLimit  = 1024
		mediaLimit = 4096
	)
	rules := []BodyLimitRule{{Suffix: "/videos/generations", MaxBytes: mediaLimit}}

	tests := []struct {
		name       string
		path       string
		size       int
		wantStatus int
		wantLimit  int64
	}{
		{name: "media body below its own ceiling passes the JSON ceiling", path: "/v1/videos/generations", size: 2048, wantStatus: http.StatusOK, wantLimit: mediaLimit},
		{name: "media body exactly at its ceiling", path: "/v1/videos/generations", size: mediaLimit, wantStatus: http.StatusOK, wantLimit: mediaLimit},
		{name: "media body one byte over its ceiling", path: "/v1/videos/generations", size: mediaLimit + 1, wantStatus: http.StatusRequestEntityTooLarge, wantLimit: mediaLimit},
		{name: "json route keeps the default ceiling", path: "/v1/chat/completions", size: jsonLimit, wantStatus: http.StatusOK, wantLimit: jsonLimit},
		{name: "json body below the media ceiling is still cut at the default", path: "/v1/chat/completions", size: 2048, wantStatus: http.StatusRequestEntityTooLarge, wantLimit: jsonLimit},
		{name: "json body one byte over the default", path: "/v1/chat/completions", size: jsonLimit + 1, wantStatus: http.StatusRequestEntityTooLarge, wantLimit: jsonLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seenLimit int64
			var seenOK bool

			handler := MaxBodyByPath(jsonLimit, rules)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenLimit, seenOK = BodyLimitFromContext(r.Context())
				if _, err := io.ReadAll(r.Body); err != nil {
					WriteBodyReadError(w, r, err)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(make([]byte, tt.size)))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !seenOK {
				t.Fatal("handler ran without a body limit on the request context")
			}
			if seenLimit != tt.wantLimit {
				t.Errorf("context limit = %d, want %d", seenLimit, tt.wantLimit)
			}
			if rec.Code == http.StatusRequestEntityTooLarge {
				if !strings.Contains(rec.Body.String(), HumanBytes(tt.wantLimit)) {
					t.Errorf("413 message %q does not name the %s ceiling", rec.Body.String(), HumanBytes(tt.wantLimit))
				}
				if !strings.Contains(rec.Body.String(), "payload_too_large") {
					t.Errorf("413 body %q does not carry the payload_too_large error code", rec.Body.String())
				}
			}
		})
	}
}

// TestBodyLimitRulesCoverMediaUploads guards the wiring the router depends on:
// every endpoint named in #125 gets a ceiling above the default, and a JSON
// route is left alone.
func TestBodyLimitRulesCoverMediaUploads(t *testing.T) {
	rules := MediaBodyLimitRules()

	mediaPaths := []string{
		"/videos/generations",
		"/videos/edits",
		"/videos/extensions",
		"/audio/transcriptions",
	}
	for _, path := range mediaPaths {
		t.Run("media "+path, func(t *testing.T) {
			for _, prefixed := range []string{path, "/v1" + path, "/api/v1" + path} {
				if got := BodyLimitForPath(prefixed, DefaultMaxBodySize, rules); got != MediaMaxBodySize {
					t.Errorf("limit for %s = %d, want %d", prefixed, got, MediaMaxBodySize)
				}
			}
		})
	}

	t.Run("json route is not raised", func(t *testing.T) {
		if got := BodyLimitForPath("/v1/chat/completions", DefaultMaxBodySize, rules); got != DefaultMaxBodySize {
			t.Errorf("limit = %d, want the %d default", got, DefaultMaxBodySize)
		}
	})

	t.Run("media ceiling is larger than the default", func(t *testing.T) {
		if MediaMaxBodySize <= DefaultMaxBodySize {
			t.Fatalf("MediaMaxBodySize = %d must exceed DefaultMaxBodySize = %d", MediaMaxBodySize, DefaultMaxBodySize)
		}
	})

	t.Run("a rule may not shrink the default", func(t *testing.T) {
		shrink := []BodyLimitRule{{Suffix: "/videos/generations", MaxBytes: 128}}
		if got := BodyLimitForPath("/videos/generations", DefaultMaxBodySize, shrink); got != DefaultMaxBodySize {
			t.Errorf("limit = %d, want %d — overrides may only raise", got, DefaultMaxBodySize)
		}
	})
}

// TestWriteBodyReadErrorKeepsCorruptBodiesAt400 is the other half of the split:
// only a MaxBytesError is a 413. A truncated body is still a 400, so the new
// status does not swallow genuine malformed-request reporting.
func TestWriteBodyReadErrorKeepsCorruptBodiesAt400(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", nil)
	WriteBodyReadError(rec, req, io.ErrUnexpectedEOF)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "failed to read request body") {
		t.Errorf("body %q does not carry the malformed-body message", rec.Body.String())
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{in: DefaultMaxBodySize, want: "10MB"},
		{in: MediaMaxBodySize, want: "128MB"},
		{in: 1024, want: "1KB"},
		{in: 1500, want: "1500 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := HumanBytes(tt.in); got != tt.want {
				t.Errorf("HumanBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
