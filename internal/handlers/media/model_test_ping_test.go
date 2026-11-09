package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/handlerutil"
)

func TestProbeContextFlag(t *testing.T) {
	bg := context.Background()
	if handlerutil.IsProbeContext(bg) {
		t.Error("expected background context to not be probe context")
	}

	probeCtx := handlerutil.WithProbeContext(bg)
	if !handlerutil.IsProbeContext(probeCtx) {
		t.Error("expected probe context to report true")
	}
}

func TestReadProbeResult_BlockedCooldownResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusBadGateway)
	rec.Write([]byte(`{"error":{"message":"no available connections for provider: deepseek (all in cooldown, earliest reset 2026-10-09T01:00:00Z)","code":502}}`))

	res := readProbeResult(rec, "")
	if res.OK {
		t.Error("expected OK to be false for 502")
	}
	if !res.Blocked {
		t.Error("expected Blocked to be true for all in cooldown")
	}
	if res.ResetAt != "2026-10-09T01:00:00Z" {
		t.Errorf("ResetAt = %q, want 2026-10-09T01:00:00Z", res.ResetAt)
	}
}

func TestReadProbeResult_StandardErrorIsNotBlocked(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusInternalServerError)
	rec.Write([]byte(`{"error":{"message":"upstream error: internal failure"}}`))

	res := readProbeResult(rec, "")
	if res.OK {
		t.Error("expected OK to be false")
	}
	if res.Blocked {
		t.Error("expected Blocked to be false for standard 500 error")
	}
	if res.ResetAt != "" {
		t.Errorf("expected empty ResetAt, got: %q", res.ResetAt)
	}
}

func TestReadProbeResult_MidStreamTimeoutInconclusive(t *testing.T) {
	// 1. SSE stream emitting valid first token then status 499/504 -> ok = true with inconclusive note
	rec1 := httptest.NewRecorder()
	rec1.WriteHeader(499)
	rec1.Write([]byte(`data: {"choices":[{"delta":{"content":"hello"}}]} `))

	res1 := readProbeResult(rec1, "")
	if !res1.OK {
		t.Errorf("expected OK=true for stream with first token, got OK=false, error=%q", res1.Error)
	}
	if res1.Note != "inconclusive (first token received)" {
		t.Errorf("Note = %q, want %q", res1.Note, "inconclusive (first token received)")
	}

	// 2. Probe that never receives an answer within the window -> ok = false with empty error string (inconclusive)
	rec2 := httptest.NewRecorder()
	rec2.WriteHeader(499)
	rec2.Write([]byte(``))

	res2 := readProbeResult(rec2, "")
	if res2.OK {
		t.Error("expected OK=false for empty aborted probe")
	}
	if res2.Error != "" {
		t.Errorf("expected empty error string for inconclusive probe, got %q", res2.Error)
	}
}

func TestReadProbeResult_ReviewerFindingsAndRequirements(t *testing.T) {
	t.Run("200 error envelope containing metadata-colon foo returns ok false with real error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		rec.Write([]byte(`{"error":{"message":"invalid metadata-colon foo"}}`))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK to be false for 200 error envelope")
		}
		if res.Error == "" {
			t.Error("expected non-empty error for 200 error envelope")
		}
		if res.Note != "" {
			t.Errorf("expected empty note, got %q", res.Note)
		}
	})

	t.Run("200 stream with only DONE returns ok false", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		rec.Write([]byte(`data: [DONE]`))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK to be false for stream with only [DONE]")
		}
		if res.Note != "" {
			t.Errorf("expected empty note, got %q", res.Note)
		}
	})

	t.Run("503, 429 and 401 with empty bodies keep err HTTP status", func(t *testing.T) {
		for _, code := range []int{503, 429, 401} {
			rec := httptest.NewRecorder()
			rec.WriteHeader(code)
			rec.Write([]byte(``))

			res := readProbeResult(rec, "")
			if res.OK {
				t.Errorf("code %d: expected OK to be false", code)
			}
			if !strings.Contains(res.Error, "HTTP ") {
				t.Errorf("code %d: expected error to keep HTTP status text, got %q", code, res.Error)
			}
		}
	})

	t.Run("mid-stream timeout stays inconclusive with ok false and empty err", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(499)
		rec.Write([]byte(``))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK to be false")
		}
		if res.Error != "" {
			t.Errorf("expected empty error, got %q", res.Error)
		}
	})

	t.Run("completed healthy 200 stays ok true and never labelled inconclusive", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		rec.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))

		res := readProbeResult(rec, "")
		if !res.OK {
			t.Errorf("expected OK=true for healthy 200, got error=%q", res.Error)
		}
		if res.Note != "" {
			t.Errorf("expected empty note for healthy 200, got %q", res.Note)
		}
	})
}

