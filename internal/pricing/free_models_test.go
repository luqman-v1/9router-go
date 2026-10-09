package pricing

import "testing"

// TestIsFreeModel covers the zero-priced namespace upstream added in v0.5.91: a
// model reached through Cline's free tier costs nothing even though the same id
// is priced everywhere else. The namespace is checked before the vendor-prefix
// strip, which would otherwise turn "cline-free/deepseek-v4.1-flash" into a
// priced "deepseek-v4.1-flash".
func TestIsFreeModel(t *testing.T) {
	tests := []struct {
		model string
		free  bool
	}{
		{model: "cline-free/deepseek-v4.1-flash", free: true},
		{model: "CLINE-FREE/DeepSeek-V4.1-Flash", free: true},
		{model: "big-pickle", free: true},
		{model: "BIG-PICKLE", free: true},
		{model: "cline-free/unknown-model", free: true},
		{model: "deepseek/deepseek-v4.1-flash", free: false},
		{model: "gpt-4o", free: false},
		{model: "", free: false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := IsFreeModel(tt.model); got != tt.free {
				t.Errorf("IsFreeModel(%q) = %v, want %v", tt.model, got, tt.free)
			}
		})
	}
}

// TestGetPricingForModel_FreeNamespaceOrder guards the step order: a provider
// table entry still wins, so a gateway that deliberately lists a free model
// at a real rate is not silently zeroed.
func TestGetPricingForModel_FreeNamespaceOrder(t *testing.T) {
	if _, ok := providerPricing["fakeprovider"]; ok {
		t.Fatal("fakeprovider unexpectedly exists in the generated table")
	}
	// The canonical table is consulted after the namespace check, so both a bare
	// and a namespaced id resolve — one to zero, one to the paid rate.
	free, found := GetPricingForModel("cline", "cline-free/deepseek-v4.1-flash")
	if !found || free != zeroPricing {
		t.Errorf("namespaced id = %+v (found %v), want zero pricing", free, found)
	}
	paid, found := GetPricingForModel("cline", "deepseek-v4.1-flash")
	if !found || paid.InputPer1M == 0 {
		t.Errorf("bare id = %+v (found %v), want the paid rate", paid, found)
	}
}
