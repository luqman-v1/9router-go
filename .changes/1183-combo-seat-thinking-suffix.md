### fix(chat): strip thinking suffix before combo seat capability lookup

A combo seat like `deepseek/deepseek-chat(max)` carried its thinking suffix
into `comboSeatCapabilities`, so the capability table was keyed by a suffixed
model id that never matches. The seat fell through to the floor capability
instead of publishing the bare model's own block (`context_length` /
`max_completion_tokens`), and combos built on that seat advertised wrong limits.

`comboSeatCapabilities` now strips the trailing `(level)` thinking suffix
(using `thinking.StripSuffix`, the same helper `resolveModel` uses) before the
capability lookup, so suffixed seats resolve to the same capability block as
their bare model.

Adds `TestHandleModels_ComboSeatThinkingSuffixStrippedBeforeCaps` covering a
`(max)`-suffixed deepseek seat publishing the bare model's 131072 / 8192 limits.
