package proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsErrorBannerText(t *testing.T) {
	banners := []string{
		"Agent execution terminated due to error: upstream 502",
		"Agent terminated",
		"Request failed: connection reset",
		"Authentication failed for this provider",
		"Quota exceeded for this model",
		"Rate limit reached, try again later",
		"Content policy violation",
		"502 Bad Gateway",
		"Service unavailable",
		"Internal server error",
		"The model is currently overloaded",
		"An error occurred while generating the response",
	}
	for _, text := range banners {
		if !IsErrorBannerText(text) {
			t.Errorf("IsErrorBannerText(%q) = false, want true", text)
		}
	}
}

func TestIsErrorBannerTextRejectsRealAnswers(t *testing.T) {
	answers := []string{
		"",
		"Errors are normal in distributed systems, and here is why.",
		"The timeout you configured is 30 seconds; increase it to 60.",
		"Here is the code that produces the error banner you asked about.",
		"Sure — the quota exceeded path is handled on line 42 of fallback.go.",
	}
	for _, text := range answers {
		if IsErrorBannerText(text) {
			t.Errorf("IsErrorBannerText(%q) = true, a real answer must never be treated as a banner", text)
		}
	}
}

func TestIsErrorBannerTextRejectsLongText(t *testing.T) {
	// A fault phrase followed by a long answer is not a banner: the match must
	// cover the whole text.
	long := "Agent execution terminated due to error: " + strings.Repeat("context ", 400)
	if IsErrorBannerText(long) {
		t.Error("a banner phrase followed by a long text must not classify as a banner")
	}
}

func TestIsBannerSuspectHoldsPrefixes(t *testing.T) {
	// The first frame of a split banner must not commit the stream.
	for _, prefix := range []string{"Agent", "Agent execution", "Request fai", "Quota", "502"} {
		if !IsBannerSuspect(prefix) {
			t.Errorf("IsBannerSuspect(%q) = false, want a held-open prefix", prefix)
		}
	}
	// A real answer's first word commits immediately.
	for _, text := range []string{"Here", "The", "Sure", "Errors are"} {
		if IsBannerSuspect(text) {
			t.Errorf("IsBannerSuspect(%q) = true, want an ordinary answer prefix to commit", text)
		}
	}
}

func TestExtractSSEText(t *testing.T) {
	tests := []struct {
		name  string
		chunk string
		want  string
	}{
		{
			name:  "openai delta",
			chunk: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
			want:  "hello",
		},
		{
			name:  "openai text parts",
			chunk: "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}]}\n\n",
			want:  "hi",
		},
		{
			name:  "anthropic content block delta",
			chunk: "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"claude\"}}\n\n",
			want:  "claude",
		},
		{
			name:  "responses delta",
			chunk: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"resp\"}\n\n",
			want:  "resp",
		},
		{
			name:  "done and comments carry nothing",
			chunk: "data: [DONE]\n\n: keep-alive\n\n",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractSSEText([]byte(tt.chunk)); got != tt.want {
				t.Errorf("ExtractSSEText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPeekRefusesBannerOnlyStream(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Agent execution \"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"terminated due to error\"}}]}\n\n" +
		"data: [DONE]\n\n"

	_, _, err := PeekFirstStreamEvent(context.Background(), strings.NewReader(body), 0)
	if err == nil {
		t.Fatal("a banner-only stream must be refused so the combo falls through")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %T, want a retryable UpstreamError", err)
	}
	if ue.StatusCode != 502 {
		t.Errorf("status = %d, want 502 so the fallback layer retries", ue.StatusCode)
	}
}

func TestPeekAcceptsRealAnswerAfterBannerPrefix(t *testing.T) {
	// The stream opens with a word that looks like a banner lead-in and then
	// turns into a real answer: it must be relayed, not refused.
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Error\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" handling starts with a retry, and that is the answer.\"}}]}\n\n" +
		"data: [DONE]\n\n"

	peeked, replay, err := PeekFirstStreamEvent(context.Background(), strings.NewReader(body), 0)
	if err != nil {
		t.Fatalf("a real answer must not be refused: %v", err)
	}
	if len(peeked) == 0 {
		t.Error("the peeked bytes must be replayable")
	}
	got, _ := readAllString(replay)
	if got != body {
		t.Errorf("replay = %q, want the original bytes", got)
	}
}

// A banner-only turn reached through the streaming writer must fail at Finish
// instead of being committed as content.
func TestStreamContentPeekerFailsBannerOnlyTurn(t *testing.T) {
	rec := httptest.NewRecorder()
	p := NewStreamContentPeeker(rec)

	frames := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Agent execution \"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"terminated due to error\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	for _, f := range frames {
		if _, err := p.Write([]byte(f)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := p.Finish(); err == nil {
		t.Fatal("a banner-only turn must fail at Finish so the caller can retry")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("nothing may be committed for a banner turn, wrote %q", rec.Body.String())
	}
}

func TestStreamContentPeekerCommitsRealAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	p := NewStreamContentPeeker(rec)

	frame := "data: {\"choices\":[{\"delta\":{\"content\":\"Here is the fix: read the error.\"}}]}\n\n"
	if _, err := p.Write([]byte(frame)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := p.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if rec.Body.String() != frame {
		t.Errorf("body = %q, want the frame relayed byte for byte", rec.Body.String())
	}
}

func readAllString(r interface{ Read([]byte) (int, error) }) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			if err.Error() == "EOF" {
				return sb.String(), nil
			}
			return sb.String(), err
		}
	}
}
