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

// A complete frame that carries no prose — an Anthropic thinking or
// signature_delta block — is content the gate counts, but the banner peeker
// has no text to classify. It must be relayed, not held: holding it starves
// the first-output gate and times out a healthy stream.
func TestFrameParses(t *testing.T) {
	tests := []struct {
		name  string
		chunk string
		want  bool
	}{
		{
			name:  "redacted_thinking content_block_start",
			chunk: "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"redacted_data\"}}\n\n",
			want:  true,
		},
		{
			name:  "signature_delta",
			chunk: "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_xyz\"}}\n\n",
			want:  true,
		},
		{
			name:  "thinking content_block_start with empty thinking",
			chunk: "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
			want:  true,
		},
		{
			name:  "text_delta carries content",
			chunk: "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n",
			want:  true,
		},
		{
			name:  "split mid-JSON does not parse",
			chunk: "data: {\"choices\":[{\"delta\":{\"content\":\"Agent exec",
			want:  false,
		},
		{
			name:  "no data line",
			chunk: "event: ping\n\n",
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FrameParses([]byte(tt.chunk)); got != tt.want {
				t.Errorf("FrameParses() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The healthy-stream path a thinking stream takes: structural frames relay,
// the following text frame commits, and nothing is held.
func TestPeekerRelaysThinkingStream(t *testing.T) {
	rec := httptest.NewRecorder()
	p := NewStreamContentPeeker(rec)

	structural := []string{
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\"}}\n\n",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"step one\"}}\n\n",
	}
	for _, c := range structural {
		if _, err := p.Write([]byte(c)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if p.heldContent {
		t.Error("a structural frame must not be held")
	}
	if !p.committed {
		t.Error("a structural frame must commit the stream")
	}

	text := "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n"
	if _, err := p.Write([]byte(text)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := p.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "done") {
		t.Errorf("expected the committed response to contain the answer, got %q", rec.Body.String())
	}
}

// The reviewer's SPLITEARLY probe: a 245-byte OpenAI frame split at byte 60,
// which lands inside the JSON header — before the `content` field. The first
// half matches no content heuristic at all, so a hold that depends on
// HasSSEContent never engages and the banner is relayed verbatim. The split
// point is the whole point of the case, so it is pinned explicitly.
func TestPeekerHoldsSplitBeforeContentField(t *testing.T) {
	frame := "data: {\"id\":\"chatcmpl-0123456789abcdef\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gemini-3-pro\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Agent execution terminated due to error: upstream 502\"},\"finish_reason\":null}]}\n\n"
	const split = 60
	if len(frame) <= split {
		t.Fatalf("fixture is %d bytes, the split needs more than %d", len(frame), split)
	}

	rec := httptest.NewRecorder()
	p := NewStreamContentPeeker(rec)

	first := frame[:split]
	second := frame[split:]
	// The premise: the first half is invisible to the content heuristic and
	// does not parse. If either changes, this test no longer exercises the bug.
	if HasSSEContent([]byte(first)) {
		t.Log("note: the first half now matches the content heuristic; the split still must be held")
	}
	if FrameParses([]byte(first)) {
		t.Fatal("fixture split must land inside the JSON, not after it")
	}

	if _, err := p.Write([]byte(first)); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("nothing may be relayed before the frame completes, wrote %q", rec.Body.String())
	}
	if _, err := p.Write([]byte(second)); err != nil {
		t.Fatalf("write second: %v", err)
	}

	err := p.Finish()
	if err == nil {
		t.Fatal("a banner turn split before its content field must fail at Finish so the caller can retry")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("the banner must never reach the client, wrote %q", rec.Body.String())
	}
}

// The same split on a REAL answer must be relayed byte for byte: the hold is
// about classification, not about dropping bytes.
func TestPeekerRelaysSplitRealAnswer(t *testing.T) {
	frame := "data: {\"id\":\"chatcmpl-0123456789abcdef\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gemini-3-pro\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Here is a real answer that runs long enough to outgrow any banner shape.\"},\"finish_reason\":null}]}\n\n"
	const split = 60

	rec := httptest.NewRecorder()
	p := NewStreamContentPeeker(rec)
	if _, err := p.Write([]byte(frame[:split])); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if _, err := p.Write([]byte(frame[split:])); err != nil {
		t.Fatalf("write second: %v", err)
	}
	if err := p.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := rec.Body.String(); got != frame {
		t.Errorf("split answer must be relayed byte for byte\ngot:  %q\nwant: %q", got, frame)
	}
}
