package translator

import (
	"testing"
)

func TestAntigravityEffortTier(t *testing.T) {
	cases := []struct {
		name  string
		model string
		body  string
		want  string
	}{
		{"model suffix wins over the body", "gemini-3.8-flash(low)", `{"reasoning_effort":"high"}`, "low"},
		{"reasoning_effort", "gemini-3.8-flash", `{"reasoning_effort":"high"}`, "high"},
		{"output_config effort", "gemini-3.8-flash", `{"output_config":{"effort":"low"}}`, "low"},
		{"reasoning object effort", "gemini-3.8-flash", `{"reasoning":{"effort":"medium"}}`, "medium"},
		{"xhigh folds onto high", "gemini-3.8-flash", `{"reasoning_effort":"xhigh"}`, "high"},
		{"minimal folds onto low", "gemini-3.8-flash", `{"reasoning_effort":"minimal"}`, "low"},
		{"large budget tokens read high", "gemini-3.8-flash", `{"thinking":{"budget_tokens":24576}}`, "high"},
		{"small budget tokens read low", "gemini-3.8-flash", `{"thinking":{"budget_tokens":2048}}`, "low"},
		{"mid budget tokens read medium", "gemini-3.8-flash", `{"thinking":{"budget_tokens":8192}}`, "medium"},
		{"effort beats budget tokens", "gemini-3.8-flash", `{"reasoning_effort":"low","thinking":{"budget_tokens":24576}}`, "low"},
		{"no tier requested", "gemini-3.8-flash", `{}`, ""},
		{"no body", "gemini-3.8-flash", "", ""},
		{"non-string effort ignored", "gemini-3.8-flash", `{"reasoning_effort":3}`, ""},
	}
	for _, tc := range cases {
		if got := AntigravityEffortTier(tc.model, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: AntigravityEffortTier(%q, %s) = %q, want %q", tc.name, tc.model, tc.body, got, tc.want)
		}
	}
}

// The bare catalog ids are the medium tier, so an unadorned id must follow the
// client's effort to the tiered backend id instead of always landing medium.
func TestAntigravityTieredModel(t *testing.T) {
	cases := []struct {
		name  string
		model string
		body  string
		want  string
	}{
		{"high effort", "gemini-3.8-flash", `{"reasoning_effort":"high"}`, "gemini-3.8-flash-high"},
		{"low effort", "gemini-3.8-flash", `{"reasoning_effort":"low"}`, "gemini-3.8-flash-low"},
		{"medium effort keeps the bare id's tier", "gemini-3.8-flash", `{"reasoning_effort":"medium"}`, "gemini-3.8-flash-medium"},
		{"default medium when omitted", "gemini-3.8-flash", `{}`, "gemini-3.8-flash-medium"},
		{"high via budget tokens", "gemini-3.8-flash", `{"thinking":{"budget_tokens":24576}}`, "gemini-3.8-flash-high"},
		{"low via budget tokens", "gemini-3.8-flash", `{"thinking":{"budget_tokens":2048}}`, "gemini-3.8-flash-low"},
		{"output_config effort", "gemini-3.8-flash", `{"output_config":{"effort":"high"}}`, "gemini-3.8-flash-high"},
		{"model suffix beats body", "gemini-3.8-flash(low)", `{"reasoning_effort":"high"}`, "gemini-3.8-flash-low"},
		{"direct tiered id stays", "gemini-3.8-flash-high", `{}`, "gemini-3.8-flash-high"},
		{"argon follows effort", "gemini-4-argon", `{"reasoning_effort":"low"}`, "gemini-4-argon-low"},
		{"claude family follows effort", "claude-sonnet-5-5", `{"reasoning_effort":"low"}`, "claude-sonnet-5-5-low"},
		{"unknown tier falls back to the base row", "gemini-3.8-flash", `{"reasoning_effort":"turbo"}`, "gemini-3.8-flash-medium"},
		{"pro high resolves through its tiered row", "gemini-3.1-pro", `{"reasoning_effort":"high"}`, "gemini-pro-agent"},
		{"medium pro keeps the plain synonym mapping", "gemini-3.1-pro", `{"reasoning_effort":"medium"}`, "gemini-pro-agent"},
	}
	for _, tc := range cases {
		if got := AntigravityTieredModel(tc.model, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: AntigravityTieredModel(%q, %s) = %q, want %q", tc.name, tc.model, tc.body, got, tc.want)
		}
	}
}
