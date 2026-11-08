### fix(providers): give Gemini Omni a thinking level its upstream accepts

AI Studio answers `400 protocol error 3: 'medium' is not a supported thinking
level for this model. Allowed values are: high, low.` for the Gemini Omni
family. Two gaps in the catalog let a rejected level through:

- `internal/providers/capabilities.go` had no `*gemini*omni*` row, so the
  generic `*gemini*` fallback won with `Reasoning: false`. `GetThinkingLevels`
  then returned `nil`, and with no declared set the thinking pass took the
  `caps.Reasoning` guard and **stripped** the request's thinking fields instead
  of clamping them — an Omni request silently lost its reasoning.
- `internal/providers/thinking_levels.go` had no `patternThinking` entry, so the
  `gemini-level` default `{minimal, low, medium, high}` applied and `medium`
  counted as valid.

Both rows are now present and sit above `*gemini-4*` / `*gemini-3.8*`: the
tables are first-match-wins, so an Omni id carrying a series number
(`gemini-4-omni-*`) would otherwise be claimed by the numbered pattern and
advertise a `medium` the model rejects.

The declared set is `{"high"}` rather than `{"high", "low"}` on purpose. The
OpenAI lane (`levelFor`) clamps by effort rank, not by declaration order, so a
set holding `"low"` sends every sub-high level down to `"low"` regardless of
element order — including the `medium` that `TranslateClaudeToOpenAI` injects as
the default effort for an enabled-thinking body with no mappable budget. Pinning
`high` keeps an Omni request on the tier the model actually serves. The
trade-off: an explicit `low` is also raised to `high`, which this lane cannot
distinguish.

Verified on the real entry point (`thinking.ApplyJSON`, the call site in
`internal/handlers/chat/fallback.go`) rather than on `providers.ClampGeminiLevel`,
which has no production caller. `medium`, `minimal`, `low`, `high`, `none` and
`xhigh` all reach the wire as `high` on the OpenAI, Responses and Gemini lanes,
and a body that asks for nothing stays byte-identical.

Also pinned as pre-existing behaviour, not fixed here: `NormalizeOpenAILevel`
routes `max` and `ultra` to `xhigh` without consulting the declared set, so both
escape on every `gemini-level` model — `gemini-4-argon` and `gemini-3.8-flash`
included. The fix belongs with the shared normalizer; the test fails the day it
learns to clamp onto the declared set, so the change is made deliberately.
