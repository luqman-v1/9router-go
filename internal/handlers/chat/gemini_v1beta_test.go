package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The action is the last colon segment of the path suffix; a model id never
// contains one, and both the one-segment and two-segment forms are accepted.
func TestGeminiPathAction(t *testing.T) {
	cases := []struct {
		suffix string
		model  string
		stream bool
		ok     bool
	}{
		{"deepseek/deepseek-chat:generateContent", "deepseek/deepseek-chat", false, true},
		{"deepseek/deepseek-chat:streamGenerateContent", "deepseek/deepseek-chat", true, true},
		{"gemini-4-argon:generateContent", "gemini-4-argon", false, true},
		{"models/deepseek/deepseek-chat:generateContent", "deepseek/deepseek-chat", false, true},
		{"deepseek/deepseek-chat", "", false, false},
		{"deepseek/deepseek-chat:countTokens", "", false, false},
		{"", "", false, false},
	}
	for _, tc := range cases {
		model, stream, ok := GeminiPathAction(tc.suffix)
		if ok != tc.ok || model != tc.model || stream != tc.stream {
			t.Errorf("GeminiPathAction(%q) = (%q,%v,%v), want (%q,%v,%v)",
				tc.suffix, model, stream, ok, tc.model, tc.stream, tc.ok)
		}
	}
}

func TestGeminiStreamWriter_PassThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := newGeminiStreamWriter(rec, "deepseek/deepseek-chat")

	chunk1 := []byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek/deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello \"}}]}\n\n")
	chunk2 := []byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek/deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world!\"}}]}\n\n")
	chunkDone := []byte("data: [DONE]\n\n")

	// Write chunk 1 in two slices to test boundary handling
	if _, err := sw.Write(chunk1[:20]); err != nil {
		t.Fatalf("Write part 1: %v", err)
	}
	if _, err := sw.Write(chunk1[20:]); err != nil {
		t.Fatalf("Write part 2: %v", err)
	}
	if _, err := sw.Write(chunk2); err != nil {
		t.Fatalf("Write chunk 2: %v", err)
	}
	if _, err := sw.Write(chunkDone); err != nil {
		t.Fatalf("Write chunkDone: %v", err)
	}
	sw.finish()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Hello ") {
		t.Errorf("body missing Hello: %s", body)
	}
	if !strings.Contains(body, "world!") {
		t.Errorf("body missing world!: %s", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Errorf("Gemini stream must not contain [DONE]: %s", body)
	}
}

func TestGeminiStreamWriter_ErrorBuffering(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := newGeminiStreamWriter(rec, "deepseek/deepseek-chat")

	sw.WriteHeader(http.StatusBadRequest)
	if _, err := sw.Write([]byte(`{"error":{"message":"bad request"}}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	sw.finish()

	if sw.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", sw.status)
	}
	if !strings.Contains(sw.errBuf.String(), "bad request") {
		t.Errorf("errBuf = %q, want bad request", sw.errBuf.String())
	}
	// Headers must not be sent on client w yet when error occurs
	if rec.Code != http.StatusOK && rec.Code != 0 {
		// rec default code is 200 if not called, or 0
		if rec.Header().Get("Content-Type") == "text/event-stream" {
			t.Errorf("event-stream headers must not be sent on error")
		}
	}
}
