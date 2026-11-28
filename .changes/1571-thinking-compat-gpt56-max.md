### Fixed

- Preserve `"max"` reasoning effort for GPT-5.6 / GPT-6 on OpenAI-compatible
  custom nodes (`openai-compatible-*`), matching the GPT-5.6 Codex family's
  level set instead of clamping to `"xhigh"`. The compat fallback is scoped
  to the case where no registry model entry and no `patternThinking` rule
  matched, so an explicit model/pattern rule still wins, and the built-in
  `openai` lane is untouched (strict provider isolation).
- `thinking.Apply` / `thinking.ApplyJSON` normalize `output_config.effort: "max"` to `reasoning_effort: "max"` on OpenAI targets for these nodes while clamping `"gpt-5"` and the built-in `openai` lane to `"xhigh"`.
- Added `internal/providers/thinking_levels_compat_test.go` and
  `internal/thinking/openai_compat_max_effort_test.go`.
