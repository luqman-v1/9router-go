package providers

import "testing"

func TestIsJailbreakModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "bare uncensored", model: "llama-3-uncensored", want: true},
		{name: "bare unrestricted", model: "qwen-unrestricted", want: true},
		{name: "bare jailbreak", model: "dolphin-jailbreak", want: true},
		{name: "nsfw marker", model: "model-nsfw", want: true},
		{name: "no-filter hyphenated", model: "mistral-no-filter", want: true},
		{name: "nofilter joined", model: "mistral-nofilter", want: true},
		{name: "unfiltered", model: "yi-unfiltered", want: true},
		{name: "jailbroken", model: "wizard-jailbroken", want: true},
		{name: "underscore separator", model: "model_uncensored_v2", want: true},
		{name: "dot separator", model: "vendor.uncensored.7b", want: true},
		{name: "uppercase", model: "LLAMA-UNCENSORED", want: true},
		{name: "provider qualified", model: "vendor/llama-uncensored", want: true},
		{name: "thinking suffix stripped", model: "claude-x(high)", want: false},
		{name: "thinking suffix then marker", model: "llama-uncensored(high)", want: true},
		{name: "plain model", model: "gpt-4o", want: false},
		{name: "marker as substring of a longer segment", model: "unrestrictedly-fast", want: false},
		{name: "bare jail is not a marker", model: "jail-model", want: false},
		{name: "empty", model: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsJailbreakModel(tt.model); got != tt.want {
				t.Errorf("IsJailbreakModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// The explicit flag and the name heuristic are ORed by the caller, so a model
// the operator marked by hand must survive a name that says nothing.
func TestJailbreakFlagSurvivesPlainName(t *testing.T) {
	ClearCustomModelCaps()
	t.Cleanup(ClearCustomModelCaps)

	const provider = "nara"
	const model = "totally-normal-name"
	SetCustomModelCaps(provider, model, Capabilities{Jailbreak: true})

	detail := GetCapabilitiesDetailForModel(provider, model)
	if !detail.Jailbreak {
		t.Fatalf("Jailbreak = false, want true: the saved flag was dropped")
	}
}

// The reverse direction: a name that names itself uncensored is flagged even
// though nobody opened the dialog.
func TestJailbreakNameHeuristicAlone(t *testing.T) {
	ClearCustomModelCaps()
	t.Cleanup(ClearCustomModelCaps)

	detail := GetCapabilitiesDetailForModel("nara", "llama-3-uncensored")
	if !detail.Jailbreak {
		t.Fatalf("Jailbreak = false, want true from the name heuristic")
	}
}

// A custom row may save the flag under any of the three operator-facing names;
// the publisher maps all three onto the one field.
func TestJailbreakAliasNames(t *testing.T) {
	for _, name := range []string{"jailbreak", "uncensored", "unrestricted"} {
		t.Run(name, func(t *testing.T) {
			ClearCustomModelCaps()
			t.Cleanup(ClearCustomModelCaps)

			const provider = "nara"
			model := "model-" + name
			SetCustomModelCaps(provider, model, Capabilities{Jailbreak: true})

			detail := GetCapabilitiesDetailForModel(provider, model)
			if !detail.Jailbreak {
				t.Errorf("Jailbreak = false for saved name %q", name)
			}
		})
	}
}
