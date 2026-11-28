### Preserve reasoning effort max for GPT-5.6 / GPT-6 on OpenAI-compatible nodes

OpenAI-compatible custom nodes (`openai-compatible-*`) now advertise the
`"max"` reasoning effort for GPT-5.6 / GPT-6 model ids, matching the
GPT-5.6 Codex family's level set, instead of clamping to `"xhigh"`.
The rule is scoped to the case where no registry model entry and no
`patternThinking` rule matched, so an explicit model/pattern rule still
wins, and the built-in `openai` lane is untouched (strict provider
isolation). `thinking.Apply` / `thinking.ApplyJSON` preserve `"max"` on
both `reasoning_effort` and `output_config.effort` for these nodes while
clamping `"gpt-5"` and the built-in `openai` lane to `"xhigh"`.

Tests: `internal/providers/thinking_levels_compat_test.go`,
`internal/thinking/openai_compat_max_effort_test.go`.
