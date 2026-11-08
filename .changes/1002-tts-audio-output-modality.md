### fix(providers): publish audioOutput for speech-synthesis models

A TTS model was advertised as text-only. `GET /v1/models` reported
`audioOutput: false` and `output_modalities: ["text"]` for
`studio/gemini-3.8-flash-tts`, while the endpoint itself answered
`Content-Type: audio/wav`. A client filtering on modality never saw it.

`buildModalities` in `internal/handlers/chat/models_list.go` derives
`output_modalities` from the `AudioOutput` capability alone, so the capability
flag is the whole fix for the published modality — no handler change needed.

The capability resolver now turns `AudioOutput` on for a speech-synthesis
model, from two sources:

- a name signal (`internal/providers/tts_patterns.go`), shaped after the
  existing `looksLikeVisionModel` including its negative-first ordering;
- the registry's per-provider service kind, as a fallback.

The name signal does the work. The registry kind cannot stand alone: it needs
an exact `ProviderModelKinds[provider][model]` hit, which a custom provider
node never has — no registry entry and no alias — so the reported case, an
operator-defined node with prefix `studio`, is reachable by name only. The same
gap hit a builtin provider carrying a TTS id without a declared kind (`vertex`,
`ag`) and the eight mimo TTS pairs on `xiaomi-tokenplan` / `xmtp`. Removing the
kind check breaks no test today; it is kept as deliberate slack for a future
classified id whose name carries no signal at all.

Speech recognition is excluded explicitly rather than by omission, because
`speech` on its own is a synthesis signal (minimax `speech-2.8-hd`): `stt`,
`asr`, `whisper`, `transcri`, `speech-to-text` and voice recognition are
checked first and can never match.

The rule only ever turns audio output ON. It deliberately does not clear
`Reasoning` or the thinking format: upstream advertises thinking levels for its
TTS models, and an earlier attempt at this fix used a first-match pattern row
that tidied those away, which broke parity on all 13 pairs pinned in
`testdata/thinking_levels.json`.

Tests name every provider/model pair literally, including the three call shapes
`models_list.go` produces (`(providerID, modelID)`, `(alias, modelID)` and
`(prefix, cm.ID)`). An earlier draft iterated `ProviderModels` and skipped pairs
whose registry kind was not `tts`, which made it vacuous by construction — it
could only check the pairs the registry rule was guaranteed to reach, and it
passed while the reported id was still broken. A mutation run confirms the
current tests fail when the name signal is removed.

Out of scope, reported alongside this and untouched: `GET /v1/models?kind=`
does not filter on the requested kind, and `GET /v1/audio/voices` returns 322
edge/azure voices that Gemini TTS models reject.
