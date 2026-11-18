### ollama-local Phonon TTS

Add speech synthesis to the `ollama-local` provider via the phonon family,
matching merged upstream `decolua/9router@31a9d262` (v0.5.111):

- **Registry**: 15 phonon tags (`phonon`, `phonon:latest`,
  `phonon:multilingual`, `phonon:40m-multilingual`, and the per-language
  `en`/`de`/`es`/`fr`/`pt` pairs) registered in
  `ProviderModels["ollama-local"]` and classified `"tts"` in
  `ProviderModelKinds`, so they stay out of the `/v1/models` LLM list and
  publish `AudioOutput`.
- **Provider config**: `TTSURL: http://localhost:11434/v1/audio/speech` on the
  `ollama-local` entry, the OpenAI-compatible audio endpoint the local daemon
  serves (upstream `ollama-local.js` `ttsConfig`, `format: "openai"`,
  `authType: "apikey"`, `defaultModel: "phonon"`). A connection's custom
  `baseUrl` still wins, deriving `/v1/audio/speech` from the configured host.
- **Routing**: `/v1/audio/speech` requests for `ollama-local/phonon*` forward
  the OpenAI TTS body verbatim to the configured Ollama host through the
  existing `forwardMediaRequest` path, with `voice` and `response_format`
  passed through and the audio body streamed back unchanged.

Tests: table-driven `testing.T` coverage for registry classification
(`GetProviderModelKind` + `AudioOutput` for every phonon tag) and for the
media route (custom-base-URL and default-`TTSURL` subtests asserting the
upstream path, the rewritten model id, and byte-for-byte audio passthrough).

Sources: upstream `decolua/9router` `open-sse/providers/registry/ollama-local.js`
at commit `31a9d262` (merged to `main`). This is a parity port — the phonon
family is already merged upstream, not a speculative feature.