package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamContentPeeker_EmitsContentAndCommits(t *testing.T) {
	rec := httptest.NewRecorder()
	peeker := NewStreamContentPeeker(rec)
	peeker.Header().Set("Content-Type", "text/event-stream")

	// First chunk: empty role block
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	if rec.Code != http.StatusOK && rec.Body.Len() > 0 {
		t.Fatalf("peeker should not have committed yet")
	}

	// Second chunk: real content
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	if rec.Body.Len() == 0 {
		t.Fatalf("peeker should have committed once content arrived")
	}

	err := peeker.Finish()
	if err != nil {
		t.Fatalf("unexpected error on finish: %v", err)
	}
}

func TestStreamContentPeeker_ZeroContentReturnsError(t *testing.T) {
	rec := httptest.NewRecorder()
	peeker := NewStreamContentPeeker(rec)

	// Stream that ends with role + finish_reason only (no content)
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
	_, _ = peeker.Write([]byte("data: [DONE]\n\n"))

	err := peeker.Finish()
	if !errors.Is(err, ErrStreamingZeroContent) {
		t.Fatalf("expected ErrStreamingZeroContent, got %v", err)
	}
	if rec.Body.Len() > 0 {
		t.Fatalf("zero-content stream should not have committed any body to underlying recorder")
	}
}

func TestStreamContentPeeker_ReasoningContentCommits(t *testing.T) {
	tests := []struct {
		name         string
		chunks       []string
		expectCommit bool
		expectErr    error
	}{
		{
			name:         "OpenAI reasoning_content commits",
			chunks:       []string{"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\n\n"},
			expectCommit: true,
			expectErr:    nil,
		},
		{
			name:         "OpenAI reasoning alias commits",
			chunks:       []string{"data: {\"choices\":[{\"delta\":{\"reasoning\":\"thinking...\"}}]}\n\n"},
			expectCommit: true,
			expectErr:    nil,
		},
		{
			name:         "OpenAI empty reasoning_content does not commit",
			chunks:       []string{"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"\"}}]}\n\n", "data: [DONE]\n\n"},
			expectCommit: false,
			expectErr:    ErrStreamingZeroContent,
		},
		{
			name:         "Anthropic thinking_delta commits",
			chunks:       []string{"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"let me think\"}}\n\n"},
			expectCommit: true,
			expectErr:    nil,
		},
		{
			name:         "Anthropic empty thinking_delta does not commit",
			chunks:       []string{"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"\"}}\n\n", "data: [DONE]\n\n"},
			expectCommit: false,
			expectErr:    ErrStreamingZeroContent,
		},
		{
			name:         "Responses reasoning_text.delta commits",
			chunks:       []string{"data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\n"},
			expectCommit: true,
			expectErr:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			peeker := NewStreamContentPeeker(rec)
			peeker.Header().Set("Content-Type", "text/event-stream")

			for _, chunk := range tc.chunks {
				_, _ = peeker.Write([]byte(chunk))
			}

			err := peeker.Finish()
			if !errors.Is(err, tc.expectErr) {
				t.Fatalf("expected error %v, got %v", tc.expectErr, err)
			}
			if tc.expectCommit && rec.Body.Len() == 0 {
				t.Fatalf("expected stream to commit and write body, but recorder body is empty")
			}
			if !tc.expectCommit && rec.Body.Len() > 0 {
				t.Fatalf("expected stream not to commit, but got recorder body: %s", rec.Body.String())
			}
		})
	}
}
