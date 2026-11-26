package providers

import "testing"

func TestOpenCodeFreeModelCaps(t *testing.T) {
	tests := []struct {
		model string
		cw    int
		max   int
	}{
		{"longcat-2.5-free", 1000000, 262144},
		{"iceman-free", 262144, 131072},
		{"inkling-2-small-free", 1048576, 131072},
		{"beam-free", 1048576, 131072},
		{"flint-40b-free", 262144, 65536},
		{"nemotron-3.5-super-vl-free", 1048576, 131072},
		{"jev-1.13-free", 64000, 32000},
		{"nemotron-3.5-lightning-free", 262144, 262144},
		{"hy4-flash-free", 1048576, 65536},
		{"tidepool-alpha-free", 1048576, 131072},
	}
	for _, p := range []string{"opencode", "oc"} {
		for _, tt := range tests {
			t.Run(p+"/"+tt.model, func(t *testing.T) {
				caps := GetCapabilitiesForModel(p, tt.model)
				if caps.ContextWindow != tt.cw {
					t.Errorf("context window = %d, want %d", caps.ContextWindow, tt.cw)
				}
				if caps.MaxOutput != tt.max {
					t.Errorf("max output = %d, want %d", caps.MaxOutput, tt.max)
				}
				if caps.ContextWindow == 128000 && caps.MaxOutput == 4096 {
					t.Errorf("hit 128k floor")
				}
			})
		}
	}
}

func TestOpenCodeFreeModelCapsDetailLanes(t *testing.T) {
	for _, p := range []string{"opencode", "oc", "opencode-go", "opencode-zen", "ocz"} {
		t.Run(p, func(t *testing.T) {
			if p == "opencode-go" {
				caps := GetCapabilitiesDetailForModel(p, "beam-free")
				if caps.MaxOutput != 131072 {
					t.Errorf("beam-free max output = %d, want 131072", caps.MaxOutput)
				}
			}
			if p == "opencode-zen" || p == "ocz" {
				caps := GetCapabilitiesDetailForModel(p, "nemotron-3.5-lightning-free")
				if caps.MaxOutput != 262144 {
					t.Errorf("nemotron-3.5-lightning-free max output = %d, want 262144", caps.MaxOutput)
				}
			}
		})
	}
}
