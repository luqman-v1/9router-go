package pricing

import "testing"

func TestFormatPerTokenPrice(t *testing.T) {
	if got := FormatPerTokenPrice(2.0); got != "0.000002" {
		t.Fatalf("FormatPerTokenPrice(2.0) = %q, want %q", got, "0.000002")
	}
	if got := FormatPerTokenPrice(0); got != "0" {
		t.Fatalf("FormatPerTokenPrice(0) = %q, want %q", got, "0")
	}
}

func TestBuildOpenRouterPricingFreeModel(t *testing.T) {
	for _, model := range []string{"big-pickle", "some-model-free"} {
		got := BuildOpenRouterPricing("openrouter", model)
		if got == nil || got.Prompt != "0" || got.Input != 0 {
			t.Fatalf("free model %q: got %+v, want prompt 0 and input 0", model, got)
		}
	}
}

func TestBuildOpenRouterPricingPaidModel(t *testing.T) {
	got := BuildOpenRouterPricing("anthropic", "claude-3-7-sonnet")
	if got == nil {
		t.Fatal("expected pricing, got nil")
	}
	if got.Prompt != "0.000003" || got.Completion != "0.000015" || got.Input != 3 || got.Output != 15 {
		t.Fatalf("got %+v, want prompt 0.000003, completion 0.000015, input 3, output 15", got)
	}
}

func TestBuildOpenRouterPricingUnknownModel(t *testing.T) {
	if got := BuildOpenRouterPricing("unknown", "unknown-model"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}
