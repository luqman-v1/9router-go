package providers

import "testing"

func TestGetCapabilitiesForModel_NvidiaNemotronUltra(t *testing.T) {
	caps := capabilitiesOf(t, "nvidia", "nvidia/nemotron-3-ultra-550b-a55b")
	if !caps.Tools {
		t.Error("Tools = false, want true")
	}
	if caps.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", caps.ContextWindow)
	}
	if !caps.Reasoning {
		t.Error("Reasoning = false, want true")
	}
	if caps.MaxOutput != 8192 {
		t.Errorf("MaxOutput = %d, want 8192", caps.MaxOutput)
	}
}
