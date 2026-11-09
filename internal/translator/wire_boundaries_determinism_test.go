package translator

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// boundaryRuns is how many identical calls each case makes. json/v2 does not
// order map members by default, so a seam that marshals a map without
// Deterministic(true) emits a different byte sequence within a couple of
// calls; twenty makes the assertion reliable without slowing the suite.
const boundaryRuns = 20

// The two values a correct translator cannot hold constant across calls: the
// nanosecond message id and the wall-clock `created` stamp. They are
// normalized before comparison so the test measures member ordering, which is
// the property under test, and nothing else.
var (
	chatcmplIDPattern   = regexp.MustCompile(`"id":"chatcmpl-\d+"`)
	createdFieldPattern = regexp.MustCompile(`"created":\d+`)
)

func normalizeVolatileTimestamps(s string) string {
	s = chatcmplIDPattern.ReplaceAllString(s, `"id":"chatcmpl-fixed"`)
	return createdFieldPattern.ReplaceAllString(s, `"created":0`)
}

// TestTranslatorBoundariesAreDeterministic pins every map-to-bytes boundary
// the translator owns, one case per seam. Each case asserts its rewrite
// actually ran — a seam that returns the input untouched would compare equal
// on every call and pass the loop vacuously — and then requires twenty
// identical calls to serialize identically. Removing
// json.Deterministic(true) from a case's seam fails that case.
func TestTranslatorBoundariesAreDeterministic(t *testing.T) {
	tests := []struct {
		name string
		// call performs one identical request and returns the wire bytes.
		// It fails the subtest when its own rewrite seam was not reached.
		call func(t *testing.T) []byte
		// normalize maps a result to its comparable form, for the seams whose
		// output carries the clock.
		normalize func(string) string
		// want is a fragment the result must contain.
		want string
	}{
		{
			// A Claude tool_choice object becomes a nested OpenAI map, which
			// is the only map member of an otherwise struct-shaped request.
			name: "claude request translation",
			want: `"tool_choice"`,
			call: func(t *testing.T) []byte {
				out, err := TranslateClaudeToOpenAI([]byte(`{
					"model": "claude-sonnet-4-5",
					"max_tokens": 64,
					"stream": true,
					"system": "be brief",
					"messages": [{"role": "user", "content": "hi"}],
					"tools": [{"name": "get_weather", "input_schema": {"type": "object"}}],
					"tool_choice": {"type": "tool", "name": "get_weather"}
				}`))
				if err != nil {
					t.Fatalf("TranslateClaudeToOpenAI: %v", err)
				}
				return out
			},
		},
		{
			name: "claude passthrough sanitize",
			want: `"messages"`,
			call: func(t *testing.T) []byte {
				in := []byte(`{"model":"m","max_tokens":16,"system":[{"type":"text","text":"s"}],` +
					`"messages":[{"role":"user","content":[{"type":"text","text":"   "},{"type":"text","text":"hi"}]}]}`)
				out := SanitizeClaudePassthrough(in, false)
				if bytes.Equal(out, in) {
					t.Fatal("SanitizeClaudePassthrough returned the body unchanged: the marshal seam was not reached")
				}
				return out
			},
		},
		{
			name: "claude default tool type",
			want: `"custom"`,
			call: func(t *testing.T) []byte {
				in := []byte(`{"model":"m","max_tokens":16,"tools":[{"name":"t","input_schema":{"type":"object"}}],` +
					`"messages":[{"role":"user","content":"hi"}]}`)
				out := DefaultClaudeToolType(in)
				if bytes.Equal(out, in) {
					t.Fatal("DefaultClaudeToolType returned the body unchanged: the marshal seam was not reached")
				}
				return out
			},
		},
		{
			// The anchor path: markers are added, then the body is marshalled.
			name: "claude cache anchor",
			want: `"cache_control"`,
			call: func(t *testing.T) []byte {
				in := []byte(`{"model":"m","max_tokens":16,"system":[{"type":"text","text":"s"}],` +
					`"tools":[{"name":"t","input_schema":{"type":"object"}}],` +
					`"messages":[{"role":"user","content":"hi"}]}`)
				out := AnchorClaudeCache(in)
				if bytes.Equal(out, in) {
					t.Fatal("AnchorClaudeCache returned the body unchanged: the marshal seam was not reached")
				}
				return out
			},
		},
		{
			// The budget path inside AnchorClaudeCache: four surviving markers
			// hit cacheControlMarkerBudget, which caps and marshals early.
			name: "claude cache anchor at the marker budget",
			want: `"cache_control"`,
			call: func(t *testing.T) []byte {
				in := []byte(`{"model":"m","max_tokens":16,"system":[` +
					`{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},` +
					`{"type":"text","text":"b","cache_control":{"type":"ephemeral"}},` +
					`{"type":"text","text":"c","cache_control":{"type":"ephemeral"}},` +
					`{"type":"text","text":"d","cache_control":{"type":"ephemeral"}}],` +
					`"messages":[{"role":"user","content":"hi"}]}`)
				out := AnchorClaudeCache(in)
				if bytes.Equal(out, in) {
					t.Fatal("AnchorClaudeCache returned the body unchanged: the budget marshal seam was not reached")
				}
				return out
			},
		},
		{
			name:      "gemini response to openai",
			want:      `"choices"`,
			normalize: normalizeVolatileTimestamps,
			call: func(t *testing.T) []byte {
				out, _, err := TranslateGeminiResponseToOpenAI([]byte(`{
					"candidates": [{"content": {"parts": [{"text": "hello"}, {"text": " world"}]}, "finishReason": "STOP"}],
					"usageMetadata": {"promptTokenCount": 9, "candidatesTokenCount": 4}
				}`))
				if err != nil {
					t.Fatalf("TranslateGeminiResponseToOpenAI: %v", err)
				}
				return out
			},
		},
		{
			name:      "gemini stream chunk to openai",
			want:      `"chat.completion.chunk"`,
			normalize: normalizeVolatileTimestamps,
			call: func(t *testing.T) []byte {
				state := &GeminiStreamState{MessageId: "chatcmpl-fixed", Model: "gemini-2.5-pro"}
				out, err := TranslateGeminiChunkToOpenAI([]byte(`{
					"candidates": [{"content": {"parts": [{"text": "hi"}]}, "finishReason": "STOP"}],
					"usageMetadata": {"promptTokenCount": 2, "candidatesTokenCount": 1}
				}`), state)
				if err != nil {
					t.Fatalf("TranslateGeminiChunkToOpenAI: %v", err)
				}
				if len(out) == 0 {
					t.Fatal("TranslateGeminiChunkToOpenAI returned no SSE payload: the marshal seam was not reached")
				}
				return out
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalize := tt.normalize
			if normalize == nil {
				normalize = func(s string) string { return s }
			}
			first := normalize(string(tt.call(t)))
			if !strings.Contains(first, tt.want) {
				t.Fatalf("result lacks %s: %s", tt.want, first)
			}
			for i := 2; i <= boundaryRuns; i++ {
				got := normalize(string(tt.call(t)))
				if got != first {
					t.Fatalf("call %d serialized differently from call 1:\n call 1: %s\n call %d: %s",
						i, first, i, got)
				}
			}
		})
	}
}
