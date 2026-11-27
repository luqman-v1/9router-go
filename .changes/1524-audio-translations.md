### Features
- **audio translations**: New route `POST /v1/audio/translations` (and
  `/audio/translations`) forwarding speech-to-text translation requests to
  upstream providers. The lane resolves `whisper-1` when the client sends no
  model, shares combo fallback and the generic URL builder with the other media
  endpoints, and answers `<base>/v1/audio/translations`.
  - Deliberately *not* wired into the Antigravity and ElevenLabs STT lanes: those
    post to vendor transcription endpoints (`/v1/speech-to-text`, Gemini
    generateContent), which transcribe rather than translate. Extending them
    needs a per-vendor translate parameter (Deepgram's `translate=true`), which
    this PR does not attempt.
- **docs**: Added `POST /v1/audio/translations` to the API surface list in `README.md`.

### Tests
- `TestHandleAudioTranslations_ForwardsMultipart` drives a real multipart body and
  asserts the upstream path, content type and forwarded model; moving the endpoint
  string or dropping it from the URL builder fails it.
- `TestHandleAudioTranslations_DefaultModelResolves` locks the `whisper-1` default
  by asserting a model-less request still succeeds.
- `TestHandleAudioTranslations_MalformedMultipartRefused` asserts the guard's own
  400 ("malformed media request body") and that zero upstream calls happen.
- `TestEmbeddings_ForwardGeminiEncodingFormat` and
  `TestHandleEmbeddings_ForwardsEncodingFormatAndDimensions` are contract tests
  covering existing behaviour: the client's `encoding_format` and `dimensions`
  reach upstream unchanged on both the Gemini transform and the generic lane.
  They are not a bug fix; no production line changed to make them pass.
