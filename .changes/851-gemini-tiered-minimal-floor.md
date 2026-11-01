### fix(thinking): floor MINIMAL tier on tiered Gemini 3.6/3.7/3.8 flash

- In `internal/providers/thinking_levels.go`, add pattern entries for `*gemini-3.7*` and `*gemini-3.6*` declaring levels `["low", "medium", "high"]` (porting upstream `decolua/9router` PR #5355).
- Prevents HTTP 400 "Thinking level MINIMAL is not supported for this model" when tiered Gemini 3.6/3.7/3.8 flash models are called with `none` or `minimal` reasoning effort.
- Floors minimal-producing intents to `low` while preserving `minimal` tier for original Gemini 3 models (`gemini-3-flash-preview`, `gemini-3.1-pro-preview`).
- Added unit tests in `internal/providers/gemini_tiered_minimal_test.go` and updated thinking levels fixture.
