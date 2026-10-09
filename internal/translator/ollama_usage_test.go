package translator

import "testing"

// Ollama's native response shape is the one place cached tokens are published
// under a provider-specific key. Without reading it, every Ollama turn records
// a cache hit rate of zero and the cache analytics under-report savings.

// TestParseOllamaUsage covers the counters as Ollama publishes them: prompt_eval
// and eval counts, plus prompt_eval_cached_count as the cache-read subset.
func TestParseOllamaUsage(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantNil    bool
		wantPrompt int
		wantDone   int
		wantCached int
	}{
		{
			name:       "final chunk with a cache hit",
			in:         `{"done":true,"response":"hi","prompt_eval_count":100,"eval_count":20,"prompt_eval_cached_count":64}`,
			wantPrompt: 100,
			wantDone:   20,
			wantCached: 64,
		},
		{
			name:       "no cache hit",
			in:         `{"done":true,"prompt_eval_count":100,"eval_count":20,"prompt_eval_cached_count":0}`,
			wantPrompt: 100,
			wantDone:   20,
		},
		{
			name:       "cached field absent entirely",
			in:         `{"done":true,"prompt_eval_count":100,"eval_count":20}`,
			wantPrompt: 100,
			wantDone:   20,
		},
		{
			// An intermediate NDJSON line with no counters at all: the flag
			// alone already rules it out, so this guards the cheap path.
			name:    "an intermediate chunk is not usage",
			in:      `{"done":false,"response":"partial"}`,
			wantNil: true,
		},
		{
			// An intermediate NDJSON line is not the completion. This one
			// carries partial counters on purpose: without the `done` check it
			// would be indistinguishable from a real usage record, and every
			// chunk of a streamed turn would be logged.
			name:    "an intermediate chunk with counters is not usage",
			in:      `{"done":false,"prompt_eval_count":100,"eval_count":5}`,
			wantNil: true,
		},
		{
			// `done` ends a turn that published no counters. Inventing a
			// zero record would write a usage row for a request that never
			// billed anything.
			name:    "done with no counters is not usage",
			in:      `{"done":true,"error":"model not found"}`,
			wantNil: true,
		},
		{
			// A false positive here would fabricate a usage record for an
			// OpenAI-shaped body, which is the common case this parser shares
			// a code path with.
			name:    "an OpenAI body is not ollama usage",
			in:      `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			wantNil: true,
		},
		{
			name:    "malformed body is not usage",
			in:      `not json`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseOllamaUsage([]byte(tt.in))
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ParseOllamaUsage() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("ParseOllamaUsage() = nil, want usage")
			}
			if got.PromptTokens != tt.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, tt.wantPrompt)
			}
			if got.CompletionTokens != tt.wantDone {
				t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, tt.wantDone)
			}
			if got.CachedTokens != tt.wantCached {
				t.Errorf("CachedTokens = %d, want %d", got.CachedTokens, tt.wantCached)
			}
		})
	}
}

// TestParseOllamaUsage_CachedIsNotSubtracted pins the convention that makes this
// number usable: Ollama reports prompt_eval_count CACHE-INCLUSIVE, exactly as
// OpenAI and Gemini do. Subtracting the cache read here would make the recorded
// prompt tokens disagree with what the provider billed.
func TestParseOllamaUsage_CachedIsNotSubtracted(t *testing.T) {
	got := ParseOllamaUsage([]byte(`{"done":true,"prompt_eval_count":1000,"eval_count":50,"prompt_eval_cached_count":900}`))
	if got == nil {
		t.Fatal("ParseOllamaUsage() = nil, want usage")
	}
	if got.PromptTokens != 1000 {
		t.Errorf("PromptTokens = %d, want 1000 (the cache read must stay included)", got.PromptTokens)
	}
	if got.GetCachedTokens() != 900 {
		t.Errorf("GetCachedTokens() = %d, want 900", got.GetCachedTokens())
	}
}

// TestCachedTokensFromJSON_ReadsOllamaShape covers the persisted-usage path:
// the counters are re-read later from a stored tokens blob, so a parser that
// only handled the live response would still under-report every historical
// Ollama row.
func TestCachedTokensFromJSON_ReadsOllamaShape(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "ollama native key", in: `{"prompt_eval_cached_count":512}`, want: 512},
		{name: "absent is zero", in: `{"prompt_eval_count":100}`, want: 0},
		// An explicit canonical key still wins: it is what a provider that
		// publishes both shapes means.
		{
			name: "canonical key takes precedence",
			in:   `{"cached_tokens":10,"prompt_eval_cached_count":512}`,
			want: 10,
		},
		{name: "null is not a value", in: `{"prompt_eval_cached_count":null}`, want: 0},
		{name: "malformed is zero", in: `not json`, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CachedTokensFromJSON([]byte(tt.in)); got != tt.want {
				t.Errorf("CachedTokensFromJSON(%s) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
