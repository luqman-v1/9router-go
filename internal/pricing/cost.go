package pricing

import "strings"

// ModelPricing holds per-million-token costs for a model.
type ModelPricing struct {
	InputPer1M  float64
	OutputPer1M float64
}

// pricingTable maps model name prefixes to their pricing.
// Lookup uses longest-prefix matching so "claude-sonnet-4" catches
// "claude-sonnet-4.5", "claude-sonnet-4-20250514", etc.
var pricingTable = map[string]ModelPricing{
	"claude-sonnet-4": {InputPer1M: 3.0, OutputPer1M: 15.0},
	"claude-haiku":    {InputPer1M: 0.25, OutputPer1M: 1.25},
	"deepseek-v4-flash": {InputPer1M: 0.07, OutputPer1M: 0.28},
	"gpt-4o":          {InputPer1M: 2.5, OutputPer1M: 10.0},
}

// defaultPricing is the fallback when no prefix matches.
var defaultPricing = ModelPricing{InputPer1M: 1.0, OutputPer1M: 3.0}

// EstimateCost calculates the USD cost for a request given the model name and token counts.
// It uses longest-prefix matching against the pricing table, falling back to defaultPricing.
func EstimateCost(model string, promptTokens, completionTokens int) float64 {
	pricing := lookupPricing(model)

	inputCost := float64(promptTokens) / 1_000_000 * pricing.InputPer1M
	outputCost := float64(completionTokens) / 1_000_000 * pricing.OutputPer1M
	return inputCost + outputCost
}

// lookupPricing finds the best matching pricing entry for a model name.
// It tries exact matches first, then longest-prefix matches.
func lookupPricing(model string) ModelPricing {
	model = strings.ToLower(model)

	// Exact match
	if p, ok := pricingTable[model]; ok {
		return p
	}

	// Longest-prefix match
	bestLen := 0
	bestPricing := defaultPricing
	for prefix, p := range pricingTable {
		if strings.HasPrefix(model, prefix) && len(prefix) > bestLen {
			bestLen = len(prefix)
			bestPricing = p
		}
	}

	return bestPricing
}

// GetPricing returns the pricing for a model (exposed for external use / testing).
func GetPricing(model string) ModelPricing {
	return lookupPricing(model)
}
