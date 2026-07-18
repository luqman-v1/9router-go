package pricing

import (
	"testing"
)

func TestEstimateCost(t *testing.T) {
	cost := EstimateCost("gpt-4o", 1000, 500)
	// 1000/1M * 2.5 + 500/1M * 10.0 = 0.0025 + 0.005 = 0.0075
	expected := 0.0075
	if cost != expected {
		t.Errorf("gpt-4o: got %f, want %f", cost, expected)
	}
}

func TestEstimateCost_ZeroTokens(t *testing.T) {
	cost := EstimateCost("gpt-4o", 0, 0)
	if cost != 0 {
		t.Errorf("expected 0, got %f", cost)
	}
}

func TestEstimateCost_DefaultPricing(t *testing.T) {
	cost := EstimateCost("unknown-model", 1000000, 1000000)
	// default: $1.0/1M input, $3.0/1M output
	expected := 4.0
	if cost != expected {
		t.Errorf("expected %f, got %f", expected, cost)
	}
}

func TestEstimateCost_PrefixMatch(t *testing.T) {
	// claude-haiku-3-5-sonnet should match claude-haiku prefix
	cost := EstimateCost("claude-haiku-3-5-sonnet", 2000000, 500000)
	// 2M/1M * 0.25 + 0.5M/1M * 1.25 = 0.5 + 0.625 = 1.125
	expected := 1.125
	if cost != expected {
		t.Errorf("claude-haiku prefix: got %f, want %f", cost, expected)
	}
}

func TestGetPricing(t *testing.T) {
	p := GetPricing("gpt-4o")
	if p.InputPer1M != 2.5 || p.OutputPer1M != 10.0 {
		t.Errorf("got %#v", p)
	}

	p = GetPricing("nonexistent")
	if p != defaultPricing {
		t.Errorf("expected default, got %#v", p)
	}
}

func TestLookupPricing_ExactMatch(t *testing.T) {
	p := lookupPricing("deepseek-v4-flash")
	if p.InputPer1M != 0.07 {
		t.Errorf("expected 0.07, got %f", p.InputPer1M)
	}
}

func TestLookupPricing_CaseInsensitive(t *testing.T) {
	p := lookupPricing("GPT-4O-Turbo")
	if p.InputPer1M != 2.5 {
		t.Errorf("expected 2.5 for case-insensitive match, got %f", p.InputPer1M)
	}
}

func TestLookupPricing_LongestPrefix(t *testing.T) {
	p := lookupPricing("claude-sonnet-4-20250514")
	if p.InputPer1M != 3.0 {
		t.Errorf("expected 3.0 for claude-sonnet-4 prefix, got %f", p.InputPer1M)
	}
}
