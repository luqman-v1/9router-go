### fix(dashboard): store a custom model's real service kind instead of always "llm"

Importing models onto a compatible provider node filed every one of them as an
LLM. The gateway believes that: `modelKind()` reads a custom row's stored `type`
before anything else, so a row saying `llm` is an LLM even when its id ends in
`-tts`. On an AI Studio node that put four non-chat models in the default
`/v1/models` listing —

- `studio/veo-3.1-generate-preview` (video)
- `studio/lyria-3.5` (music)
- `studio/gemini-3.8-flash-tts` (speech)
- `studio/gemini-nano-banana-2.1` (image)

— where a client filtering for chat models would call them and get nothing
usable back.

The six import and add paths in `ProviderDetailView.svelte` hardcoded
`type: 'llm'` and built the `alias|id|type` key with a literal `|llm`. They now
infer the kind from the id through a new `web/src/lib/modelKindInference.ts`,
whose regexes are ported from upstream `inferKindFromUnknownModelId`
(`src/app/api/v1/models/route.js`) in upstream's order — embedding, then tts,
then image — so a row written here resolves to the kind the Node gateway would
infer for the same id. The backend already honoured a per-entry `type` and only
defaulted to `llm` when one was absent, so no server change was needed.

Two kinds of id needed care beyond the upstream port:

- Upstream's `audio` branch claims `stepaudio-2.5-chat` and
  `gpt-4o-audio-preview`, which are audio-capable *chat* models. Filing one as
  TTS removes it from the default listing, a worse failure than leaving a media
  model in, so chat spellings are excluded before any positive hint is tried.
- `speech` alone is a synthesis signal (minimax `speech-2.8-hd`), so the
  recognition direction — `stt`, `asr`, `whisper`, `transcri`, `speech-to-text`
  — is excluded explicitly or whisper reads as TTS.

Storing the real kind then exposed three places that assumed every custom row
was an LLM, all fixed here:

- the compatible-models list dropped rows whose type was not `llm`, which is
  the only screen where a node's custom models can be seen, tested or deleted —
  so a correctly-filed TTS row became unreachable: absent from `/v1/models`, and
  absent from the one place that could remove it;
- the four delete paths addressed `alias|id|llm` literally, which would not
  match a row stored under its real kind;
- "Test all models" probes with a chat request, so it now skips media rows
  rather than reporting a working TTS model as dead.

Existing rows are left alone. The nine studio rows already in `kv` carry
`type: llm` and their keys embed `|llm`, so re-filing them is a data migration
with its own blast radius, not part of this change.

`web/src/lib/modelKindInference.test.ts` covers the four leaked ids, the chat
models that must stay LLM, the audio-chat and speech-recognition exclusions, the
architecture-named synthesis models, and the stored-kind-over-inference rule the
delete paths depend on.

Out of scope, reported alongside this: `GET /v1/models?kind=` is ignored
(upstream hardcodes `buildModelsList([LLM_KIND])` and reads no query param, so
honouring it is a deliberate divergence, not a fix), and `GET /v1/models/{kind}`
returns providers where upstream returns models.
