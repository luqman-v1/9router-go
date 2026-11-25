package providers

import "testing"

func TestQwenCloudAliases(t *testing.T) {
	cases := map[string]string{
		"qc":         "qwencloud",
		"qctp":       "qwencloud",
		"qwen-cloud": "qwencloud",
	}
	for alias, canonical := range cases {
		if got := ResolveAlias(alias); got != canonical {
			t.Errorf("ResolveAlias(%q) = %q, want %q", alias, got, canonical)
		}
	}
}

func TestQwenCloudCapabilities(t *testing.T) {
	cases := []struct {
		provider   string
		model      string
		cw         int
		mo         int
		videoInput bool
	}{
		{"qwencloud", "qwen4-max", 1000000, 393216, true},
		{"qwencloud", "qwen4-max-preview", 1000000, 393216, true},
		{"qwencloud", "qwen3.8-max", 1000000, 131072, true},
		{"alitp-intl", "qwen4-max", 1000000, 393216, true},
		{"alitp-intl", "qwen4-max-preview", 1000000, 393216, true},
		{"alitp-intl", "qwen3.8-max", 1000000, 131072, true},
	}
	for _, c := range cases {
		got := GetCapabilitiesForModel(c.provider, c.model)
		if got.ContextWindow != c.cw || got.MaxOutput != c.mo {
			t.Errorf("%s/%s: got (%d,%d), want (%d,%d)", c.provider, c.model, got.ContextWindow, got.MaxOutput, c.cw, c.mo)
		}
		if got.VideoInput != c.videoInput {
			t.Errorf("%s/%s: got VideoInput=%v, want %v", c.provider, c.model, got.VideoInput, c.videoInput)
		}
	}
}
