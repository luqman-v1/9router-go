package semanticcache

import (
	"testing"
	"time"

	"9router/proxy/internal/translator"
)

func imageOnlyRequest(url string) *translator.OpenAIRequest {
	return &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: []translator.OpenAIContentBlock{
				{Type: "image_url", ImageUrl: &translator.OpenAIImageUrl{URL: url}},
			}},
		},
	}
}

// Two multimodal requests that differ only in their image bytes must not share
// a cache key: hashing text alone writes role + separator and no payload, so
// every image-only prompt collapsed onto one entry and served another user's
// image response.
func TestBuildCacheKey_SeparatesNonTextContent(t *testing.T) {
	a := BuildCacheKey("", imageOnlyRequest("data:image/png;base64,AAAAAAAA"))
	b := BuildCacheKey("", imageOnlyRequest("data:image/png;base64,BBBBBBBB"))

	if a == b {
		t.Fatal("different image payloads produced the same cache key")
	}
	if a != BuildCacheKey("", imageOnlyRequest("data:image/png;base64,AAAAAAAA")) {
		t.Fatal("cache key is not deterministic for identical input")
	}
}

func TestBuildCacheKey_SeparatesFilePayloads(t *testing.T) {
	withFile := func(data string) *translator.OpenAIRequest {
		return &translator.OpenAIRequest{
			Model: "gpt-4o",
			Messages: []translator.OpenAIMessage{
				{Role: "user", Content: []translator.OpenAIContentBlock{
					{Type: "file", File: &translator.OpenAIFile{FileData: data}},
				}},
			},
		}
	}

	if BuildCacheKey("", withFile("AAA")) == BuildCacheKey("", withFile("BBB")) {
		t.Fatal("different file payloads produced the same cache key")
	}
}

// The generic []any content path carries the same payloads as the typed one,
// and is what an unmarshalled request body actually looks like.
func TestBuildCacheKey_SeparatesUntypedImagePayloads(t *testing.T) {
	build := func(url string) *translator.OpenAIRequest {
		return &translator.OpenAIRequest{
			Model: "gpt-4o",
			Messages: []translator.OpenAIMessage{
				{Role: "user", Content: []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}},
				}},
			},
		}
	}

	if BuildCacheKey("", build("data:image/png;base64,AAAAAAAA")) == BuildCacheKey("", build("data:image/png;base64,BBBBBBBB")) {
		t.Fatal("untyped content: different image payloads produced the same cache key")
	}
}

// A parameter that changes the answer must change the key. Two requests whose
// prompts are byte-identical but that pin different max_tokens cannot share a
// cached body: the second caller would silently receive the first one's shorter
// completion.
func TestBuildCacheKey_SeparatesResponseShapingParameters(t *testing.T) {
	base := func() *translator.OpenAIRequest {
		return &translator.OpenAIRequest{
			Model:    "gpt-4o",
			Messages: []translator.OpenAIMessage{{Role: "user", Content: "hello"}},
		}
	}
	withInt := func(v int) *translator.OpenAIRequest {
		r := base()
		r.MaxTokens = &v
		return r
	}

	tests := []struct {
		name string
		a, b *translator.OpenAIRequest
	}{
		{"max_tokens differs", withInt(16), withInt(4096)},
		{"max_tokens set versus omitted", withInt(16), base()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if BuildCacheKey("", tt.a) == BuildCacheKey("", tt.b) {
				t.Fatal("requests that produce different completions share a cache key")
			}
		})
	}

	maxCompletion := func(v int) *translator.OpenAIRequest {
		r := base()
		r.MaxCompletionTokens = &v
		return r
	}
	if BuildCacheKey("", maxCompletion(16)) == BuildCacheKey("", maxCompletion(4096)) {
		t.Error("max_completion_tokens does not reach the cache key")
	}

	effort := func(e string) *translator.OpenAIRequest {
		r := base()
		r.ReasoningEffort = e
		return r
	}
	if BuildCacheKey("", effort("low")) == BuildCacheKey("", effort("high")) {
		t.Error("reasoning_effort does not reach the cache key")
	}

	parallel := func(v bool) *translator.OpenAIRequest {
		r := base()
		r.ParallelToolCalls = &v
		return r
	}
	if BuildCacheKey("", parallel(true)) == BuildCacheKey("", parallel(false)) {
		t.Error("parallel_tool_calls does not reach the cache key")
	}

	// A request that changes nothing must keep producing the same key, or the
	// cache would never hit.
	if BuildCacheKey("", base()) != BuildCacheKey("", base()) {
		t.Error("cache key is not stable for identical requests")
	}
}

// TTL expiry is evaluated in SQL as a string comparison on storedAt. Plain
// RFC3339 truncates to whole seconds, so a row written in the same second as the
// cutoff compares equal, never sorts before it, and is never deleted — the
// janitor ran while the expired row stayed on disk forever.
func TestStoredAtFormat_SecondPrecisionWouldNeverExpire(t *testing.T) {
	old := time.Now().Add(-50 * time.Millisecond)
	const ttl = 20 * time.Millisecond

	// The bug this guards: second-precision truncation collapses both stamps
	// onto the same string, so `storedAt < cutoff` is false.
	coarseStored := old.UTC().Format(time.RFC3339)
	coarseCutoff := time.Now().Add(-ttl).UTC().Format(time.RFC3339)
	if coarseStored < coarseCutoff {
		t.Skip("wall clock advanced across a second boundary; the coarse case is unobservable here")
	}

	// The shipped format must separate them.
	stored := formatStoredAt(old)
	cutoff := formatStoredAt(time.Now().Add(-ttl))
	if !(stored < cutoff) {
		t.Fatalf("expired entry not pruned: storedAt=%q cutoff=%q", stored, cutoff)
	}
}

func TestParseStoredAt_ReadsBothFormats(t *testing.T) {
	want := time.Date(2026, 10, 7, 15, 4, 5, 123456789, time.UTC)

	got, ok := parseStoredAt(formatStoredAt(want))
	if !ok {
		t.Fatal("fixed-width format did not parse")
	}
	if !got.Equal(want) {
		t.Fatalf("round trip = %s, want %s", got, want)
	}

	// Rows written by older builds are second-precision RFC3339 and must still
	// load, or a restart silently empties the cache.
	legacy, ok := parseStoredAt("2026-10-07T15:04:05Z")
	if !ok {
		t.Fatal("legacy second-precision value did not parse")
	}
	if !legacy.Equal(want.Truncate(time.Second)) {
		t.Fatalf("legacy parse = %s, want %s", legacy, want.Truncate(time.Second))
	}

	if _, ok := parseStoredAt("not a timestamp"); ok {
		t.Fatal("expected a garbage storedAt to be rejected")
	}
}
