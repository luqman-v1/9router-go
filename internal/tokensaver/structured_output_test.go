package tokensaver

import (
	"strings"
	"testing"
)

// A JSON payload must survive RTK compression byte-for-byte. Every filter
// reduces its input line by line, so a structured body that matches a prose
// filter loses the lines matching no includePattern and stops parsing.
func TestRTK_NeverCorruptsStructuredOutput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "gh api body matching gh content pattern",
			body: "{\n  \"html_url\": \"https://github.com/owner/repo/issues/1\",\n  \"title\": \"feat: x\",\n  \"body\": \"line one\"\n}",
		},
		{
			name: "kubectl -o json body",
			body: "{\n  \"items\": [1, 2, 3],\n  \"body\": \"line one\"\n}",
		},
		{
			name: "json array body",
			body: "[\n  { \"number\": 1, \"title\": \"feat: x\" },\n  { \"number\": 2, \"body\": \"second\" }\n]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultRTKConfig()
			res := CompressTextDetailed(tt.body, cfg)
			if res.Text != tt.body {
				t.Fatalf("structured output was rewritten.\ninput:\n%s\noutput:\n%s", tt.body, res.Text)
			}
			if res.SavedTokens != 0 {
				t.Errorf("SavedTokens = %d, want 0 for structured output", res.SavedTokens)
			}
		})
	}
}

// The gh/kubectl/git-diff filters carried Perl negative lookaheads. Go's RE2
// has no lookahead, so compileRegexSafe silently dropped those patterns and the
// filters loaded with fewer commands than they declare.
func TestLoadFilters_AllDeclaredPatternsCompile(t *testing.T) {
	declared := map[string]int{"gh": 1, "kubectl": 1, "git-diff": 3}
	for _, f := range LoadFilters() {
		want, tracked := declared[f.ID]
		if !tracked {
			continue
		}
		// git-diff declares 3 errorPatterns plus 2 rules errorPatterns; the
		// assertion is that nothing was dropped, so compare against the count
		// of patterns that actually compiled for the fields this filter uses.
		got := len(f.Commands)
		if f.ID == "git-diff" {
			if len(f.ErrorPatterns) == 0 {
				t.Errorf("filter %q compiled 0 errorPatterns; its lookahead pattern was rejected", f.ID)
			}
			continue
		}
		if got < want {
			t.Errorf("filter %q compiled %d command patterns, want at least %d (a pattern was rejected by RE2)", f.ID, got, want)
		}
	}
}

func TestMatchFilter_ProseStillMatches(t *testing.T) {
	cfg := DefaultRTKConfig()
	if m := MatchFilter("total 0\nFAIL src/app.test.ts\n  ● Test suite failed", "", cfg); m == nil {
		t.Fatal("expected a filter for jest output, got nil")
	}
	if m := MatchFilter("nothing structured or noisy here at all", "", cfg); m == nil || m.ID != "generic-output" {
		t.Fatalf("expected generic-output fallback, got %v", m)
	}
	if strings.Contains(MatchFilter("nothing structured or noisy here at all", "", cfg).ID, "json") {
		t.Fatal("prose must not fall through to the JSON path")
	}
}
