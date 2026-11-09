package providers

import "testing"

// TestAggregateComboCapabilities_MinMaxOutput pins the upstream parity fix:
// both contextWindow and maxOutput are aggregated as the minimum positive value
// across leaves, so a combo never advertises a ceiling its most constrained
// member cannot serve.
func TestAggregateComboCapabilities_MinMaxOutput(t *testing.T) {
	tests := []struct {
		name    string
		leaves  []CapabilitiesDetail
		wantMax int
		wantCW  int
		wantOk  bool
	}{
		{
			name:    "min maxOutput across leaves",
			leaves:  []CapabilitiesDetail{{MaxOutput: 16000}, {MaxOutput: 64000}},
			wantMax: 16000,
			wantOk:  true,
		},
		{
			name:    "zero output does not drag the minimum down",
			leaves:  []CapabilitiesDetail{{MaxOutput: 0}, {MaxOutput: 64000}},
			wantMax: 64000,
			wantOk:  true,
		},
		{
			name:    "all unbounded yields zero",
			leaves:  []CapabilitiesDetail{{MaxOutput: 0}, {MaxOutput: 0}},
			wantMax: 0,
			wantOk:  true,
		},
		{
			name:    "context window is also the minimum",
			leaves:  []CapabilitiesDetail{{ContextWindow: 1000000, MaxOutput: 64000}, {ContextWindow: 131072, MaxOutput: 16000}},
			wantMax: 16000,
			wantCW:  131072,
			wantOk:  true,
		},
		{
			name:   "empty list yields no aggregate",
			leaves: nil,
			wantOk: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := AggregateComboCapabilities(tt.leaves)
			if ok != tt.wantOk {
				t.Errorf("AggregateComboCapabilities() ok = %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if got.MaxOutput != tt.wantMax {
				t.Errorf("MaxOutput = %d, want %d", got.MaxOutput, tt.wantMax)
			}
			if tt.wantCW != 0 && got.ContextWindow != tt.wantCW {
				t.Errorf("ContextWindow = %d, want %d", got.ContextWindow, tt.wantCW)
			}
		})
	}
}
