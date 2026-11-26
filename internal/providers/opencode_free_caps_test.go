package providers

import "testing"

func TestOpenCodeFreeModelCaps(t *testing.T) {
	tests := []struct {
		model string
		cw    int
		max   int
	}{
		{"iceman-free", 262144, 131072},
		{"beam-free", 1048576, 131072},
		{"flint-40b-free", 262144, 65536},
		{"nemotron-3.5-super-vl-free", 1048576, 131072},
		{"jev-1.13-free", 64000, 32000},
		{"nemotron-3.5-lightning-free", 262144, 262144},
		{"hy4-flash-free", 1048576, 65536},
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

// Dead free ids must not be advertised by the static tables.
//
// OpenCode rotates its free lane without notice, and a dead id is worse than a
// missing one: the dashboard offers a model the chat endpoint then refuses, and
// every such request burns a fallback attempt before reaching one that works.
// Each id below answered 410 ModelDeprecated or a 400 upstream refusal on
// POST https://opencode.ai/zen/v1/chat/completions with the literal key
// "public", and none appears in either live feed (/zen/v1/models,
// /inference/go/v1/models) — the check freemodels.go's catalogue comments
// already demand before an id is seeded.
func TestDeadOpenCodeFreeIdsAreNotAdvertised(t *testing.T) {
	dead := map[string]string{
		"longcat-2.5-free":     "410 ModelDeprecated",
		"inkling-2-small-free": "410 ModelDeprecated",
		"tidepool-alpha-free":  "400 Upstream request failed: Model is unavailable",
	}
	for _, provider := range []string{"oc", "opencode", "opencode-zen", "ocz"} {
		for id, evidence := range dead {
			t.Run(provider+"/"+id, func(t *testing.T) {
				if _, ok := providerCapabilities[provider][id]; ok {
					t.Errorf("capability row still advertises %s (%s)", id, evidence)
				}
				if _, ok := providerTokenLimits[provider][id]; ok {
					t.Errorf("token limit row still advertises %s (%s)", id, evidence)
				}
			})
		}
	}
}
