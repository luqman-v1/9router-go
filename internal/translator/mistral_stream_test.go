package translator

import (
	"strings"
	"testing"
)

func TestMistralArrayContentDeltaStream(t *testing.T) {
	session := "mistral-array-delta-1"
	defer ClearStreamState(session)

	chunk := []byte(`{"id":"chatcmpl-mistral-array","model":"mistral-large-latest","choices":[{"index":0,"delta":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"let me think"}]},{"type":"text","text":"hello world"}]},"finish_reason":null}]}`)
	out, err := TranslateOpenAIToClaudeStreamSession(session, chunk)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"type":"thinking_delta"`) {
		t.Fatalf("expected thinking_delta, got: %s", got)
	}
	if !strings.Contains(got, `"thinking":"let me think"`) {
		t.Fatalf("expected thinking text, got: %s", got)
	}
	if !strings.Contains(got, `"type":"text_delta"`) {
		t.Fatalf("expected text_delta, got: %s", got)
	}
	if !strings.Contains(got, `"text":"hello world"`) {
		t.Fatalf("expected visible text, got: %s", got)
	}
	if strings.Contains(got, `[{"type"`) {
		t.Fatalf("raw array leaked into output: %s", got)
	}
}
