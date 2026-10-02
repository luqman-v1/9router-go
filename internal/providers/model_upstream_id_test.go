package providers

import "testing"

// The codex catalog publishes ids that are not wire ids, and the ChatGPT
// backend answers 400 when one of them is forwarded verbatim. These cases pin
// the mapping (open-sse/providers/registry/codex.js `upstreamModelId`, resolved
// by open-sse/config/providerModels.js getModelUpstreamId).
func TestCodexUpstreamModelID(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		// Extended-context variants declare their base id upstream.
		{name: "astra extended context", model: "gpt-6-astra[1m]", want: "gpt-6-astra"},
		{name: "sol extended context", model: "gpt-6-sol[1m]", want: "gpt-6-sol"},
		{name: "luna extended context", model: "gpt-6-luna[1m]", want: "gpt-6-luna"},
		{name: "gpt-5.6 sol extended context", model: "gpt-5.6-sol[1m]", want: "gpt-5.6-sol"},
		{name: "gpt-5.6 terra extended context", model: "gpt-5.6-terra[1m]", want: "gpt-5.6-terra"},
		{name: "gpt-5.6 luna extended context", model: "gpt-5.6-luna[1m]", want: "gpt-5.6-luna"},

		// Synthesized review ids map back to the base model.
		{name: "sol review", model: "gpt-5.6-sol-review", want: "gpt-5.6-sol"},
		{name: "terra review", model: "gpt-5.6-terra-review", want: "gpt-5.6-terra"},
		{name: "gpt-5.5 review", model: "gpt-5.5-review", want: "gpt-5.5"},

		// The auto-review virtual model is NOT derived from a base model, so its
		// suffix stays (#1398). Stripping it would forward "codex-auto".
		{name: "auto review is forwarded verbatim", model: "codex-auto-review", want: "codex-auto-review"},

		// A "(level)" thinking override is a request modifier, not part of the id.
		// It is preserved because downstream code re-applies thinking from it.
		{name: "extended context with thinking override", model: "gpt-6-sol[1m](high)", want: "gpt-6-sol(high)"},
		{name: "review with thinking override", model: "gpt-5.6-sol-review(max)", want: "gpt-5.6-sol(max)"},
		{name: "plain id with thinking override", model: "gpt-5.5(high)", want: "gpt-5.5(high)"},

		// A vendor prefix names a different provider, so the suffix belongs to it.
		{name: "vendor prefixed id is not rewritten", model: "cx/gpt-5.6-sol-review", want: "cx/gpt-5.6-sol-review"},

		// Unknown ids pass through: the gateway forwards arbitrary model strings.
		{name: "unknown id passes through", model: "gpt-some-future-model", want: "gpt-some-future-model"},
		{name: "empty model passes through", model: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CodexUpstreamModelID(tt.model); got != tt.want {
				t.Errorf("CodexUpstreamModelID(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// Every extended-context and review id in the published catalog has to resolve
// to a base id that is itself in the catalog. A catalog entry whose wire id is
// missing is a 400 waiting to happen.
func TestCodexCatalogWireIDsResolve(t *testing.T) {
	reviewSuffix := "-review"
	extendedSuffix := "[1m]"
	for _, id := range ProviderModels["codex"] {
		if id == CodexAutoReviewModel {
			continue
		}
		resolved := CodexUpstreamModelID(id)
		if resolved == id {
			continue
		}
		if found(id, ProviderModels["codex"]) || found(resolved, ProviderModels["codex"]) {
			continue
		}
		t.Errorf("codex catalog id %q resolves to %q, which is not itself a codex id "+
			"(review suffix %q, extended suffix %q)", id, resolved, reviewSuffix, extendedSuffix)
	}
}

func found(id string, list []string) bool {
	for _, candidate := range list {
		if candidate == id {
			return true
		}
	}
	return false
}