### Added — Per-model custom request body for OpenRouter custom models

- A custom model can now carry an `extraBody` JSON object (Add Custom Model
  modal → "Custom request body (JSON, optional)", shown for OpenRouter only).
  The gateway merges it into every OpenAI-format request it sends to
  `openrouter` for that model, so operator sampling and reasoning knobs apply
  without the client sending them.
- Allowlist, not denylist. Only these top-level keys (exact, case-sensitive)
  are accepted: `temperature`, `top_p`, `top_k`, `min_p`, `top_a`,
  `repetition_penalty`, `frequency_penalty`, `presence_penalty`, `seed`,
  `stop`, `logit_bias`, `logprobs`, `top_logprobs`, `max_completion_tokens`,
  `reasoning`, `reasoning_effort`. No provider routing (`provider`,
  `plugins`, `models`, ...) and nothing the gateway owns (`model`, `messages`,
  `stream`, `tools`, ...). Values are type-checked only (no numeric ranges;
  integer keys accept integer-valued numbers; `null` is allowed).
- `reasoning` may only hold `effort` (max|xhigh|high|medium|low|minimal|none),
  `max_tokens` (int), `enabled`, `exclude` (bool), `summary`
  (auto|concise|detailed), `context` (auto|all_turns|current_turn), `mode`
  (standard|pro). `reasoning.max_tokens` together with `reasoning.effort` or
  `reasoning_effort` is refused (OpenRouter answers 400).
- Merge rule: shallow top-level override, operator wins; `reasoning` is
  replaced whole.
- Save is refused with `400 {"code":"INVALID_EXTRA_BODY"}` (message names the
  offending key and lists the allowed ones) when the row's provider does not
  resolve to `openrouter`, the value is not a JSON object, it exceeds 8 KB, or
  a key/value fails the allowlist. The same allowlist is re-applied at merge
  time, so a legacy or imported row (e.g. with `provider`) never forwards a
  disallowed key. The stored extraBody keeps the request's exact bytes, so
  large integers (e.g. a 17-digit `seed`) are not rounded. The modal mirrors
  the rules inline and shows the accepted keys, an example and links to the
  OpenRouter parameters and reasoning-token guides.
- The lookup is an in-memory index built from the `customModels` kv scope and
  invalidated (generation counter, so a racing build is never cached) on every
  custom-model write, provider-node rename/delete and database import — no
  SQLite read per request; a failed read is retried, not cached as empty.
  A client body the merge cannot decode is forwarded unchanged. Other
  providers, including a sibling with the same model id, are untouched (§3
  provider isolation).
- Divergence: no merged `decolua/9router` equivalent exists; this is a
  9router-go addition.
- Verified by db round-trip, dashboard validation, merge unit tests and an
  `integration` test that asserts the fake OpenRouter upstream receives the
  merged body (direct and through a combo) while a sibling provider does not.
