package pricing

import (
	"math"
	"testing"
)

// TestGetPricingForModel_Parity pins the resolver against the values upstream
// itself returns, rather than against a hand-written subset: the whole point of
// the port is that the numbers in tables.go are the numbers upstream bills.
func TestGetPricingForModel_Parity(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     ModelPricing
		found    bool
	}{
		{
			name: "canonical model", provider: "anthropic", model: "claude-sonnet-4-5-20250929",
			want:  ModelPricing{InputPer1M: 3, OutputPer1M: 15, CachedPer1M: 0.3, ReasoningPer1M: 15, CacheCreationPer1M: 3.75},
			found: true,
		},
		{
			name: "openai model", provider: "openai", model: "gpt-4o",
			want:  ModelPricing{InputPer1M: 2.5, OutputPer1M: 10, CachedPer1M: 1.25, ReasoningPer1M: 15, CacheCreationPer1M: 2.5},
			found: true,
		},
		{
			name: "codex gpt-5.6", provider: "openai", model: "gpt-5.6-sol",
			want:  ModelPricing{InputPer1M: 5, OutputPer1M: 30, CachedPer1M: 0.5, ReasoningPer1M: 30, CacheCreationPer1M: 5},
			found: true,
		},
		{
			// The vendor prefix is stripped before the canonical lookup.
			name: "vendor-prefixed id", provider: "cline", model: "deepseek/deepseek-v4.1-flash",
			want:  ModelPricing{InputPer1M: 0.14, OutputPer1M: 0.28, CachedPer1M: 0.0028, ReasoningPer1M: 0.28, CacheCreationPer1M: 0.14},
			found: true,
		},
		{
			name: "a free namespace never inherits a paid rate", provider: "cline", model: "cline-free/deepseek-v4.1-flash",
			want: zeroPricing, found: true,
		},
		{
			// Nothing prices it upstream either, so nothing invents a rate here.
			name: "an unpriced model", provider: "x", model: "totally-unknown-model",
			found: false,
		},
		{
			name: "an empty model", provider: "openai", model: "",
			found: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := GetPricingForModel(tt.provider, tt.model)
			if found != tt.found {
				t.Fatalf("found = %v, want %v (got %+v)", found, tt.found, got)
			}
			if !tt.found {
				return
			}
			if got != tt.want {
				t.Errorf("pricing = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestCalculateCost_Parity pins the cost formula on the same token mix for
// every case. The expected numbers are what upstream's
// calculateCostFromTokens returns for prompt 1M, completion 1M, cached 250k,
// reasoning 100k, cache_creation 50k.
func TestCalculateCost_Parity(t *testing.T) {
	tokens := TokenCounts{
		PromptTokens:        1_000_000,
		CompletionTokens:    1_000_000,
		CachedTokens:        250_000,
		CacheCreationTokens: 50_000,
		ReasoningTokens:     100_000,
	}

	tests := []struct {
		name     string
		provider string
		model    string
		want     float64
	}{
		{name: "anthropic sonnet", provider: "anthropic", model: "claude-sonnet-4-5-20250929", want: 18.8625},
		{name: "openai gpt-4o", provider: "openai", model: "gpt-4o", want: 13.6875},
		{name: "codex gpt-5.6-sol", provider: "openai", model: "gpt-5.6-sol", want: 36.875},
		{name: "vendor-prefixed deepseek", provider: "cline", model: "deepseek/deepseek-v4.1-flash", want: 0.4137},
		{name: "free namespace", provider: "cline", model: "cline-free/deepseek-v4.1-flash", want: 0},
		{name: "unpriced model", provider: "x", model: "totally-unknown-model", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateCost(tt.provider, tt.model, tokens)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("cost = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCalculateCost_CacheIsASubsetOfPrompt covers the convention the formula
// assumes: prompt_tokens is cache-inclusive, so a provider that bills 1M prompt
// tokens of which 900k were cache reads must not charge 1M at the full input
// rate. It also covers the zero-rate fallbacks.
func TestCalculateCost_CacheIsASubsetOfPrompt(t *testing.T) {
	t.Run("cached tokens are taken out of the input rate", func(t *testing.T) {
		// 1M prompt, all of it a cache read. At the input rate this would be $10;
		// at the cache rate it is $1. The subset subtraction is the whole point,
		// so both rates are set explicitly.
		p := ModelPricing{InputPer1M: 10, OutputPer1M: 0, CachedPer1M: 1}
		got := CalculateCost(TokenCounts{PromptTokens: 1_000_000, CachedTokens: 1_000_000}, p)
		if got != 1 {
			t.Errorf("cost = %v, want 1 — the whole prompt was a cache read", got)
		}
	})

	t.Run("cached falls back to the input rate", func(t *testing.T) {
		p := ModelPricing{InputPer1M: 2, OutputPer1M: 0}
		got := CalculateCost(TokenCounts{PromptTokens: 0, CachedTokens: 1_000_000}, p)
		if got != 2 {
			t.Errorf("cost = %v, want 2", got)
		}
	})

	t.Run("reasoning falls back to the output rate", func(t *testing.T) {
		p := ModelPricing{InputPer1M: 0, OutputPer1M: 4}
		got := CalculateCost(TokenCounts{PromptTokens: 0, ReasoningTokens: 500_000}, p)
		if got != 2 {
			t.Errorf("cost = %v, want 2", got)
		}
	})

	t.Run("cache creation falls back to the input rate", func(t *testing.T) {
		p := ModelPricing{InputPer1M: 6, OutputPer1M: 0}
		got := CalculateCost(TokenCounts{PromptTokens: 0, CacheCreationTokens: 1_000_000}, p)
		if got != 6 {
			t.Errorf("cost = %v, want 6", got)
		}
	})

	t.Run("a negative remainder floors at zero", func(t *testing.T) {
		// Some upstreams report a cache count larger than prompt_tokens. The
		// input portion must not go negative; the cache read is still charged.
		p := ModelPricing{InputPer1M: 3, CachedPer1M: 0}
		got := CalculateCost(TokenCounts{PromptTokens: 10, CachedTokens: 40}, p)
		want := 40.0 / 1_000_000 * 3
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("cost = %v, want %v (cache reads only)", got, want)
		}
	})
}

func TestEstimateCost_ZeroTokens(t *testing.T) {
	if cost := EstimateCost("openai", "gpt-4o", TokenCounts{}); cost != 0 {
		t.Errorf("cost = %v, want 0", cost)
	}
}

// TestMatchPricingPattern covers the glob rule: `*` has to cross a `/`, because
// the tables are written against vendor-prefixed ids.
func TestMatchPricingPattern(t *testing.T) {
	tests := []struct {
		pattern string
		model   string
		want    bool
	}{
		{pattern: "*-codex-xhigh", model: "gpt-5.3-codex-xhigh", want: true},
		{pattern: "*-codex", model: "gpt-5.3-codex", want: true},
		{pattern: "*-codex", model: "gpt-5.3-codex-spark", want: false},
		{pattern: "codex-*", model: "codex-mini", want: true},
		{pattern: "gpt-4*", model: "gpt-4o", want: true},
		{pattern: "gpt-4o", model: "GPT-4O", want: true}, // case-insensitive
		{pattern: "*gpt-4", model: "vendor/scoped/gpt-4", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"|"+tt.model, func(t *testing.T) {
			if got := matchPricingPattern(tt.pattern, tt.model); got != tt.want {
				t.Errorf("matchPricingPattern(%q, %q) = %v, want %v", tt.pattern, tt.model, got, tt.want)
			}
		})
	}
}

// TestPricingTablesPopulated guards the generated file: an empty table would
// still compile and would silently price everything at zero.
func TestPricingTablesPopulated(t *testing.T) {
	if len(modelPricing) < 100 {
		t.Errorf("modelPricing has %d entries, expected the full upstream table", len(modelPricing))
	}
	if len(patternPricing) < 40 {
		t.Errorf("patternPricing has %d rows, expected the full upstream table", len(patternPricing))
	}
	providerEntries := 0
	for _, models := range providerPricing {
		providerEntries += len(models)
	}
	if providerEntries < 100 {
		t.Errorf("providerPricing has %d entries, expected the full upstream table", providerEntries)
	}
}

func TestCommandCodePricing_OMPParity(t *testing.T) {
	cases := []struct {
		model string
		in    float64
		out   float64
		cache float64
	}{
		{"claude-sonnet-5", 2.0, 10.0, 0.2},
		{"deepseek/deepseek-v4-pro", 0.66, 1.98, 0.022},
		{"Qwen/Qwen3.8-Flash", 0.16, 0.47, 0.016},
		{"typesafe/jev", 0.042, 0.0, 0.0},
	}

	for _, c := range cases {
		p, ok := GetPricingForModel("commandcode", c.model)
		if !ok {
			t.Fatalf("model %s missing from commandcode pricing", c.model)
		}
		if p.InputPer1M != c.in || p.OutputPer1M != c.out || p.CachedPer1M != c.cache {
			t.Errorf("%s rates = (in:%v, out:%v, cache:%v), want (%v, %v, %v)",
				c.model, p.InputPer1M, p.OutputPer1M, p.CachedPer1M, c.in, c.out, c.cache)
		}
	}

	// Alias cmc resolves same rates
	p, ok := GetPricingForModel("cmc", "claude-sonnet-5")
	if !ok || p.InputPer1M != 2.0 {
		t.Errorf("cmc alias failed to resolve pricing")
	}
}
