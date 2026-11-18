### fix(chat): strip thinking suffix before combo seat capability lookup

A combo seat like `oc/deepseek-v4.1-flash-free(max)` carried its thinking suffix
into `comboSeatCapabilities`, so the capability table was keyed by a suffixed
model id that never matches. The seat fell through to the pattern-table
default instead of publishing the bare model's own block (`context_length` /
`max_completion_tokens`), and combos built on that seat advertised a window the
model does not have.

`comboSeatCapabilities` now strips the trailing `(level)` thinking suffix
(using `thinking.StripSuffix`, the same helper `resolveModel` uses) before the
capability lookup, so suffixed seats resolve to the same capability block as
their bare model.

Adds `TestHandleModels_ComboSeatThinkingSuffixStrippedBeforeCaps` covering a
`(max)`-suffixed opencode seat publishing the bare model's 330000 / 128000
limits.
