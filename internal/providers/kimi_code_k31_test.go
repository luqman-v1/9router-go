package providers

import "testing"

func TestKimiCodeK31ContextLimits(t *testing.T) {
	if got := GetCapabilitiesForModel("kimi", "k3.1-pro").ContextWindow; got != 1048576 {
		t.Errorf("GetCapabilitiesForModel(kimi, k3.1-pro).ContextWindow = %d, want 1048576", got)
	}
	if got := GetCapabilitiesForModel("kimi", "k3.1-pro-highspeed").ContextWindow; got != 1048576 {
		t.Errorf("GetCapabilitiesForModel(kimi, k3.1-pro-highspeed).ContextWindow = %d, want 1048576", got)
	}
	if got := GetCapabilitiesForModel("kmc", "k3.1-pro").ContextWindow; got != 1048576 {
		t.Errorf("GetCapabilitiesForModel(kmc, k3.1-pro).ContextWindow = %d, want 1048576", got)
	}
}
