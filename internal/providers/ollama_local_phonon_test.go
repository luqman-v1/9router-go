package providers

import "testing"

// ollama-local's phonon family is speech synthesis. The id carries no "tts"
// signal, so the registry kind is the only source that reaches it — without
// the ProviderModelKinds entry the ids would resolve as plain LLM entries,
// stay in the /v1/models LLM list, and publish AudioOutput=false.
func TestOllamaLocalPhononClassifiedAsTTS(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	models := GetProviderModels("ollama-local")
	if len(models) == 0 {
		t.Fatal("GetProviderModels(\"ollama-local\") is empty; the phonon family was not registered")
	}
	if len(models) < 15 {
		t.Errorf("GetProviderModels(\"ollama-local\") returned %d models, want at least 15 phonon tags", len(models))
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			if kind := GetProviderModelKind("ollama-local", model); kind != serviceKindTTS {
				t.Errorf("GetProviderModelKind(ollama-local, %q) = %q, want %q", model, kind, serviceKindTTS)
			}
			if !GetCapabilitiesForModel("ollama-local", model).AudioOutput {
				t.Errorf("%q on ollama-local: AudioOutput = false, want true", model)
			}
		})
	}

	// The default model upstream points a bare request at (ttsConfig.defaultModel).
	if kind := GetProviderModelKind("ollama-local", "phonon"); kind != serviceKindTTS {
		t.Errorf("default model \"phonon\" kind = %q, want %q", kind, serviceKindTTS)
	}
}

// The TTS endpoint is only reachable because the registry carries it: a
// connection without a custom base URL dials KnownProviders.TTSURL. Without it
// the generic media forwarder derives /v1/audio/speech from BaseURL, which on
// this provider is .../v1/chat/completions.
func TestOllamaLocalPhononTTSURLDefault(t *testing.T) {
	const wantTTSURL = "http://localhost:11434/v1/audio/speech"
	cfg, ok := KnownProviders["ollama-local"]
	if !ok {
		t.Fatal(`KnownProviders["ollama-local"] is missing`)
	}
	if cfg.TTSURL != wantTTSURL {
		t.Errorf("KnownProviders[\"ollama-local\"].TTSURL = %q, want %q", cfg.TTSURL, wantTTSURL)
	}
}

// The bare id the route forwards (`ollama-local/phonon`) must be TTS too. The
// tagged family loop above covers phonon:latest and the language pairs, but the
// bare id is the one the branch in forwardTTSRequest passes through.
func TestOllamaLocalPhononBareIDKind(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	if kind := GetProviderModelKind("ollama-local", "phonon"); kind != serviceKindTTS {
		t.Errorf("GetProviderModelKind(ollama-local, \"phonon\") = %q, want %q", kind, serviceKindTTS)
	}
	if !GetCapabilitiesForModel("ollama-local", "phonon").AudioOutput {
		t.Error(`GetCapabilitiesForModel(ollama-local, "phonon").AudioOutput = false, want true`)
	}
}
