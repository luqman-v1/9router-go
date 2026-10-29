package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// A conversation whose image lives in an OLD turn while the current turn is
// text-only is the shape that reached a text-only custom openai-compatible
// provider verbatim and died on 400 `type has unsupported value "image_url"`,
// with the vision pool never consulted (the trailing-only scan saw no
// vision). With no candidate able to serve the media, the old image must come
// out of the forwarded history.
func TestStripLegacyMediaNoCandidateServes(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": [
				{"type": "text", "text": "what is in this picture?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}
			]},
			{"role": "assistant", "content": "a cat"},
			{"role": "user", "content": "thanks, now write me a poem"}
		]
	}`)

	cleaned := maybeStripLegacyMediaBytes(body, []string{"cust/text-only-model"})

	var got map[string]any
	if err := json.Unmarshal(cleaned, &got); err != nil {
		t.Fatalf("cleaned body is not JSON: %v", err)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("message count changed: %d", len(msgs))
	}
	if old, ok := msgs[1].(map[string]any)["content"].([]any); ok {
		for _, b := range old {
			bm := b.(map[string]any)
			if bm["type"] == "image_url" {
				t.Fatalf("old-turn image_url survived the strip: %v", bm)
			}
		}
	} else {
		t.Fatalf("old-turn content is no longer a block array")
	}
	// The current turn is untouched — the trailing run keeps its media even
	// when no candidate serves it, because that media is the actual question.
	trailing := msgs[3].(map[string]any)
	if s, _ := trailing["content"].(string); s != "thanks, now write me a poem" {
		t.Fatalf("trailing turn was modified: %v", trailing)
	}
	if s, _ := msgs[0].(map[string]any)["content"].(string); s != "you are helpful" {
		t.Fatalf("system message was modified: %v", msgs[0])
	}
}

// A vision-capable candidate keeps the history verbatim: the reorder logic
// floats it forward, and dropping old media would cost context fidelity for
// nothing.
func TestStripLegacyMediaKeptWhenCandidateServes(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "see this?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}
			]},
			{"role": "assistant", "content": "yes"},
			{"role": "user", "content": "and this one?"}
		]
	}`)

	cleaned := maybeStripLegacyMediaBytes(body, []string{"openai/gpt-4o"})
	if string(cleaned) != string(body) {
		t.Fatalf("body was rewritten although a vision-capable candidate exists")
	}
}

// An image in the CURRENT turn is a real question, never a strip target.
func TestStripLegacyMediaKeepsTrailingImage(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "and now?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}
			]}
		]
	}`)

	cleaned := maybeStripLegacyMediaBytes(body, []string{"cust/text-only-model"})
	if string(cleaned) != string(body) {
		t.Fatalf("trailing-run media was stripped: %s", cleaned)
	}
}

// The Ollama shape carries media as a message-level images array.
func TestStripLegacyMediaOllamaImagesArray(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "what is this?", "images": ["base64data"]},
			{"role": "assistant", "content": "a cat"},
			{"role": "user", "content": "ok good"}
		]
	}`)

	cleaned := maybeStripLegacyMediaBytes(body, []string{"cust/text-only-model"})
	s := string(cleaned)
	if strings.Contains(s, "images") || strings.Contains(s, "base64data") {
		t.Fatalf("ollama images array survived: %s", s)
	}
	if !strings.Contains(s, `"ok good"`) {
		t.Fatalf("trailing turn lost: %s", s)
	}
}

// Gemini contents carry media under parts, via inlineData.
func TestStripLegacyMediaGeminiParts(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [
				{"text": "what is this?"},
				{"inlineData": {"mimeType": "image/png", "data": "AAAA"}}
			]},
			{"role": "model", "parts": [{"text": "a cat"}]},
			{"role": "user", "parts": [{"text": "ok good"}]}
		]
	}`)

	cleaned := maybeStripLegacyMediaBytes(body, []string{"cust/text-only-model"})
	s := string(cleaned)
	if strings.Contains(s, "inlineData") {
		t.Fatalf("old-turn inlineData survived: %s", s)
	}
	if !strings.Contains(s, `"ok good"`) {
		t.Fatalf("trailing turn lost: %s", s)
	}
}

// The boundary walk must agree with trailingUserItems: the current user run
// may span several messages (text + media split across blocks), and all of
// them are exempt from the strip.
func TestTrailingRunStartSpansSplitTurn(t *testing.T) {
	arr := []any{
		map[string]any{"role": "user", "content": "old"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "part one"},
		map[string]any{"role": "user", "content": "part two"},
	}
	if got := trailingRunStart(arr); got != 2 {
		t.Fatalf("trailingRunStart = %d, want 2", got)
	}
}

// The safety net: a 400 naming an unsupported image_url part must classify as
// a model-fit error so the combo advances to its next member instead of
// surfacing the upstream 400 as terminal.
func TestVisionUnsupportedClassifiesAsModelFit(t *testing.T) {
	crown := []byte(`{"error":{"message":"forward to upstream: messages[302].content[1].type has unsupported value \"image_url\"","type":"upstream_error"}}`)
	if !providers.IsModelFitError(400, crown) {
		t.Fatalf("vision-unsupported 400 not classified as model fit")
	}
	plain := []byte(`{"error":{"message":"invalid api key"}}`)
	if providers.IsModelFitError(400, plain) {
		t.Fatalf("unrelated 400 classified as model fit")
	}
}
