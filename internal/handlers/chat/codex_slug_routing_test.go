package chat

import (
	"testing"
)

// Codex-only slugs exist on backend-api/codex/models but not on the OpenAI
// API. Before these rules a bare id fell through to the generic gpt-* fallback
// and resolved to openai, so an account holding only a Codex OAuth connection
// got a 404 for every model its own CLI could list (#4405).
func TestCodexOnlyModelSlug(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "gpt-5.6 variant", model: "gpt-5.6-terra", want: true},
		{name: "gpt-5.4 variant", model: "gpt-5.4-mini", want: true},
		// gpt-5.5 itself is a plain OpenAI slug — upstream matches /^gpt-[56]\./
		// only, so a bare "gpt-5.5" with no variant suffix stays off codex.
		{name: "gpt-5.5 is not codex", model: "gpt-5.5", want: false},
		{name: "gpt-6 dotted variant", model: "gpt-6.1-sol", want: true},
		{name: "gpt-6 dashed variant", model: "gpt-6-astra", want: true},
		{name: "extended context variant", model: "gpt-6-sol[1m]", want: true},
		{name: "daybreak", model: "gpt-daybreak-blue-latest", want: true},
		{name: "reserve", model: "gpt-reserve", want: true},

		// These deliberately stay on the OpenAI API.
		{name: "gpt-4o is an OpenAI model", model: "gpt-4o", want: false},
		{name: "gpt-4 turbo is an OpenAI model", model: "gpt-4-turbo", want: false},
		{name: "gpt-3.5 is an OpenAI model", model: "gpt-3.5-turbo", want: false},
		{name: "gpt-4.1 is an OpenAI model", model: "gpt-4.1-mini", want: false},
		{name: "bare gpt-5 prefix is not a slug", model: "gpt-5", want: false},
		{name: "bare gpt-6 prefix is not a slug", model: "gpt-6", want: false},
		{name: "claude model is not codex", model: "claude-sonnet-5", want: false},
		{name: "empty", model: "", want: false},
		{name: "whitespace only", model: "   ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexOnlyModelSlug(tt.model); got != tt.want {
				t.Errorf("codexOnlyModelSlug(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// A bare codex slug must resolve to codex even with no database at all, so the
// static-catalog path (nil Repo) keeps working.
func TestResolveModel_BareCodexSlugWithoutRepo(t *testing.T) {
	h := &ChatHandler{}
	for _, model := range []string{"gpt-5.6-terra", "gpt-6-astra", "gpt-daybreak-blue-latest", "gpt-reserve"} {
		t.Run(model, func(t *testing.T) {
			info, err := h.resolveModel(model)
			if err != nil {
				t.Fatalf("resolveModel(%q) error = %v, want a codex resolution", model, err)
			}
			if info.Provider != "codex" {
				t.Errorf("resolveModel(%q).Provider = %q, want codex", model, info.Provider)
			}
			if info.Model != model {
				t.Errorf("resolveModel(%q).Model = %q, want the id unchanged", model, info.Model)
			}
		})
	}
}

