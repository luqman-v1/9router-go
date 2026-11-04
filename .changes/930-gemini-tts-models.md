### feat(gemini): register Gemini 3.8 Flash TTS and 3.8 Flash Lite TTS models

- Add `gemini-3.8-flash-tts` and `gemini-3.8-flash-lite-tts` to the gemini provider model catalog in `internal/providers/registry_models.go`.
- Declare both models with the `tts` service kind in `ProviderModelKinds` so they are routed to audio/TTS handling rather than leaking into the LLM list.
- Added unit tests in `internal/providers/providers_test.go` confirming both models are registered under `gemini` and carry the `tts` kind (upstream `decolua/9router#5321` parity).
