### feat(gateway): serve the Gemini API surface at /v1beta

A Gemini SDK — the `@google/genai` client, the Python SDK, plain curl — speaks
only one protocol: it lists models, then posts `contents[]` to an
action-suffixed path. The gateway routed everything else but had no such routes,
so a Gemini client could not use it at all. The audit called this the most
visible of the remaining gaps.

`GET /v1beta/models` answers the SDK's list shape (`models[].name` plus
`supportedGenerationMethods`); without it a client reports "no models
available" and never tries a turn. `POST /v1beta/models/{model}:generateContent`
and `:streamGenerateContent` accept both the one-segment and two-segment path
forms.

The handler is a translation sandwich: the Gemini body becomes a Chat body, the
existing Chat pipeline answers it, and the answer becomes a
`GenerateContentResponse`. Every part of the gateway a Chat request gets — model
resolution, combos, per-key policy, fallback, usage accounting — therefore
applies unchanged, instead of being reimplemented on a second path.

Two details the SDK is strict about:

- Gemini SSE has **no `[DONE]` sentinel** — the stream ends by closing, and
  emitting one makes the client report a parse error on the last frame;
- finish reasons are Gemini's vocabulary (`length` → `MAX_TOKENS`,
  `content_filter` → `SAFETY`), because a client switches on them.

A reasoning delta becomes a `thought: true` part, which is how the SDK renders
thinking separately from the answer.
