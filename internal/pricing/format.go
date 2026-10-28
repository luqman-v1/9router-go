package pricing

import (
	"strconv"
	"strings"
)

type OpenRouterPricing struct {
	Prompt          string   `json:"prompt"`
	Completion      string   `json:"completion"`
	InputCacheRead  string   `json:"input_cache_read,omitempty"`
	InputCacheWrite string   `json:"input_cache_write,omitempty"`
	Input           float64  `json:"input"`
	Output          float64  `json:"output"`
	Cached          *float64 `json:"cached,omitempty"`
	CacheCreation   *float64 `json:"cache_creation,omitempty"`
	Reasoning       *float64 `json:"reasoning,omitempty"`
}

func FormatPerTokenPrice(ratePer1M float64) string {
	if ratePer1M == 0 {
		return "0"
	}
	perToken := ratePer1M / 1000000.0
	str := strconv.FormatFloat(perToken, 'f', 12, 64)
	str = strings.TrimRight(str, "0")
	str = strings.TrimRight(str, ".")
	if str == "" {
		return "0"
	}
	return str
}

func BuildOpenRouterPricing(provider, model string) *OpenRouterPricing {
	cleanModel := strings.ToLower(model)
	if IsFreeModel(model) || strings.HasSuffix(cleanModel, "-free") || strings.HasSuffix(cleanModel, ":free") {
		return &OpenRouterPricing{
			Prompt:     "0",
			Completion: "0",
			Input:      0,
			Output:     0,
		}
	}
	p, ok := GetPricingForModel(provider, model)
	if !ok {
		return nil
	}
	res := &OpenRouterPricing{
		Prompt:     FormatPerTokenPrice(p.InputPer1M),
		Completion: FormatPerTokenPrice(p.OutputPer1M),
		Input:      p.InputPer1M,
		Output:     p.OutputPer1M,
	}
	if p.CachedPer1M > 0 {
		res.InputCacheRead = FormatPerTokenPrice(p.CachedPer1M)
		c := p.CachedPer1M
		res.Cached = &c
	}
	if p.CacheCreationPer1M > 0 {
		res.InputCacheWrite = FormatPerTokenPrice(p.CacheCreationPer1M)
		cc := p.CacheCreationPer1M
		res.CacheCreation = &cc
	}
	if p.ReasoningPer1M > 0 {
		r := p.ReasoningPer1M
		res.Reasoning = &r
	}
	return res
}