func TestReadProbeResult_HTMLErrorBodies(t *testing.T) {
	t.Run("429 Cloudflare challenge page is summarized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusTooManyRequests)
		rec.Write([]byte(`<!DOCTYPE html><html><head><title>Attention Required! | Cloudflare</title></head><body>cloudflare WAF block</body></html>`))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK=false for Cloudflare 429")
		}
		if !strings.Contains(res.Error, "Cloudflare WAF challenge (Attention Required!)") {
			t.Errorf("expected Cloudflare summary, got %q", res.Error)
		}
		if !strings.Contains(res.Error, "HTTP 429") {
			t.Errorf("expected HTTP 429 prefix, got %q", res.Error)
		}
	})

	t.Run("502 with title uses the title text", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusBadGateway)
		rec.Write([]byte(`<html><head><title>502 Bad Gateway</title></head><body><h1>502 Bad Gateway</h1><p>The server returned an invalid response.</p></body></html>`))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK=false for 502")
		}
		if !strings.Contains(res.Error, "502 Bad Gateway") {
			t.Errorf("expected title text '502 Bad Gateway', got %q", res.Error)
		}
		if strings.Contains(res.Error, "<h1>") || strings.Contains(res.Error, "</body>") {
			t.Errorf("expected HTML tags stripped, got %q", res.Error)
		}
	})

	t.Run("bare HTML without title is stripped to plain text", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusInternalServerError)
		rec.Write([]byte("<!DOCTYPE html><html><body><div>upstream error</div></body></html>"))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK=false for bare HTML 500")
		}
		if !strings.Contains(res.Error, "upstream error") {
			t.Errorf("expected stripped text 'upstream error', got %q", res.Error)
		}
		if strings.Contains(res.Error, "<") || strings.Contains(res.Error, ">") {
			t.Errorf("expected no angle brackets, got %q", res.Error)
		}
	})

	t.Run("long HTML title is truncated at probeTextLimit", func(t *testing.T) {
		longTitle := strings.Repeat("x", 300)
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusBadGateway)
		rec.Write([]byte("<html><head><title>" + longTitle + "</title></head><body></body></html>"))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK=false for long-title 502")
		}
		if !strings.HasPrefix(res.Error, "HTTP 502:") {
			t.Errorf("expected HTTP 502 prefix, got %q", res.Error)
		}
		detail := strings.TrimPrefix(res.Error, "HTTP 502: ")
		if !strings.HasSuffix(detail, "…") {
			t.Errorf("expected truncated detail to end with ellipsis, got %q", detail)
		}
		body := strings.TrimSuffix(detail, "…")
		if len(body) > probeTextLimit {
			t.Errorf("detail length %d exceeds limit %d", len(body), probeTextLimit)
		}
	})

	t.Run("non-HTML error body passes through unchanged", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusBadGateway)
		rec.Write([]byte(`{"error":{"message":"upstream quota exceeded"}}`))

		res := readProbeResult(rec, "")
		if res.OK {
			t.Error("expected OK=false")
		}
		if res.Error != "HTTP 502: upstream quota exceeded" {
			t.Errorf("expected JSON error passed through, got %q", res.Error)
		}
	})
}
